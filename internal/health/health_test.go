package health

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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
	return hooks.StatusReport{
		Runtime:        runtime,
		Code:           hooks.StatusInstalled,
		ConfigExists:   true,
		Managed:        hooks.ManagedEntryInstalled,
		ManagedEntries: []hooks.ManagedEntry{{State: hooks.ManagedEntryInstalled, ExactHandlers: 1}},
		Binary:         hooks.BinaryResolution{Name: hooks.BinaryName, Resolved: resolved},
	}
}

func installedInputs(runtime domain.Runtime) (Inputs, *fakeHooks, *fakeStore) {
	hookRuntime := hooks.RuntimeCodex
	if runtime == domain.RuntimeClaudeCode {
		hookRuntime = hooks.RuntimeClaude
	}
	hooksFake := &fakeHooks{report: installedHooksReport(hookRuntime, true)}
	storeFake := &fakeStore{
		schema:   store.SchemaStatus{Current: 6, Latest: 6},
		delivery: capture.DeliveryHealth{Runtime: runtime},
	}
	return Inputs{Runtime: runtime, Hooks: hooksFake, Store: storeFake}, hooksFake, storeFake
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

func evaluate(t *testing.T, input Inputs) Report {
	t.Helper()
	result, err := Evaluate(context.Background(), input)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	return result
}

func TestEvaluateHealthyAndIdle(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name    string
		deliver capture.DeliveryHealth
		want    State
	}{
		{name: "idle", deliver: capture.DeliveryHealth{Runtime: domain.RuntimeCodex}, want: Idle},
		{name: "healthy", deliver: deliveryAt(domain.RuntimeCodex, base, time.Time{}, 0), want: Healthy},
		{name: "old success stays healthy", deliver: deliveryAt(domain.RuntimeCodex, base.Add(-365*24*time.Hour), time.Time{}, 0), want: Healthy},
	} {
		t.Run(test.name, func(t *testing.T) {
			input, _, storeFake := installedInputs(domain.RuntimeCodex)
			storeFake.delivery = test.deliver
			got := evaluate(t, input)
			if got.State != test.want {
				t.Fatalf("Evaluate() state = %q, want %q (report=%#v)", got.State, test.want, got)
			}
		})
	}
}

func TestEvaluateFailureOrderingAndRecovery(t *testing.T) {
	base := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		delivery capture.DeliveryHealth
		want     State
	}{
		{name: "failure later than success degrades", delivery: deliveryAt(domain.RuntimeCodex, base, base.Add(time.Minute), 1), want: Degraded},
		{name: "later success with zero failures recovers", delivery: deliveryAt(domain.RuntimeCodex, base.Add(2*time.Minute), base.Add(time.Minute), 0), want: Healthy},
		{name: "failure before success is healthy", delivery: deliveryAt(domain.RuntimeCodex, base.Add(time.Minute), base, 0), want: Healthy},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input, _, storeFake := installedInputs(domain.RuntimeCodex)
			storeFake.delivery = test.delivery
			got := evaluate(t, input)
			if got.State != test.want {
				t.Fatalf("Evaluate() state = %q, want %q (components=%#v)", got.State, test.want, got.Components)
			}
		})
	}
}

func TestEvaluateStaticFailuresAreDistinctAndBroken(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*fakeHooks, *fakeStore)
		want   State
		check  func(Components) ComponentState
		state  ComponentState
	}{
		{
			name: "no config",
			mutate: func(h *fakeHooks, _ *fakeStore) {
				h.report = hooks.StatusReport{Runtime: hooks.RuntimeCodex, Code: hooks.StatusConfigurationNotFound, Binary: hooks.BinaryResolution{Resolved: true}}
			},
			want: Broken, check: func(c Components) ComponentState { return c.Config.State }, state: ComponentMissing,
		},
		{
			name: "malformed config",
			mutate: func(h *fakeHooks, _ *fakeStore) {
				h.report = hooks.StatusReport{Runtime: hooks.RuntimeCodex, Code: hooks.StatusMalformed, ConfigExists: true, Binary: hooks.BinaryResolution{Resolved: true}}
			},
			want: Broken, check: func(c Components) ComponentState { return c.Config.State }, state: ComponentMalformed,
		},
		{
			name:   "missing executable",
			mutate: func(h *fakeHooks, _ *fakeStore) { h.report.Binary.Resolved = false },
			want:   Broken, check: func(c Components) ComponentState { return c.Executable.State }, state: ComponentUnresolved,
		},
		{
			name:   "SQLite unavailable",
			mutate: func(_ *fakeHooks, s *fakeStore) { s.schemaErr = errors.New("sqlite unavailable") },
			want:   Broken, check: func(c Components) ComponentState { return c.Schema.State }, state: ComponentUnavailable,
		},
		{
			name:   "schema mismatch",
			mutate: func(_ *fakeHooks, s *fakeStore) { s.schema = store.SchemaStatus{Current: 5, Latest: 6} },
			want:   Broken, check: func(c Components) ComponentState { return c.Schema.State }, state: ComponentMismatch,
		},
		{
			name:   "self-test failure",
			mutate: func(_ *fakeHooks, s *fakeStore) { s.selfTestErr = errors.New("self-test failed") },
			want:   Broken, check: func(c Components) ComponentState { return c.SelfTest.State }, state: ComponentFailed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input, hooksFake, storeFake := installedInputs(domain.RuntimeCodex)
			test.mutate(hooksFake, storeFake)
			got := evaluate(t, input)
			if got.State != test.want {
				t.Fatalf("Evaluate() state = %q, want %q", got.State, test.want)
			}
			if gotState := test.check(got.Components); gotState != test.state {
				t.Fatalf("component state = %q, want %q (components=%#v)", gotState, test.state, got.Components)
			}
		})
	}
}

