// Package health evaluates direct-hook readiness without invoking a runtime.
//
// Health is deliberately a read-only, runtime-neutral diagnostic boundary. It
// inspects structural hook status, the executable lookup result, database
// schema/self-test state, and coarse delivery observations. It never receives
// or stores hook payloads, parser errors, commands, prompts, or tool output.
package health

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/kespineira/harness-lint/internal/capture"
	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/hooks"
	"github.com/kespineira/harness-lint/internal/store"
)

// State is the canonical aggregate capture-health state.
type State string

const (
	Healthy  State = "healthy"
	Idle     State = "idle"
	Degraded State = "degraded"
	Broken   State = "broken"
	Unknown  State = "unknown"

	// State-prefixed aliases keep call sites readable when several state enums
	// are in scope while preserving the concise canonical values above.
	StateHealthy  = Healthy
	StateIdle     = Idle
	StateDegraded = Degraded
	StateBroken   = Broken
	StateUnknown  = Unknown
)

func (s State) Valid() bool {
	switch s {
	case Healthy, Idle, Degraded, Broken, Unknown:
		return true
	default:
		return false
	}
}

// ComponentState describes one bounded readiness check. Values are stable
// categories only; component reports never retain source error text.
type ComponentState string

const (
	ComponentOK          ComponentState = "ok"
	ComponentNone        ComponentState = "none"
	ComponentMissing     ComponentState = "missing"
	ComponentMalformed   ComponentState = "malformed"
	ComponentIncomplete  ComponentState = "incomplete"
	ComponentUnresolved  ComponentState = "unresolved"
	ComponentUnavailable ComponentState = "unavailable"
	ComponentMismatch    ComponentState = "mismatch"
	ComponentFailed      ComponentState = "failed"
	ComponentUnsupported ComponentState = "unsupported"
	ComponentInvalid     ComponentState = "invalid"
	ComponentUnknown     ComponentState = "unknown"
)

// Check is a privacy-safe component result. It intentionally has no Detail,
// Error, Path, or source-data field.
type Check struct {
	State ComponentState
}

func (c Check) OK() bool { return c.State == ComponentOK || c.State == ComponentNone }

// Components groups the independent checks used to derive the aggregate
// state. Config and Hooks are both exposed: Config describes the file's
// existence/shape, while Hooks describes the structural status reported by
// the manager. ManagedEntries and Executable remain separate so callers can
// distinguish incomplete ownership from a missing PATH command.
type Components struct {
	Config         Check
	Hooks          Check
	ManagedEntries Check
	Executable     Check
	Database       Check
	Schema         Check
	SelfTest       Check
	Delivery       Check
}

// Report is the complete privacy-safe health result. Delivery contains only
// bounded timestamps, runtime, failure count, and the allow-listed failure
// kind from capture.DeliveryHealth.
type Report struct {
	Runtime    domain.Runtime
	State      State
	Components Components
	Delivery   capture.DeliveryHealth
}

// Result and Health are compatibility aliases for callers that prefer those
// names for an aggregate evaluator result.
type Result = Report
type Health = Report

// Snapshot is a pure evaluator input. It is useful for tests and callers that
// already performed the read-only dependency calls. Errors are consumed only
// to select a bounded component state and are never copied into Report.
type Snapshot struct {
	Runtime domain.Runtime

	HookStatus    hooks.StatusReport `json:"-"`
	HookStatusErr error              `json:"-"`

	DatabaseAvailable bool
	Schema            store.SchemaStatus     `json:"-"`
	SchemaErr         error                  `json:"-"`
	SelfTestErr       error                  `json:"-"`
	Delivery          capture.DeliveryHealth `json:"-"`
	DeliveryErr       error                  `json:"-"`

	// Now is intentionally ignored for state derivation. A successful capture
	// does not become broken merely because it is old.
	Now time.Time
}

// Inputs describes the dependencies for Evaluate. StoreOpenErr represents an
// unsuccessful open before a store could be handed to the evaluator. A nil
// Store is treated as an unavailable dependency, while a non-nil Store is
// checked only through store.HealthReader.
type Inputs struct {
	Runtime domain.Runtime     `json:"-"`
	Hooks   hooks.StatusReader `json:"-"`
	Store   store.HealthReader `json:"-"`

	StoreOpenErr error     `json:"-"`
	Now          time.Time `json:"-"`

	// HookStatus allows callers that already called hooks.Status to use the
	// same evaluator without another filesystem read. Hooks takes precedence
	// when non-nil.
	HookStatus    hooks.StatusReport `json:"-"`
	HookStatusErr error              `json:"-"`
}

