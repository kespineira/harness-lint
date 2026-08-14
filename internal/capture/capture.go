// Package capture contains privacy-safe diagnostics for direct usage capture.
//
// The package deliberately models only coarse, bounded state. It has no error
// text, payload, prompt, response, command, path, or other runtime content.
package capture

import (
	"errors"
	"fmt"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
)

// MaxConsecutiveFailures is the hard upper bound for persisted capture
// failure state. A successful delivery resets the counter while retaining the
// last failure timestamp and kind.
const MaxConsecutiveFailures = 32

// FailureKind is the documented coarse metadata-only failure vocabulary. Raw
// errors are intentionally not accepted by the persistence API.
type FailureKind string

const (
	FailureMalformedPayload    FailureKind = "malformed_payload"
	FailureUnsupportedEvent    FailureKind = "unsupported_event"
	FailureDatabaseBusy        FailureKind = "database_busy"
	FailureDatabaseUnavailable FailureKind = "database_unavailable"
	FailureSchemaError         FailureKind = "schema_error"
	FailureInternalError       FailureKind = "internal_error"
)

func (k FailureKind) Valid() bool {
	switch k {
	case FailureMalformedPayload, FailureUnsupportedEvent, FailureDatabaseBusy,
		FailureDatabaseUnavailable, FailureSchemaError, FailureInternalError:
		return true
	default:
		return false
	}
}

// CaptureFailure is the only input accepted for a failed delivery. It
// contains runtime, time, and a bounded category; it cannot carry raw error
// text or event data.
type CaptureFailure struct {
	Runtime  domain.Runtime
	FailedAt time.Time
	Kind     FailureKind
}

func (f CaptureFailure) Validate() error {
	if !f.Runtime.Valid() {
		return fmt.Errorf("invalid capture runtime %q", f.Runtime)
	}
	if f.FailedAt.IsZero() {
		return errors.New("capture failure time is required")
	}
	if !f.Kind.Valid() {
		return errors.New("invalid capture failure kind")
	}
	return nil
}

// DeliveryHealth is aggregate direct-capture delivery state for one runtime.
// Nil timestamps and LastFailureKind mean that no corresponding observation
// has been recorded. ConsecutiveFailures is bounded by MaxConsecutiveFailures.
type DeliveryHealth struct {
	Runtime                domain.Runtime
	LastSuccessfulDelivery *time.Time
	LastFailedDelivery     *time.Time
	ConsecutiveFailures    int
	LastFailureKind        *FailureKind
}

func (h DeliveryHealth) Validate() error {
	if !h.Runtime.Valid() {
		return fmt.Errorf("invalid capture health runtime %q", h.Runtime)
	}
	if h.ConsecutiveFailures < 0 || h.ConsecutiveFailures > MaxConsecutiveFailures {
		return fmt.Errorf("capture failure count %d is outside [0,%d]", h.ConsecutiveFailures, MaxConsecutiveFailures)
	}
	if h.LastFailureKind != nil && !h.LastFailureKind.Valid() {
		return errors.New("invalid last capture failure kind")
	}
	return nil
}

// FailureKindText returns the validated storage-safe category as text.
func (k FailureKind) FailureKindText() (string, error) {
	if !k.Valid() {
		return "", errors.New("invalid capture failure kind")
	}
	return string(k), nil
}

// ProvesMissedDirectDelivery reports whether this failure is evidence that a
// managed direct hook delivery was not captured. Unsupported events are
// intentionally excluded: seeing an event outside the managed capture
// contract does not establish that a relevant delivery was missed.
func (k FailureKind) ProvesMissedDirectDelivery() bool {
	switch k {
	case FailureMalformedPayload, FailureDatabaseBusy, FailureDatabaseUnavailable,
		FailureSchemaError, FailureInternalError:
		return true
	default:
		return false
	}
}
