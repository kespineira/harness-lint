// Package health evaluates direct-hook readiness without invoking a runtime.
//
// The package is a read-only, runtime-neutral diagnostic boundary. It inspects
// structural hook status, executable resolution, database schema/self-test
// state, and coarse delivery observations; it never receives or stores hook
// payloads, parser errors, commands, prompts, or tool output.
package health

import (
	"context"
	"errors"

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
)

func (s State) Valid() bool {
	switch s {
	case Healthy, Idle, Degraded, Broken, Unknown:
		return true
	default:
		return false
	}
}

// ComponentState is a bounded category for one readiness check. Reports do
// not retain source errors, paths, or untrusted input.
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

// Check is a privacy-safe component result.
type Check struct {
	State ComponentState
}

// Components exposes independent checks so callers can distinguish missing
// or malformed hook configuration, incomplete ownership, executable lookup,
// database availability, schema, self-test, and delivery state.
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

// Report is the privacy-safe health result. Delivery contains only bounded
// timestamps, runtime, failure count, and an allow-listed failure kind.
type Report struct {
	Runtime    domain.Runtime
	State      State
	Components Components
	Delivery   capture.DeliveryHealth
}

// Inputs contains only the narrow read-only dependency surfaces needed by
// Evaluate. StoreOpenErr represents a failure before a store was available;
// its text is consumed only as a presence signal and is never returned.
type Inputs struct {
	Runtime domain.Runtime
	Hooks   hooks.StatusReader
	Store   store.HealthReader

	StoreOpenErr error
}

// Evaluate reads hook status and store health, then derives one canonical
// state. Dependency failures are represented as bounded component states;
// only context cancellation is returned as an error.
func Evaluate(ctx context.Context, input Inputs) (Report, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}

	runtimeName := input.Runtime
	var hookStatus hooks.StatusReport
	var hookErr error
	if input.Hooks == nil {
		hookErr = errHooksUnavailable
	} else {
		hookStatus, hookErr = input.Hooks.Status(ctx)
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
	}
	if runtimeName == domain.RuntimeUnknown {
		runtimeName = domainRuntime(hookStatus.Runtime)
	}
	if !runtimeName.Valid() {
		runtimeName = domain.RuntimeUnknown
	}

	snapshot := evaluationSnapshot{
		runtime:     runtimeName,
		hooks:       hookStatus,
		hookErr:     hookErr,
		delivery:    capture.DeliveryHealth{Runtime: runtimeName},
		deliveryErr: errStoreUnavailable,
	}
	if input.StoreOpenErr == nil && input.Store != nil {
		snapshot.dbAvailable = true
		snapshot.schema, snapshot.schemaErr = input.Store.SchemaStatus(ctx)
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		snapshot.selfTestErr = input.Store.SelfTestCaptureIngest(ctx)
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		snapshot.delivery, snapshot.deliveryErr = input.Store.GetCaptureHealth(ctx, runtimeName)
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
	} else if input.StoreOpenErr == nil {
		snapshot.schemaErr = errStoreUnavailable
		snapshot.selfTestErr = errStoreUnavailable
	}

	return evaluateSnapshot(snapshot), nil
}

type evaluationSnapshot struct {
	runtime domain.Runtime
	hooks   hooks.StatusReport
	hookErr error

	dbAvailable bool
	schema      store.SchemaStatus
	schemaErr   error
	selfTestErr error
	delivery    capture.DeliveryHealth
	deliveryErr error
}

var (
	errHooksUnavailable = errors.New("hook status is unavailable")
	errStoreUnavailable = errors.New("store is unavailable")
)

func evaluateSnapshot(snapshot evaluationSnapshot) Report {
	result := Report{
		Runtime:  snapshot.runtime,
		State:    Unknown,
		Delivery: sanitizeDelivery(snapshot.delivery, snapshot.runtime),
	}
	if !snapshot.runtime.Valid() {
		return result
	}
	result.Components = assessComponents(snapshot)
	if staticFailure(result.Components) {
		if onlyUnknownStaticFailure(result.Components) {
			result.State = Unknown
		} else {
			result.State = Broken
		}
		return result
	}
	switch result.Components.Delivery.State {
	case ComponentNone:
		result.State = Idle
	case ComponentFailed:
		result.State = Degraded
	case ComponentOK:
		result.State = Healthy
	default:
		result.State = Unknown
	}
	return result
}

func assessComponents(snapshot evaluationSnapshot) Components {
	var result Components
	statusRuntime := domainRuntime(snapshot.hooks.Runtime)
	if snapshot.hookErr != nil || (snapshot.runtime.Valid() && statusRuntime.Valid() && statusRuntime != snapshot.runtime) {
		result.Config = Check{State: ComponentUnknown}
		result.Hooks = Check{State: ComponentUnknown}
		result.ManagedEntries = Check{State: ComponentUnknown}
		result.Executable = Check{State: ComponentUnknown}
	} else {
		result.Config = assessConfig(snapshot.hooks)
		result.Hooks = assessHooks(snapshot.hooks)
		result.ManagedEntries = assessManagedEntries(snapshot.hooks)
		if snapshot.hooks.Binary.Resolved {
			result.Executable = Check{State: ComponentOK}
		} else {
			result.Executable = Check{State: ComponentUnresolved}
		}
	}

	if snapshot.dbAvailable {
		result.Database = Check{State: ComponentOK}
	} else {
		result.Database = Check{State: ComponentUnavailable}
	}
	switch {
	case !snapshot.dbAvailable:
		result.Schema = Check{State: ComponentUnavailable}
		result.SelfTest = Check{State: ComponentUnavailable}
	case snapshot.schemaErr != nil:
		result.Schema = Check{State: ComponentUnavailable}
		result.SelfTest = Check{State: ComponentUnknown}
	case snapshot.schema.Current <= 0 || snapshot.schema.Latest <= 0 || snapshot.schema.Current != snapshot.schema.Latest:
		result.Schema = Check{State: ComponentMismatch}
		result.SelfTest = assessSelfTest(snapshot.selfTestErr)
	default:
		result.Schema = Check{State: ComponentOK}
		result.SelfTest = assessSelfTest(snapshot.selfTestErr)
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
	if status.Code == hooks.StatusInstalled || status.ConfigExists {
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

// The manager's StatusReport is authoritative for the expected event set.
// Health checks only the aggregate status and every entry returned by the
// manager; it does not maintain a second Claude/Codex event vocabulary.
func assessManagedEntries(status hooks.StatusReport) Check {
	if status.Code != hooks.StatusInstalled || status.Managed != hooks.ManagedEntryInstalled || len(status.ManagedEntries) == 0 {
		return Check{State: ComponentIncomplete}
	}
	for _, entry := range status.ManagedEntries {
		if entry.State != hooks.ManagedEntryInstalled || entry.ExactHandlers != 1 || entry.Partial != 0 {
			return Check{State: ComponentIncomplete}
		}
	}
	return Check{State: ComponentOK}
}

func assessSelfTest(err error) Check {
	if err != nil {
		return Check{State: ComponentFailed}
	}
	return Check{State: ComponentOK}
}

func assessDelivery(snapshot evaluationSnapshot) Check {
	if snapshot.deliveryErr != nil {
		return Check{State: ComponentUnavailable}
	}
	health := snapshot.delivery
	if !health.Runtime.Valid() && snapshot.runtime.Valid() {
		health.Runtime = snapshot.runtime
	}
	if !health.Runtime.Valid() || health.Runtime != snapshot.runtime {
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

func staticFailure(components Components) bool {
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

func onlyUnknownStaticFailure(components Components) bool {
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
			return false
		}
	}
	return true
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