// Input and Dependencies are aliases for the evaluator input vocabulary.
type Input = Inputs
type Dependencies = Inputs

// Evaluate reads the configured hook and store surfaces, then derives one
// canonical state. Component failures are represented in the returned report
// rather than returned as raw errors, so malformed configuration and database
// failures remain actionable without leaking paths or untrusted text.
func Evaluate(ctx context.Context, input Inputs) (Report, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}

	snapshot := Snapshot{
		Runtime:           input.Runtime,
		HookStatus:        input.HookStatus,
		HookStatusErr:     input.HookStatusErr,
		DatabaseAvailable: false,
		Now:               input.Now,
	}

	if input.Hooks != nil {
		snapshot.HookStatus, snapshot.HookStatusErr = input.Hooks.Status(ctx)
	}

	if snapshot.Runtime == domain.RuntimeUnknown {
		snapshot.Runtime = domainRuntime(snapshot.HookStatus.Runtime)
	}

	switch {
	case input.StoreOpenErr != nil:
		snapshot.DatabaseAvailable = false
	case input.Store != nil:
		snapshot.DatabaseAvailable = true
		snapshot.Schema, snapshot.SchemaErr = input.Store.SchemaStatus(ctx)
		snapshot.SelfTestErr = input.Store.SelfTestCaptureIngest(ctx)
		snapshot.Delivery, snapshot.DeliveryErr = input.Store.GetCaptureHealth(ctx, snapshot.Runtime)
	default:
		// A missing dependency without an open error is still unavailable to
		// diagnostics; keep all store-derived checks bounded and error-free.
		snapshot.SchemaErr = errMissingStore
		snapshot.SelfTestErr = errMissingStore
		snapshot.DeliveryErr = errMissingStore
	}

	return Assess(snapshot), nil
}

// EvaluateHealth is a concise alias for Evaluate.
func EvaluateHealth(ctx context.Context, input Inputs) (Report, error) {
	return Evaluate(ctx, input)
}

// Assess evaluates an already collected snapshot without calling any
// filesystem, database, runtime, model, MCP, or network operation.
func Assess(snapshot Snapshot) Report {
	runtimeName := snapshot.Runtime
	if runtimeName == domain.RuntimeUnknown {
		runtimeName = domainRuntime(snapshot.HookStatus.Runtime)
	}
	result := Report{
		Runtime: runtimeName,
		State:   Unknown,
	}
	if snapshot.HookStatus.Runtime == "" {
		snapshot.HookStatus.Runtime = hooksRuntime(runtimeName)
	}
	if runtimeName.Valid() {
		result.Delivery = sanitizeDelivery(snapshot.Delivery, runtimeName)
	} else {
		result.Delivery = sanitizeDelivery(snapshot.Delivery, domain.RuntimeUnknown)
	}

	result.Components = assessComponents(snapshot)
	if !runtimeName.Valid() {
		return result
	}

	if hardFailure(result.Components) {
		if hasUnknownComponent(result.Components) && !hasKnownComponentFailure(result.Components) {
			result.State = Unknown
		} else {
			result.State = Broken
		}
		return result
	}
	if result.Components.Delivery.State == ComponentUnknown || result.Components.Delivery.State == ComponentUnavailable || result.Components.Delivery.State == ComponentInvalid {
		result.State = Unknown
		return result
	}
	switch result.Components.Delivery.State {
	case ComponentNone:
		result.State = Idle
	case ComponentFailed:
		result.State = Degraded
	default:
		result.State = Healthy
	}
	return result
}

var errMissingStore = errors.New("health store dependency is missing")

