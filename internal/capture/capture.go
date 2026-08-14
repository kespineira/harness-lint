// Package capture contains privacy-safe diagnostics for direct usage capture.
//
// The package deliberately models only coarse, bounded state. It has no error
// text, payload, prompt, response, command, path, or other runtime content.
package capture

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
)

// MaxConsecutiveFailures is the hard upper bound for persisted capture
// failure state. Keeping this bounded prevents a long outage from turning a
// diagnostic row into an unbounded counter. A successful delivery resets the
// counter while retaining the last failure timestamp and kind.
const MaxConsecutiveFailures = 32

// MaxRecentFailures is a descriptive alias for callers that use the wording
// from the capture-health contract.
const MaxRecentFailures = MaxConsecutiveFailures

// FailureKind is a coarse metadata-only category. Raw errors are intentionally
// not accepted by the persistence API and must never be represented here.
type FailureKind string

// CaptureFailureKind is a descriptive alias for FailureKind.
type CaptureFailureKind = FailureKind

const (
	FailureMalformedPayload    FailureKind = "malformed_payload"
	FailureUnsupportedEvent    FailureKind = "unsupported_event"
	FailureDatabaseBusy        FailureKind = "database_busy"
	FailureDatabaseUnavailable FailureKind = "database_unavailable"
	FailureSchemaError         FailureKind = "schema_error"
	FailureInternalError       FailureKind = "internal_error"

	// Short aliases keep call sites readable while retaining one canonical set
	// of persisted values.
	FailureKindMalformedPayload    = FailureMalformedPayload
	FailureKindUnsupportedEvent    = FailureUnsupportedEvent
	FailureKindDatabaseBusy        = FailureDatabaseBusy
	FailureKindDatabaseUnavailable = FailureDatabaseUnavailable
	FailureKindSchemaError         = FailureSchemaError
	FailureKindInternalError       = FailureInternalError
	MalformedPayload               = FailureMalformedPayload
	UnsupportedEvent               = FailureUnsupportedEvent
	DatabaseBusy                   = FailureDatabaseBusy
	DatabaseUnavailable            = FailureDatabaseUnavailable
	SchemaError                    = FailureSchemaError
	InternalError                  = FailureInternalError
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
	// At is a concise alias for FailedAt. FailedAt takes precedence when both
	// are supplied.
	At   time.Time
	Kind FailureKind
}

// Failure is a concise alias for CaptureFailure.
type Failure = CaptureFailure

func (f CaptureFailure) Validate() error {
	if !f.Runtime.Valid() {
		return fmt.Errorf("invalid capture runtime %q", f.Runtime)
	}
	if f.failureTime().IsZero() {
		return errors.New("capture failure time is required")
	}
	if !f.Kind.Valid() {
		return errors.New("invalid capture failure kind")
	}
	return nil
}

func (f CaptureFailure) failureTime() time.Time {
	if !f.FailedAt.IsZero() {
		return f.FailedAt
	}
	return f.At
}

// DeliveryHealth is the aggregate direct-capture delivery state for one
// runtime. Nil timestamps and LastFailureKind mean that no corresponding
// observation has been recorded. ConsecutiveFailures and RecentFailureCount
// contain the same bounded value; the latter is provided as a vocabulary
// alias for consumers of the capture-health contract.
type DeliveryHealth struct {
	Runtime                domain.Runtime
	LastSuccessfulDelivery *time.Time
	LastFailedDelivery     *time.Time
	ConsecutiveFailures    int
	RecentFailureCount     int
	LastFailureKind        *FailureKind
}

// Health is a concise alias for DeliveryHealth.
type Health = DeliveryHealth

// CaptureHealth is a fully-qualified alias for DeliveryHealth.
type CaptureHealth = DeliveryHealth

func (h DeliveryHealth) Validate() error {
	if !h.Runtime.Valid() {
		return fmt.Errorf("invalid capture health runtime %q", h.Runtime)
	}
	if h.ConsecutiveFailures < 0 || h.ConsecutiveFailures > MaxConsecutiveFailures {
		return fmt.Errorf("capture failure count %d is outside [0,%d]", h.ConsecutiveFailures, MaxConsecutiveFailures)
	}
	if h.RecentFailureCount < 0 || h.RecentFailureCount > MaxConsecutiveFailures {
		return fmt.Errorf("capture recent failure count %d is outside [0,%d]", h.RecentFailureCount, MaxConsecutiveFailures)
	}
	if h.ConsecutiveFailures != 0 && h.RecentFailureCount != 0 && h.ConsecutiveFailures != h.RecentFailureCount {
		return errors.New("capture failure count aliases disagree")
	}
	if h.LastFailureKind != nil && !h.LastFailureKind.Valid() {
		return errors.New("invalid last capture failure kind")
	}
	return nil
}

// Normalize returns a UTC copy with both count aliases synchronized. It is a
// defensive boundary for values returned by storage and is not a source of
// additional diagnostic information.
func (h DeliveryHealth) Normalize() (DeliveryHealth, error) {
	if !h.Runtime.Valid() {
		return DeliveryHealth{}, fmt.Errorf("invalid capture health runtime %q", h.Runtime)
	}
	count := h.ConsecutiveFailures
	if count == 0 {
		count = h.RecentFailureCount
	}
	if count < 0 || count > MaxConsecutiveFailures {
		return DeliveryHealth{}, fmt.Errorf("capture failure count %d is outside [0,%d]", count, MaxConsecutiveFailures)
	}
	h.ConsecutiveFailures = count
	h.RecentFailureCount = count
	if h.LastSuccessfulDelivery != nil {
		value := h.LastSuccessfulDelivery.UTC()
		h.LastSuccessfulDelivery = &value
	}
	if h.LastFailedDelivery != nil {
		value := h.LastFailedDelivery.UTC()
		h.LastFailedDelivery = &value
	}
	if h.LastFailureKind != nil {
		value := *h.LastFailureKind
		h.LastFailureKind = &value
	}
	if err := h.Validate(); err != nil {
		return DeliveryHealth{}, err
	}
	return h, nil
}

// FailureKindText returns the storage-safe category as text. It exists to
// make it harder for callers to accidentally serialize a non-validated value.
func (k FailureKind) FailureKindText() (string, error) {
	if !k.Valid() {
		return "", errors.New("invalid capture failure kind")
	}
	return strings.TrimSpace(string(k)), nil
}
