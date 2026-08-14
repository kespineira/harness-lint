// Package history defines neutral, non-JSON DTOs for SQL-backed historical
// usage queries. It knows only runtime-neutral domain values and has no store
// or presentation dependencies.
package history

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
)

// Query describes a closed UTC activity interval. A non-zero Start includes
// events exactly at Start; a non-zero End includes events exactly at End. A
// zero endpoint is unbounded. Runtime, Type, and Name filters compose with the
// interval. Effective activity is source_timestamp when trustworthy and
// observed_at otherwise; observed_at itself is never replaced.
type Query struct {
	Start time.Time
	End   time.Time

	Runtime        domain.Runtime
	CapabilityType domain.CapabilityType
	CapabilityName string

	// Type and Name are concise aliases accepted for callers that prefer the
	// aggregate's field vocabulary. Store code gives the explicit fields
	// precedence when both forms are supplied.
	Type domain.CapabilityType
	Name string
}

// Filter is a vocabulary alias for Query.
type Filter = Query

func (q Query) ResolvedType() domain.CapabilityType {
	if q.CapabilityType != "" {
		return q.CapabilityType
	}
	return q.Type
}

func (q Query) ResolvedName() string {
	if q.CapabilityName != "" {
		return q.CapabilityName
	}
	return q.Name
}

func (q Query) Validate() error {
	if !q.Start.IsZero() && !q.End.IsZero() && q.End.Before(q.Start) {
		return errors.New("history query end precedes start")
	}
	if q.Runtime != "" && !q.Runtime.Valid() {
		return fmt.Errorf("invalid history runtime %q", q.Runtime)
	}
	typ := q.ResolvedType()
	if typ != "" && !typ.Valid() {
		return fmt.Errorf("invalid history capability type %q", typ)
	}
	return nil
}

// Aggregate is one deterministic runtime/type/name history bucket. Usage
// rows do not carry scope, so InstalledScopes is populated only from current
// inventory and never projected onto an observed event. A bucket can therefore
// be installed with zero uses, or usage-only with Installed false.
type Aggregate struct {
	Runtime        domain.Runtime
	CapabilityType domain.CapabilityType
	CapabilityName string

	// Uses is the count of canonical invocation rows. InvocationUses is a
	// descriptive alias carrying the same value.
	Uses            int64
	InvocationUses  int64
	InvocationCount int64

	DistinctInvocationSessions int64
	DistinctSessionCount       int64
	FirstObservedAt            *time.Time
	LastObservedAt             *time.Time
	FirstEffectiveActivityAt   *time.Time
	LastEffectiveActivityAt    *time.Time
	FirstObserved              *time.Time
	LastObserved               *time.Time
	FirstActivity              *time.Time
	LastActivity               *time.Time

	// InvocationEvidence contains one count per provenance relation. A stable
	// invocation present in both hook and transcript evidence contributes one to
	// each source while Uses remains one.
	InvocationEvidence   map[domain.Provenance]int64
	EvidenceByProvenance map[domain.Provenance]int64

	// ObservedAdvertisedSessions is nil when no explicit advertised-event
	// evidence exists; it is non-nil (including zero) only when such evidence
	// exists in the selected interval.
	ObservedAdvertisedSessions *int64
	AdvertisedSessions         *int64

	Installed       bool
	InstalledScopes []domain.Scope
	Current         bool
	Scopes          []domain.Scope
}

// UsageAggregate and InvocationAggregate are descriptive aliases.
type UsageAggregate = Aggregate
type InvocationAggregate = Aggregate

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

// MonthlyQuery is the same closed-interval filter as Query, with the result
// grouped by UTC calendar month. Runtime and capability-type filters compose.
type MonthlyQuery = Query

// MonthlyAggregate is one UTC calendar-month invocation subtotal. Month is
// normalized to the first instant of its month in UTC.
type MonthlyAggregate struct {
	Month                      time.Time
	Runtime                    domain.Runtime
	CapabilityType             domain.CapabilityType
	Uses                       int64
	InvocationUses             int64
	DistinctInvocationSessions int64
	DistinctSessions           int64
}

// InvocationEvidenceCount returns a provenance subtotal and treats a missing
// key as zero.
func (a Aggregate) InvocationEvidenceCount(provenance domain.Provenance) int64 {
	return a.InvocationEvidence[provenance]
}

// Normalize makes a result deterministic for callers even when it was built
// from a map or an unsorted scope slice.
func (a Aggregate) Normalize() Aggregate {
	if a.InvocationEvidence == nil {
		a.InvocationEvidence = make(map[domain.Provenance]int64)
	}
	if a.Uses == 0 {
		a.Uses = a.InvocationUses
	}
	if a.Uses == 0 {
		a.Uses = a.InvocationCount
	}
	a.InvocationUses = a.Uses
	a.InvocationCount = a.Uses
	if a.DistinctInvocationSessions == 0 {
		a.DistinctInvocationSessions = a.DistinctSessionCount
	}
	a.DistinctSessionCount = a.DistinctInvocationSessions
	if a.FirstObservedAt == nil {
		a.FirstObservedAt = a.FirstObserved
	}
	if a.LastObservedAt == nil {
		a.LastObservedAt = a.LastObserved
	}
	if a.FirstEffectiveActivityAt == nil {
		a.FirstEffectiveActivityAt = a.FirstActivity
	}
	if a.LastEffectiveActivityAt == nil {
		a.LastEffectiveActivityAt = a.LastActivity
	}
	a.FirstObserved = a.FirstObservedAt
	a.LastObserved = a.LastObservedAt
	a.FirstActivity = a.FirstEffectiveActivityAt
	a.LastActivity = a.LastEffectiveActivityAt
	if a.InvocationEvidence == nil {
		a.InvocationEvidence = a.EvidenceByProvenance
	}
	if a.InvocationEvidence == nil {
		a.InvocationEvidence = make(map[domain.Provenance]int64)
	}
	a.EvidenceByProvenance = a.InvocationEvidence
	if a.ObservedAdvertisedSessions == nil {
		a.ObservedAdvertisedSessions = a.AdvertisedSessions
	}
	a.AdvertisedSessions = a.ObservedAdvertisedSessions
	if !a.Installed {
		a.Installed = a.Current
	}
	a.Current = a.Installed
	if len(a.InstalledScopes) == 0 {
		a.InstalledScopes = a.Scopes
	}
	a.Scopes = a.InstalledScopes
	if len(a.InstalledScopes) > 1 {
		sort.Slice(a.InstalledScopes, func(i, j int) bool { return a.InstalledScopes[i] < a.InstalledScopes[j] })
	}
	return a
}
