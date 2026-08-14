package history

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
)

// Interval is a UTC half-open interval, [Start, End). A zero End represents
// an open interval. Open intervals are useful for a currently healthy,
// confirmed observation epoch; they are not evidence of complete coverage.
//
// Start is always required. End, when present, must be strictly after Start.
// Values returned by the interval helpers are normalized to UTC.
type Interval struct {
	Start time.Time
	End   time.Time
}

// IsOpen reports whether the interval has no explicit end yet.
func (i Interval) IsOpen() bool { return i.End.IsZero() }

// Normalize converts the interval's timestamps to UTC and validates its
// half-open boundaries. It does not fill an open End.
func (i Interval) Normalize() (Interval, error) {
	if i.Start.IsZero() {
		return Interval{}, errors.New("interval start is required")
	}
	i.Start = i.Start.UTC()
	if !i.End.IsZero() {
		i.End = i.End.UTC()
		if !i.End.After(i.Start) {
			return Interval{}, errors.New("closed interval end must be after start")
		}
	}
	return i, nil
}

// Validate checks the interval without changing it. Call Normalize when a
// canonical UTC value is needed.
func (i Interval) Validate() error {
	_, err := i.Normalize()
	return err
}

// CoverageKey identifies a capability without carrying source paths, raw
// identities, or any other runtime content. Scope and source remain separate
// inventory concerns; this key is the matching identity shared by runtime
// capture and capability-presence evidence.
type CoverageKey struct {
	Runtime        domain.Runtime
	CapabilityType domain.CapabilityType
	CapabilityName string
}

func (k CoverageKey) Validate() error {
	if !k.Runtime.Valid() {
		return fmt.Errorf("invalid coverage runtime %q", k.Runtime)
	}
	if !k.CapabilityType.Valid() {
		return fmt.Errorf("invalid coverage capability type %q", k.CapabilityType)
	}
	if strings.TrimSpace(k.CapabilityName) == "" {
		return errors.New("coverage capability name is required")
	}
	return nil
}

// CaptureStartReason identifies the evidence that opened a reliable capture
// epoch. The contract is intentionally extensible, but currently only a
// confirmed direct delivery proves that capture began.
type CaptureStartReason string

const (
	CaptureStartReasonConfirmedDirectDelivery CaptureStartReason = "confirmed_direct_delivery"
)

func (r CaptureStartReason) Valid() bool {
	return r == CaptureStartReasonConfirmedDirectDelivery
}

// CaptureEndReason identifies the lifecycle evidence that closed a reliable
// capture epoch. Only confirmed capture failure and managed hook uninstall are
// currently allow-listed; installation and configuration are not lifecycle
// reasons for a reliable capture epoch.
type CaptureEndReason string

const (
	CaptureEndReasonConfirmedCaptureFailure CaptureEndReason = "confirmed_capture_failure"
	CaptureEndReasonManagedHookUninstall    CaptureEndReason = "managed_hook_uninstall"
)

func (r CaptureEndReason) Valid() bool {
	return r == CaptureEndReasonConfirmedCaptureFailure || r == CaptureEndReasonManagedHookUninstall
}

// CaptureEpoch is a confirmed direct-capture interval for one runtime. An
// epoch begins only after a real hook delivery is confirmed. Installation,
// configuration, or a merely advertised hook never creates this value.
// Explicit capture failure or managed uninstall closes the epoch; a later
// successful delivery is represented by a new epoch.
type CaptureEpoch struct {
	Runtime domain.Runtime
	Interval
	StartReason CaptureStartReason
	EndReason   CaptureEndReason
}

func (e CaptureEpoch) Validate() error {
	if !e.Runtime.Valid() {
		return fmt.Errorf("invalid capture epoch runtime %q", e.Runtime)
	}
	if !e.StartReason.Valid() {
		if e.StartReason == "" {
			return errors.New("capture epoch start reason is required")
		}
		return fmt.Errorf("invalid capture epoch start reason %q", e.StartReason)
	}
	if err := e.Interval.Validate(); err != nil {
		return fmt.Errorf("invalid capture epoch interval: %w", err)
	}
	if e.Interval.IsOpen() {
		if e.EndReason != "" {
			return fmt.Errorf("open capture epoch cannot have end reason %q", e.EndReason)
		}
		return nil
	}
	if !e.EndReason.Valid() {
		if e.EndReason == "" {
			return errors.New("closed capture epoch end reason is required")
		}
		return fmt.Errorf("invalid capture epoch end reason %q", e.EndReason)
	}
	return nil
}

// CapabilityPresenceEpoch is an interval in which a capability was present
// according to a successful inventory transition. Separate epochs preserve
// disappearance/reappearance gaps; first_seen and last_seen bounds must not
// be interpreted as one continuous presence interval.
type CapabilityPresenceEpoch struct {
	CoverageKey
	Interval
}

