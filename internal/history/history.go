// Package history defines neutral, non-JSON DTOs for SQL-backed historical
// usage queries. It knows only runtime-neutral domain values and has no store
// or presentation dependencies.
package history

import (
	"errors"
	"fmt"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
)

// Query describes a closed UTC activity interval. A non-zero Start includes
// events exactly at Start; a non-zero End includes events exactly at End. A
// zero endpoint is unbounded. Runtime, CapabilityType, and CapabilityName
// filters compose with the interval. Effective activity is source_timestamp
// when trustworthy and observed_at otherwise; observed_at itself is never
// replaced.
type Query struct {
	Start time.Time
	End   time.Time

	Runtime        domain.Runtime
	CapabilityType domain.CapabilityType
	CapabilityName string
}

func (q Query) Validate() error {
	if !q.Start.IsZero() && !q.End.IsZero() && q.End.Before(q.Start) {
		return errors.New("history query end precedes start")
	}
	if q.Runtime != "" && !q.Runtime.Valid() {
		return fmt.Errorf("invalid history runtime %q", q.Runtime)
	}
	if q.CapabilityType != "" && !q.CapabilityType.Valid() {
		return fmt.Errorf("invalid history capability type %q", q.CapabilityType)
	}
	return nil
}

// CoverageQuery describes a half-open UTC coverage interval, [Start, End).
// A zero endpoint is unbounded; equal non-zero endpoints are a valid empty
// window and produce unknown coverage. Coverage identity is canonical
// runtime/type/name only; presence definitions from every scope and source are
// unioned before intersecting capture epochs because usage events cannot
// identify either of those definition dimensions.
type CoverageQuery struct {
	Start time.Time
	End   time.Time

	Runtime        domain.Runtime
	CapabilityType domain.CapabilityType
	CapabilityName string
}

func (q CoverageQuery) Validate() error {
	if !q.Start.IsZero() && !q.End.IsZero() && q.End.Before(q.Start) {
		return errors.New("coverage query end precedes start")
	}
	if q.Runtime != "" && !q.Runtime.Valid() {
		return fmt.Errorf("invalid coverage runtime %q", q.Runtime)
	}
	if q.CapabilityType != "" && !q.CapabilityType.Valid() {
		return fmt.Errorf("invalid coverage capability type %q", q.CapabilityType)
	}
	return nil
}

// Coverage contains nullable observation windows over all recorded history.
// It is evidence of what this store has observed, not a continuity or
// lifetime-completeness claim. Each pair remains nil when the schema has no
// corresponding evidence.
type Coverage struct {
	FirstInventoryObservedAt  *time.Time
	LastInventoryObservedAt   *time.Time
	FirstUsageObservedAt      *time.Time
	LastUsageObservedAt       *time.Time
	FirstDirectHookObservedAt *time.Time
	LastDirectHookObservedAt  *time.Time
}

// Aggregate is one deterministic runtime/type/name history bucket. Usage
// rows do not carry scope, so InstalledScopes is populated only from current
// inventory and never projected onto an observed event. A bucket can therefore
// be installed with zero uses, or usage-only with Installed false.
type Aggregate struct {
	Runtime        domain.Runtime
	CapabilityType domain.CapabilityType
	CapabilityName string

	Uses                       int64
	DistinctInvocationSessions int64
	// These timestamps are invocation-only and remain nil for zero-use buckets;
	// advertised and loaded events are represented independently.
	FirstObservedAt          *time.Time
	LastObservedAt           *time.Time
	FirstEffectiveActivityAt *time.Time
	LastEffectiveActivityAt  *time.Time

	// InvocationEvidence contains one count per provenance relation. A stable
	// invocation present in both hook and transcript evidence contributes one to
	// each source while Uses remains one.
	InvocationEvidence map[domain.Provenance]int64

	// AdvertisedObservations and LoadedObservations are canonical non-negative
	// event counts from usage_events in the selected interval. They are
	// independent of invocation totals and current inventory state.
	AdvertisedObservations int64
	LoadedObservations     int64
	// ObservedAdvertisedSessions is nil when no explicit advertised-event
	// evidence exists; it is non-nil only when such evidence exists in the
	// selected interval.
	ObservedAdvertisedSessions *int64

	Installed       bool
	InstalledScopes []domain.Scope
	// Coverage is the released legacy observation-only evidence range. It is
	// not a continuity claim and must not be used as effective coverage.
	Coverage *Coverage
	// EffectiveCoverage is the modeled intersection of confirmed capture and
	// matching capability-presence epochs. It is always partial or unknown;
	// a nil value is reserved for callers that did not request this query.
	EffectiveCoverage *EffectiveCoverage
}

// EventEvidence is one normalized evidence relation for a canonical usage
// event. It intentionally contains only metadata and normalized identifiers.
type EventEvidence struct {
	Fingerprint      string
	Provenance       domain.Provenance
	ObservedAt       time.Time
	SourceTimestamp  *time.Time
	InvocationOrigin domain.InvocationOrigin
	SourceIdentity   string
}

// MonthlyAggregate is one UTC calendar-month invocation subtotal. Month is
// normalized to the first instant of its month in UTC.
type MonthlyAggregate struct {
	Month                      time.Time
	Runtime                    domain.Runtime
	CapabilityType             domain.CapabilityType
	CapabilityName             string
	Uses                       int64
	DistinctInvocationSessions int64
}

// InvocationEvidenceCount returns a provenance subtotal and treats a missing
// key as zero.
func (a Aggregate) InvocationEvidenceCount(provenance domain.Provenance) int64 {
	return a.InvocationEvidence[provenance]
}
