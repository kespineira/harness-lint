package health

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/capture"
	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/hooks"
	"github.com/kespineira/harness-lint/internal/store"
)

type fakeHooks struct {
	report hooks.StatusReport
	err    error
	calls  int
}

func (f *fakeHooks) Status(context.Context) (hooks.StatusReport, error) {
	f.calls++
	return f.report, f.err
}

type fakeStore struct {
	schema        store.SchemaStatus
	schemaErr     error
	selfTestErr   error
	delivery      capture.DeliveryHealth
	deliveryErr   error
	schemaCalls   int
	selfTestCalls int
	deliveryCalls int
}

func (f *fakeStore) SchemaStatus(context.Context) (store.SchemaStatus, error) {
	f.schemaCalls++
	return f.schema, f.schemaErr
}

func (f *fakeStore) SelfTestCaptureIngest(context.Context) error {
	f.selfTestCalls++
	return f.selfTestErr
}

func (f *fakeStore) GetCaptureHealth(context.Context, domain.Runtime) (capture.DeliveryHealth, error) {
	f.deliveryCalls++
	return f.delivery, f.deliveryErr
}

func installedHooksReport(runtime hooks.Runtime, resolved bool) hooks.StatusReport {
	events := []string{"PostToolUse"}
	if runtime == hooks.RuntimeClaude {
		events = []string{"PostToolUse", "PostToolUseFailure", "UserPromptExpansion"}
	}
	entries := make([]hooks.ManagedEntry, 0, len(events))
	for _, event := range events {
		entries = append(entries, hooks.ManagedEntry{
			Event:         event,
			State:         hooks.ManagedEntryInstalled,
			ExactHandlers: 1,
		})
	}
	return hooks.StatusReport{
		Runtime:        runtime,
		Code:           hooks.StatusInstalled,
		ConfigExists:   true,
		Managed:        hooks.ManagedEntryInstalled,
		ManagedEntries: entries,
		Binary:         hooks.BinaryResolution{Name: hooks.BinaryName, Resolved: resolved},
	}
}

func installedSnapshot(runtime domain.Runtime) Snapshot {
	hookRuntime := hooks.RuntimeCodex
	if runtime == domain.RuntimeClaudeCode {
		hookRuntime = hooks.RuntimeClaude
	}
	return Snapshot{
		Runtime:           runtime,
		HookStatus:        installedHooksReport(hookRuntime, true),
		DatabaseAvailable: true,
		Schema:            store.SchemaStatus{Current: 6, Latest: 6},
		Delivery:          capture.DeliveryHealth{Runtime: runtime},
	}
}

func deliveryAt(runtime domain.Runtime, success, failure time.Time, failures int) capture.DeliveryHealth {
	result := capture.DeliveryHealth{Runtime: runtime, ConsecutiveFailures: failures}
	if !success.IsZero() {
		result.LastSuccessfulDelivery = &success
	}
	if !failure.IsZero() {
		result.LastFailedDelivery = &failure
		kind := capture.FailureInternalError
		result.LastFailureKind = &kind
	}
	return result
}

func TestAssessHealthyAndIdle(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		health capture.DeliveryHealth
		want   State
	}{
		{name: "idle", health: capture.DeliveryHealth{Runtime: domain.RuntimeCodex}, want: Idle},
		{name: "healthy", health: deliveryAt(domain.RuntimeCodex, base, time.Time{}, 0), want: Healthy},
		{name: "old success stays healthy", health: deliveryAt(domain.RuntimeCodex, base, time.Time{}, 0), want: Healthy},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := installedSnapshot(domain.RuntimeCodex)
			snapshot.Delivery = test.health
			snapshot.Now = base.Add(365 * 24 * time.Hour)
			got := Assess(snapshot)
			if got.State != test.want {
				t.Fatalf("Assess() state = %q, want %q (report=%#v)", got.State, test.want, got)
			}
		})
	}
}

func TestAssessFailureOrderingAndRecovery(t *testing.T) {
	base := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		health capture.DeliveryHealth
		want   State
	}{
		{
			name:   "failure later than success degrades",
			health: deliveryAt(domain.RuntimeCodex, base, base.Add(time.Minute), 1),
			want:   Degraded,
		},
		{
			name:   "later success with zero failures recovers",
			health: deliveryAt(domain.RuntimeCodex, base.Add(2*time.Minute), base.Add(time.Minute), 0),
			want:   Healthy,
		},
		{
			name:   "failure before success is healthy",
			health: deliveryAt(domain.RuntimeCodex, base.Add(time.Minute), base, 0),
			want:   Healthy,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := installedSnapshot(domain.RuntimeCodex)
			snapshot.Delivery = test.health
			got := Assess(snapshot)
			if got.State != test.want {
				t.Fatalf("Assess() state = %q, want %q (components=%#v)", got.State, test.want, got.Components)
			}
		})
	}
}