func (e CapabilityPresenceEpoch) Validate() error {
	if err := e.CoverageKey.Validate(); err != nil {
		return fmt.Errorf("invalid capability presence key: %w", err)
	}
	if err := e.Interval.Validate(); err != nil {
		return fmt.Errorf("invalid capability presence interval: %w", err)
	}
	return nil
}

// CoverageStatus is deliberately small. Complete is reserved because this
// daemonless contract has no heartbeat or other proof of uninterrupted
// observation; ComputeEffectiveCoverage therefore emits only unknown or
// partial.
type CoverageStatus string

const (
	CoverageUnknown  CoverageStatus = "unknown"
	CoveragePartial  CoverageStatus = "partial"
	CoverageComplete CoverageStatus = "complete"
)

func (s CoverageStatus) Valid() bool {
	return s == CoverageUnknown || s == CoveragePartial || s == CoverageComplete
}

// EffectiveCoverage is the modeled union-normalized intersection of confirmed
// runtime capture and matching capability-presence epochs. Intervals are
// clipped by ComputeEffectiveCoverage when a query/as-of interval is supplied.
// Positive modeled coverage is partial, including when an open epoch is
// clipped to an explicit as-of; complete is never emitted by this contract.
type EffectiveCoverage struct {
	Key       CoverageKey
	Intervals []Interval
	Status    CoverageStatus
}

// Validate checks the status and canonical interval representation. A
// positive interval set can only have partial status, while an empty set has
// unknown status. Complete is reserved and rejected as an emitted result.
func (c EffectiveCoverage) Validate() error {
	if err := c.Key.Validate(); err != nil {
		return err
	}
	if !c.Status.Valid() {
		return fmt.Errorf("invalid coverage status %q", c.Status)
	}
	if c.Status == CoverageComplete {
		return errors.New("complete coverage status is reserved")
	}
	normalized, err := NormalizeIntervals(c.Intervals)
	if err != nil {
		return err
	}
	if len(normalized) != len(c.Intervals) {
		return errors.New("coverage intervals are not union-normalized")
	}
	for index := range normalized {
		if !sameInterval(normalized[index], c.Intervals[index]) {
			return errors.New("coverage intervals are not canonical UTC intervals")
		}
	}
	want := CoverageUnknown
	if len(c.Intervals) > 0 {
		want = CoveragePartial
	}
	if c.Status != want {
		return fmt.Errorf("coverage status %q does not match interval evidence %q", c.Status, want)
	}
	return nil
}

// NormalizeIntervals validates, sorts, and unions intervals. Overlapping or
// adjacent intervals are merged. An open interval absorbs every subsequent
// interval in the same union. Empty input returns an empty, non-nil slice.
func NormalizeIntervals(input []Interval) ([]Interval, error) {
	if len(input) == 0 {
		return []Interval{}, nil
	}
	normalized := make([]Interval, len(input))
	for index, interval := range input {
		canonical, err := interval.Normalize()
		if err != nil {
			return nil, fmt.Errorf("interval %d: %w", index, err)
		}
		normalized[index] = canonical
	}
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].Start.Equal(normalized[j].Start) {
			return compareEnds(normalized[i].End, normalized[j].End) < 0
		}
		return normalized[i].Start.Before(normalized[j].Start)
	})

	result := make([]Interval, 0, len(normalized))
	for _, next := range normalized {
		if len(result) == 0 {
			result = append(result, next)
			continue
		}
		last := &result[len(result)-1]
		if last.IsOpen() || !next.Start.After(last.End) {
			if last.IsOpen() || (next.IsOpen() && !last.IsOpen()) {
				last.End = time.Time{}
			} else if next.End.After(last.End) {
				last.End = next.End
			}
			continue
		}
		result = append(result, next)
	}
	return result, nil
}

// IntersectIntervals returns the positive intersections of two interval sets.
// Both inputs are validated and union-normalized first, and the result is
// union-normalized as well. Touching endpoints have no intersection under
// half-open semantics.
func IntersectIntervals(left, right []Interval) ([]Interval, error) {
	left, err := NormalizeIntervals(left)
	if err != nil {
		return nil, fmt.Errorf("normalize left intervals: %w", err)
	}
	right, err = NormalizeIntervals(right)
	if err != nil {
		return nil, fmt.Errorf("normalize right intervals: %w", err)
	}
	result := make([]Interval, 0)
	for leftIndex, rightIndex := 0, 0; leftIndex < len(left) && rightIndex < len(right); {
		intersection := intersectInterval(left[leftIndex], right[rightIndex])
		if intersection != nil {
			result = append(result, *intersection)
		}
		if endsBefore(left[leftIndex].End, right[rightIndex].End) {
			leftIndex++
		} else if endsBefore(right[rightIndex].End, left[leftIndex].End) {
			rightIndex++
		} else {
			// Equal finite ends or two open ends cannot overlap another
			// interval after this pair on either side.
			leftIndex++
			rightIndex++
		}
	}
	return NormalizeIntervals(result)
}