func assessComponents(snapshot Snapshot) Components {
	var result Components
	databaseAvailable := snapshot.DatabaseAvailable
	statusRuntime := domainRuntime(snapshot.HookStatus.Runtime)
	if snapshot.HookStatusErr != nil || (snapshot.Runtime.Valid() && statusRuntime.Valid() && statusRuntime != snapshot.Runtime) {
		result.Config = Check{State: ComponentUnknown}
		result.Hooks = Check{State: ComponentUnknown}
	} else {
		result.Config = assessConfig(snapshot.HookStatus)
		result.Hooks = assessHooks(snapshot.HookStatus)
	}
	result.ManagedEntries = assessManagedEntries(snapshot.HookStatus)
	if snapshot.HookStatusErr != nil {
		result.ManagedEntries = Check{State: ComponentUnknown}
	}
	if snapshot.HookStatus.Binary.Resolved {
		result.Executable = Check{State: ComponentOK}
	} else if snapshot.HookStatusErr != nil {
		result.Executable = Check{State: ComponentUnknown}
	} else {
		result.Executable = Check{State: ComponentUnresolved}
	}

	if databaseAvailable {
		result.Database = Check{State: ComponentOK}
	} else {
		result.Database = Check{State: ComponentUnavailable}
	}

	switch {
	case !databaseAvailable:
		result.Schema = Check{State: ComponentUnavailable}
		result.SelfTest = Check{State: ComponentUnavailable}
	case snapshot.SchemaErr != nil:
		result.Schema = Check{State: ComponentUnavailable}
		result.SelfTest = Check{State: ComponentUnknown}
	case snapshot.Schema.Current <= 0 || snapshot.Schema.Latest <= 0 || snapshot.Schema.Current != snapshot.Schema.Latest:
		result.Schema = Check{State: ComponentMismatch}
		result.SelfTest = assessSelfTest(snapshot.SelfTestErr)
	default:
		result.Schema = Check{State: ComponentOK}
		result.SelfTest = assessSelfTest(snapshot.SelfTestErr)
	}
	result.Delivery = assessDelivery(snapshot)
	return result
}

func assessConfig(status hooks.StatusReport) Check {
	if !status.ConfigExists && status.Code == hooks.StatusConfigurationNotFound {
		return Check{State: ComponentMissing}
	}
	if status.Code == hooks.StatusMalformed {
		return Check{State: ComponentMalformed}
	}
	if status.Code == hooks.StatusInstalled {
		return Check{State: ComponentOK}
	}
	if status.ConfigExists {
		return Check{State: ComponentOK}
	}
	if status.Code == hooks.StatusUnsupported {
		return Check{State: ComponentUnsupported}
	}
	return Check{State: ComponentUnknown}
}

func assessHooks(status hooks.StatusReport) Check {
	switch status.Code {
	case hooks.StatusInstalled:
		return Check{State: ComponentOK}
	case hooks.StatusConfigurationNotFound, hooks.StatusNotInstalled:
		return Check{State: ComponentMissing}
	case hooks.StatusMalformed:
		return Check{State: ComponentMalformed}
	case hooks.StatusPartiallyInstalled:
		return Check{State: ComponentIncomplete}
	case hooks.StatusUnsupported:
		return Check{State: ComponentUnsupported}
	default:
		return Check{State: ComponentUnknown}
	}
}

func assessManagedEntries(status hooks.StatusReport) Check {
	if status.Managed != hooks.ManagedEntryInstalled || len(status.ManagedEntries) == 0 {
		return Check{State: ComponentIncomplete}
	}
	expected := expectedEvents(status.Runtime)
	if len(expected) == 0 || len(status.ManagedEntries) != len(expected) {
		return Check{State: ComponentIncomplete}
	}
	seen := make(map[string]struct{}, len(status.ManagedEntries))
	for _, entry := range status.ManagedEntries {
		if _, ok := expected[entry.Event]; !ok || strings.TrimSpace(entry.Event) == "" {
			return Check{State: ComponentIncomplete}
		}
		if _, duplicate := seen[entry.Event]; duplicate {
			return Check{State: ComponentIncomplete}
		}
		seen[entry.Event] = struct{}{}
		if entry.State != hooks.ManagedEntryInstalled || entry.ExactHandlers != 1 || entry.Partial != 0 {
			return Check{State: ComponentIncomplete}
		}
	}
	return Check{State: ComponentOK}
}

func expectedEvents(runtime hooks.Runtime) map[string]struct{} {
	switch runtime {
	case hooks.RuntimeClaude:
		return map[string]struct{}{
			"PostToolUse":         {},
			"PostToolUseFailure":  {},
			"UserPromptExpansion": {},
		}
	case hooks.RuntimeCodex:
		return map[string]struct{}{"PostToolUse": {}}
	default:
		return nil
	}
}