func TestEvaluateInstalledStatusWithoutConfigIsUnknown(t *testing.T) {
	input, hooksFake, _ := installedInputs(domain.RuntimeCodex)
	hooksFake.report.ConfigExists = false
	got := evaluate(t, input)
	if got.State != Unknown {
		t.Fatalf("installed status without config = %q, want unknown", got.State)
	}
	if got.Components.Config.State != ComponentUnknown {
		t.Fatalf("installed status without config component = %q, want unknown", got.Components.Config.State)
	}
}

func TestEvaluateManagedCompletenessUsesReturnedStatusEntries(t *testing.T) {
	input, hooksFake, _ := installedInputs(domain.RuntimeCodex)
	hooksFake.report.ManagedEntries = append(hooksFake.report.ManagedEntries, hooks.ManagedEntry{State: hooks.ManagedEntryInstalled, ExactHandlers: 1})
	got := evaluate(t, input)
	if got.State != Idle {
		t.Fatalf("extra returned managed entry changed state = %q, want idle", got.State)
	}
	hooksFake.report.ManagedEntries[0].Partial = 1
	got = evaluate(t, input)
	if got.State != Broken || got.Components.ManagedEntries.State != ComponentIncomplete {
		t.Fatalf("partial returned entry = %#v, want broken/incomplete", got)
	}
}

func TestEvaluateRuntimeStatusMismatchIsUnknown(t *testing.T) {
	input, hooksFake, _ := installedInputs(domain.RuntimeCodex)
	hooksFake.report.Runtime = hooks.RuntimeClaude
	got := evaluate(t, input)
	if got.State != Unknown {
		t.Fatalf("runtime status mismatch = %q, want unknown", got.State)
	}
	if got.Components.Config.State != ComponentUnknown || got.Components.Hooks.State != ComponentUnknown {
		t.Fatalf("runtime status mismatch components = %#v, want unknown hook checks", got.Components)
	}
}

func TestEvaluateUsesOnlyReadOnlyInterfacesAndSelfTest(t *testing.T) {
	input, hooksFake, storeFake := installedInputs(domain.RuntimeCodex)
	result := evaluate(t, input)
	if result.State != Idle {
		t.Fatalf("Evaluate() state = %q, want idle", result.State)
	}
	if hooksFake.calls != 1 || storeFake.schemaCalls != 1 || storeFake.selfTestCalls != 1 || storeFake.deliveryCalls != 1 {
		t.Fatalf("read-only calls = hooks=%d schema=%d self-test=%d delivery=%d", hooksFake.calls, storeFake.schemaCalls, storeFake.selfTestCalls, storeFake.deliveryCalls)
	}
}

func TestEvaluateUnknownDependencyErrorAndContextCancellation(t *testing.T) {
	sentinel := "PROMPT_SENTINEL args=ARGS_SENTINEL output=OUTPUT_SENTINEL command=COMMAND_SENTINEL"
	input, _, _ := installedInputs(domain.RuntimeCodex)
	input.Hooks = &fakeHooks{err: errors.New(sentinel)}
	got := evaluate(t, input)
	if got.State != Unknown {
		t.Fatalf("unknown hook status = %q, want unknown", got.State)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal health report: %v", err)
	}
	if strings.Contains(string(encoded), sentinel) {
		t.Fatalf("health output retained sentinel: %s", encoded)
	}

	input, _, storeFake := installedInputs(domain.RuntimeCodex)
	storeFake.deliveryErr = errors.New(sentinel)
	got = evaluate(t, input)
	if got.State != Unknown {
		t.Fatalf("unknown delivery error = %q, want unknown", got.State)
	}
	encoded, err = json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal delivery-error report: %v", err)
	}
	if strings.Contains(string(encoded), sentinel) {
		t.Fatalf("delivery error leaked into health report: %s", encoded)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Evaluate(ctx, input)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Evaluate() error = %v, want context canceled", err)
	}
}

func TestEvaluateStoreOpenFailureIsBrokenAndPrivacySafe(t *testing.T) {
	sentinel := "PROMPT_SENTINEL args=ARGS_SENTINEL output=OUTPUT_SENTINEL command=COMMAND_SENTINEL"
	input, _, _ := installedInputs(domain.RuntimeCodex)
	input.Store = nil
	input.StoreOpenErr = errors.New(sentinel)
	got := evaluate(t, input)
	if got.State != Broken || got.Components.Database.State != ComponentUnavailable {
		t.Fatalf("store open failure = %#v, want broken/unavailable", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal health report: %v", err)
	}
	if strings.Contains(string(encoded), sentinel) {
		t.Fatalf("store error leaked into health report: %s", encoded)
	}
}

func TestEvaluateRealStoreSelfTestDoesNotPolluteState(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	input, _, _ := installedInputs(domain.RuntimeCodex)
	input.Store = db
	beforeEvents, err := db.ListUsageEvents(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	beforeHealth, err := db.GetCaptureHealth(context.Background(), domain.RuntimeCodex)
	if err != nil {
		t.Fatal(err)
	}
	got := evaluate(t, input)
	if got.State != Idle {
		t.Fatalf("real-store health = %q, want idle", got.State)
	}
	afterEvents, err := db.ListUsageEvents(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	afterHealth, err := db.GetCaptureHealth(context.Background(), domain.RuntimeCodex)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeEvents, afterEvents) || !reflect.DeepEqual(beforeHealth, afterHealth) {
		t.Fatalf("self-test polluted store: before events=%#v health=%#v after events=%#v health=%#v", beforeEvents, beforeHealth, afterEvents, afterHealth)
	}
}