func TestAssessStaticComponentFailuresAreDistinctAndBroken(t *testing.T) {
	base := installedSnapshot(domain.RuntimeCodex)
	tests := []struct {
		name   string
		mutate func(*Snapshot)
		want   State
		check  func(Components) ComponentState
	}{
		{
			name: "no config",
			mutate: func(s *Snapshot) {
				s.HookStatus = hooks.StatusReport{Runtime: hooks.RuntimeCodex, Code: hooks.StatusConfigurationNotFound, Binary: hooks.BinaryResolution{Resolved: true}}
			},
			want:  Broken,
			check: func(c Components) ComponentState { return c.Config.State },
		},
		{
			name: "malformed config",
			mutate: func(s *Snapshot) {
				s.HookStatus = hooks.StatusReport{Runtime: hooks.RuntimeCodex, Code: hooks.StatusMalformed, ConfigExists: true, Binary: hooks.BinaryResolution{Resolved: true}}
			},
			want:  Broken,
			check: func(c Components) ComponentState { return c.Config.State },
		},
		{
			name: "missing executable",
			mutate: func(s *Snapshot) {
				s.HookStatus.Binary.Resolved = false
			},
			want:  Broken,
			check: func(c Components) ComponentState { return c.Executable.State },
		},
		{
			name: "SQLite unavailable",
			mutate: func(s *Snapshot) {
				s.DatabaseAvailable = false
			},
			want:  Broken,
			check: func(c Components) ComponentState { return c.Database.State },
		},
		{
			name: "schema mismatch",
			mutate: func(s *Snapshot) {
				s.Schema = store.SchemaStatus{Current: 5, Latest: 6}
			},
			want:  Broken,
			check: func(c Components) ComponentState { return c.Schema.State },
		},
	}
	wantChecks := []ComponentState{ComponentMissing, ComponentMalformed, ComponentUnresolved, ComponentUnavailable, ComponentMismatch}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := base
			test.mutate(&snapshot)
			got := Assess(snapshot)
			if got.State != test.want {
				t.Fatalf("Assess() state = %q, want %q", got.State, test.want)
			}
			if gotState := test.check(got.Components); gotState != wantChecks[index] {
				t.Fatalf("component state = %q, want %q (components=%#v)", gotState, wantChecks[index], got.Components)
			}
		})
	}
}

func TestEvaluateUsesOnlyReadOnlyInterfacesAndSelfTest(t *testing.T) {
	storeFake := &fakeStore{
		schema:   store.SchemaStatus{Current: 6, Latest: 6},
		delivery: capture.DeliveryHealth{Runtime: domain.RuntimeCodex},
	}
	hooksFake := &fakeHooks{report: installedHooksReport(hooks.RuntimeCodex, true)}
	result, err := Evaluate(context.Background(), Inputs{
		Runtime: domain.RuntimeCodex,
		Hooks:   hooksFake,
		Store:   storeFake,
	})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if result.State != Idle {
		t.Fatalf("Evaluate() state = %q, want idle", result.State)
	}
	if hooksFake.calls != 1 || storeFake.schemaCalls != 1 || storeFake.selfTestCalls != 1 || storeFake.deliveryCalls != 1 {
		t.Fatalf("read-only calls = hooks=%d schema=%d self-test=%d delivery=%d", hooksFake.calls, storeFake.schemaCalls, storeFake.selfTestCalls, storeFake.deliveryCalls)
	}
}

func TestEvaluateHealthUnavailableAndPrivacySafe(t *testing.T) {
	sentinel := "PROMPT_SENTINEL args=ARGS_SENTINEL output=OUTPUT_SENTINEL command=COMMAND_SENTINEL"
	invalidKind := capture.FailureKind(sentinel)
	result, err := Evaluate(context.Background(), Inputs{
		Runtime:       domain.RuntimeCodex,
		Hooks:         &fakeHooks{err: errors.New(sentinel)},
		StoreOpenErr:  errors.New(sentinel),
		HookStatusErr: errors.New(sentinel),
	})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if result.State != Broken && result.State != Unknown {
		t.Fatalf("unavailable state = %q, want broken or unknown", result.State)
	}
	result = Assess(Snapshot{
		Runtime:           domain.RuntimeCodex,
		HookStatus:        installedHooksReport(hooks.RuntimeCodex, true),
		DatabaseAvailable: true,
		Schema:            store.SchemaStatus{Current: 6, Latest: 6},
		Delivery:          capture.DeliveryHealth{Runtime: domain.RuntimeCodex, LastFailureKind: &invalidKind},
	})
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal health report: %v", err)
	}
	if strings.Contains(string(encoded), sentinel) {
		t.Fatalf("health report retained raw sentinel: %s", encoded)
	}
}