func assessSelfTest(err error) Check {
	if err != nil {
		return Check{State: ComponentFailed}
	}
	return Check{State: ComponentOK}
}

func assessDelivery(snapshot Snapshot) Check {
	if snapshot.DeliveryErr != nil {
		return Check{State: ComponentUnavailable}
	}
	health := snapshot.Delivery
	if !health.Runtime.Valid() && snapshot.Runtime.Valid() {
		health.Runtime = snapshot.Runtime
	}
	if !health.Runtime.Valid() || (snapshot.Runtime.Valid() && health.Runtime != snapshot.Runtime) {
		return Check{State: ComponentInvalid}
	}
	if err := health.Validate(); err != nil {
		return Check{State: ComponentInvalid}
	}
	if health.LastSuccessfulDelivery == nil && health.LastFailedDelivery == nil && health.ConsecutiveFailures == 0 && health.LastFailureKind == nil {
		return Check{State: ComponentNone}
	}
	if health.LastFailureKind != nil && health.LastFailedDelivery == nil {
		return Check{State: ComponentInvalid}
	}
	if health.ConsecutiveFailures > 0 || health.LastFailedDelivery != nil && (health.LastSuccessfulDelivery == nil || health.LastFailedDelivery.After(*health.LastSuccessfulDelivery)) {
		return Check{State: ComponentFailed}
	}
	return Check{State: ComponentOK}
}

func sanitizeDelivery(input capture.DeliveryHealth, runtime domain.Runtime) capture.DeliveryHealth {
	result := capture.DeliveryHealth{Runtime: runtime}
	if input.ConsecutiveFailures >= 0 && input.ConsecutiveFailures <= capture.MaxConsecutiveFailures {
		result.ConsecutiveFailures = input.ConsecutiveFailures
	}
	if input.LastSuccessfulDelivery != nil && !input.LastSuccessfulDelivery.IsZero() {
		value := input.LastSuccessfulDelivery.UTC()
		result.LastSuccessfulDelivery = &value
	}
	if input.LastFailedDelivery != nil && !input.LastFailedDelivery.IsZero() {
		value := input.LastFailedDelivery.UTC()
		result.LastFailedDelivery = &value
	}
	if input.LastFailureKind != nil && input.LastFailureKind.Valid() {
		value := *input.LastFailureKind
		result.LastFailureKind = &value
	}
	return result
}

func hardFailure(components Components) bool {
	for _, check := range []Check{
		components.Config,
		components.Hooks,
		components.ManagedEntries,
		components.Executable,
		components.Database,
		components.Schema,
		components.SelfTest,
	} {
		if check.State != ComponentOK {
			return true
		}
	}
	return false
}

func hasUnknownComponent(components Components) bool {
	for _, check := range []Check{
		components.Config,
		components.Hooks,
		components.ManagedEntries,
		components.Executable,
		components.Database,
		components.Schema,
		components.SelfTest,
		components.Delivery,
	} {
		if check.State == ComponentUnknown {
			return true
		}
	}
	return false
}

func hasKnownComponentFailure(components Components) bool {
	for _, check := range []Check{
		components.Config,
		components.Hooks,
		components.ManagedEntries,
		components.Executable,
		components.Database,
		components.Schema,
		components.SelfTest,
	} {
		if check.State != ComponentOK && check.State != ComponentUnknown {
			return true
		}
	}
	return false
}

func domainRuntime(runtime hooks.Runtime) domain.Runtime {
	switch runtime {
	case hooks.RuntimeClaude:
		return domain.RuntimeClaudeCode
	case hooks.RuntimeCodex:
		return domain.RuntimeCodex
	default:
		return domain.RuntimeUnknown
	}
}

func hooksRuntime(runtime domain.Runtime) hooks.Runtime {
	switch runtime {
	case domain.RuntimeClaudeCode:
		return hooks.RuntimeClaude
	case domain.RuntimeCodex:
		return hooks.RuntimeCodex
	default:
		return ""
	}
}

// Evaluator is a reusable dependency-backed evaluator. It is intentionally
// small so callers can construct it once while retaining the read-only
// interfaces rather than a concrete manager or store.
type Evaluator struct {
	Input Inputs
}

func NewEvaluator(input Inputs) Evaluator { return Evaluator{Input: input} }

func (e Evaluator) Evaluate(ctx context.Context) (Report, error) {
	return Evaluate(ctx, e.Input)
}