// ClipIntervals intersects intervals with one query/as-of interval.
func ClipIntervals(intervals []Interval, clip Interval) ([]Interval, error) {
	if err := clip.Validate(); err != nil {
		return nil, fmt.Errorf("invalid clip interval: %w", err)
	}
	return IntersectIntervals(intervals, []Interval{clip})
}

// ClipIntervalsAsOf clips modeled intervals at an explicit UTC as-of time.
// The as-of is a calculation boundary only; callers still receive partial
// status from ComputeEffectiveCoverage.
func ClipIntervalsAsOf(intervals []Interval, asOf time.Time) ([]Interval, error) {
	if asOf.IsZero() {
		return nil, errors.New("as-of time is required")
	}
	intervals, err := NormalizeIntervals(intervals)
	if err != nil {
		return nil, err
	}
	asOf = asOf.UTC()
	result := make([]Interval, 0, len(intervals))
	for _, interval := range intervals {
		if !interval.Start.Before(asOf) {
			continue
		}
		if interval.IsOpen() || asOf.Before(interval.End) {
			interval.End = asOf
		}
		if interval.End.After(interval.Start) {
			result = append(result, interval)
		}
	}
	return NormalizeIntervals(result)
}

// ComputeEffectiveCoverage computes coverage for one capability key. Capture
// epochs are unioned by runtime, matching presence epochs are unioned by key,
// and their intersections are optionally clipped by a closed or open query
// interval. Existing history.Query event boundaries remain closed; these
// modeled duration intervals are half-open and this clipping does not change
// user-facing event filtering. No intersection produces unknown; every
// positive result is partial, including a result derived from an open epoch or
// as-of clipping.
func ComputeEffectiveCoverage(key CoverageKey, captureEpochs []CaptureEpoch, presenceEpochs []CapabilityPresenceEpoch, clip *Interval) (EffectiveCoverage, error) {
	if err := key.Validate(); err != nil {
		return EffectiveCoverage{}, err
	}
	if clip != nil {
		canonical, err := clip.Normalize()
		if err != nil {
			return EffectiveCoverage{}, fmt.Errorf("invalid coverage clip: %w", err)
		}
		clip = &canonical
	}
	capture := make([]Interval, 0, len(captureEpochs))
	for index, epoch := range captureEpochs {
		if err := epoch.Validate(); err != nil {
			return EffectiveCoverage{}, fmt.Errorf("capture epoch %d: %w", index, err)
		}
		if epoch.Runtime == key.Runtime {
			canonical, _ := epoch.Interval.Normalize()
			capture = append(capture, canonical)
		}
	}
	presence := make([]Interval, 0, len(presenceEpochs))
	for index, epoch := range presenceEpochs {
		if err := epoch.Validate(); err != nil {
			return EffectiveCoverage{}, fmt.Errorf("presence epoch %d: %w", index, err)
		}
		if epoch.CoverageKey == key {
			canonical, _ := epoch.Interval.Normalize()
			presence = append(presence, canonical)
		}
	}
	intervals, err := IntersectIntervals(capture, presence)
	if err != nil {
		return EffectiveCoverage{}, err
	}
	if clip != nil {
		intervals, err = ClipIntervals(intervals, *clip)
		if err != nil {
			return EffectiveCoverage{}, err
		}
	}
	status := CoverageUnknown
	if len(intervals) > 0 {
		status = CoveragePartial
	}
	result := EffectiveCoverage{Key: key, Intervals: intervals, Status: status}
	if err := result.Validate(); err != nil {
		return EffectiveCoverage{}, err
	}
	return result, nil
}

func intersectInterval(left, right Interval) *Interval {
	start := left.Start
	if right.Start.After(start) {
		start = right.Start
	}
	end := earlierEnd(left.End, right.End)
	if !end.IsZero() && !end.After(start) {
		return nil
	}
	return &Interval{Start: start.UTC(), End: end}
}

func earlierEnd(left, right time.Time) time.Time {
	if left.IsZero() {
		return right
	}
	if right.IsZero() || left.Before(right) {
		return left
	}
	return right
}

func endsBefore(left, right time.Time) bool {
	if left.IsZero() {
		return false
	}
	return right.IsZero() || left.Before(right)
}

func compareEnds(left, right time.Time) int {
	if left.IsZero() {
		if right.IsZero() {
			return 0
		}
		return 1
	}
	if right.IsZero() {
		return -1
	}
	if left.Before(right) {
		return -1
	}
	if left.After(right) {
		return 1
	}
	return 0
}

func sameInterval(left, right Interval) bool {
	return left.Start.Equal(right.Start) && left.End.Equal(right.End)
}
