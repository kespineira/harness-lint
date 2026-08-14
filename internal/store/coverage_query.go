package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kespineira/harness-lint/internal/history"
)

// QueryEffectiveCoverage computes modeled coverage from the sparse epoch
// tables. Canonical coverage keys come only from capability-presence epochs;
// usage events and current inventory are not coverage identity evidence.
// Presence epochs for different scopes/sources intentionally collapse to the
// canonical history key.
func (s *Store) QueryEffectiveCoverage(ctx context.Context, query history.CoverageQuery) ([]history.EffectiveCoverage, error) {
	if s.isClosed() {
		return nil, errors.New("store is closed")
	}
	if err := query.Validate(); err != nil {
		return nil, err
	}
	keys, err := s.coverageKeys(ctx, query)
	if err != nil {
		return nil, err
	}
	capture, presence, err := s.readCoverageEpochs(ctx, query)
	if err != nil {
		return nil, err
	}
	result := make([]history.EffectiveCoverage, 0, len(keys))
	for _, key := range keys {
		coverage, err := computeQueryCoverage(key, capture, presence, query)
		if err != nil {
			return nil, fmt.Errorf("compute effective coverage for %s/%s/%s: %w", key.Runtime, key.CapabilityType, key.CapabilityName, err)
		}
		result = append(result, coverage)
	}
	return result, nil
}

// QueryCapabilityCoverage computes one canonical capability bucket. It is
// useful when the caller needs unknown (rather than an empty result) for a
// key with no presence epoch.
func (s *Store) QueryCapabilityCoverage(ctx context.Context, key history.CoverageKey, query history.CoverageQuery) (history.EffectiveCoverage, error) {
	if s.isClosed() {
		return history.EffectiveCoverage{}, errors.New("store is closed")
	}
	if err := query.Validate(); err != nil {
		return history.EffectiveCoverage{}, err
	}
	if err := key.Validate(); err != nil {
		return history.EffectiveCoverage{}, err
	}
	if query.Runtime != "" && query.Runtime != key.Runtime {
		return history.EffectiveCoverage{}, fmt.Errorf("coverage query runtime %q conflicts with capability key %q", query.Runtime, key.Runtime)
	}
	if query.CapabilityType != "" && query.CapabilityType != key.CapabilityType {
		return history.EffectiveCoverage{}, fmt.Errorf("coverage query capability type %q conflicts with capability key %q", query.CapabilityType, key.CapabilityType)
	}
	if query.CapabilityName != "" && query.CapabilityName != key.CapabilityName {
		return history.EffectiveCoverage{}, fmt.Errorf("coverage query capability name %q conflicts with capability key %q", query.CapabilityName, key.CapabilityName)
	}
	// Force all canonical key predicates into both epoch reads. This keeps a
	// single-key query sparse and prevents an accidental runtime-wide scan.
	query.Runtime = key.Runtime
	query.CapabilityType = key.CapabilityType
	query.CapabilityName = key.CapabilityName
	capture, presence, err := s.readCoverageEpochs(ctx, query)
	if err != nil {
		return history.EffectiveCoverage{}, err
	}
	return computeQueryCoverage(key, capture, presence, query)
}

// ExplainCoverageQueryPlan returns plans for both indexed epoch lookups used
// by QueryEffectiveCoverage. It is intentionally diagnostic-only and does not
// execute the coverage calculation.
func (s *Store) ExplainCoverageQueryPlan(ctx context.Context, query history.CoverageQuery) ([]string, error) {
	if s.isClosed() {
		return nil, errors.New("store is closed")
	}
	if err := query.Validate(); err != nil {
		return nil, err
	}
	identityQuery := query
	identityQuery.Start = time.Time{}
	identityQuery.End = time.Time{}
	plans := make([]string, 0, 3)
	for _, statement := range []struct {
		statement string
		args      []any
	}{
		{statement: `EXPLAIN QUERY PLAN SELECT runtime, started_at, ended_at FROM capture_epochs` + coverageCaptureWhere(query), args: coverageCaptureArgs(query)},
		{statement: `EXPLAIN QUERY PLAN SELECT runtime, capability_type, capability_name, scope, source, started_at, ended_at FROM capability_presence_epochs` + coveragePresenceWhere(query), args: coveragePresenceArgs(query)},
		{statement: `EXPLAIN QUERY PLAN SELECT DISTINCT runtime, capability_type, capability_name FROM capability_presence_epochs` + coveragePresenceWhere(identityQuery), args: coveragePresenceArgs(identityQuery)},
	} {
		rows, err := s.db.QueryContext(ctx, statement.statement, statement.args...)
		if err != nil {
			return nil, fmt.Errorf("explain effective coverage query: %w", err)
		}
		for rows.Next() {
			var id, parent, detail int
			var textDetail string
			if err := rows.Scan(&id, &parent, &detail, &textDetail); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan effective coverage query plan: %w", err)
			}
			plans = append(plans, textDetail)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("iterate effective coverage query plan: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close effective coverage query plan: %w", err)
		}
	}
	return plans, nil
}

func (s *Store) coverageKeys(ctx context.Context, query history.CoverageQuery) ([]history.CoverageKey, error) {
	keys := make(map[history.CoverageKey]struct{})
	// Discover canonical keys only from sparse epoch identity, independently of
	// the requested interval. A known key with no overlap is returned as
	// unknown rather than disappearing, while usage-only/current-only keys are
	// not treated as effective coverage identities.
	identityQuery := query
	identityQuery.Start = time.Time{}
	identityQuery.End = time.Time{}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT runtime, capability_type, capability_name FROM capability_presence_epochs`+coveragePresenceWhere(identityQuery), coveragePresenceArgs(identityQuery)...)
	if err != nil {
		return nil, fmt.Errorf("query effective coverage epoch keys: %w", err)
	}
	for rows.Next() {
		var key history.CoverageKey
		if err := rows.Scan(&key.Runtime, &key.CapabilityType, &key.CapabilityName); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan effective coverage epoch key: %w", err)
		}
		keys[key] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate effective coverage epoch keys: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close effective coverage epoch keys: %w", err)
	}
	if query.Runtime != "" && query.CapabilityType != "" && query.CapabilityName != "" {
		keys[history.CoverageKey{Runtime: query.Runtime, CapabilityType: query.CapabilityType, CapabilityName: query.CapabilityName}] = struct{}{}
	}
	result := make([]history.CoverageKey, 0, len(keys))
	for key := range keys {
		result = append(result, key)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Runtime != result[j].Runtime {
			return result[i].Runtime < result[j].Runtime
		}
		if result[i].CapabilityType != result[j].CapabilityType {
			return result[i].CapabilityType < result[j].CapabilityType
		}
		return result[i].CapabilityName < result[j].CapabilityName
	})
	return result, nil
}

func (s *Store) readCoverageEpochs(ctx context.Context, query history.CoverageQuery) ([]history.CaptureEpoch, []history.CapabilityPresenceEpoch, error) {
	captureRows, err := s.db.QueryContext(ctx, `SELECT runtime, started_at, ended_at, start_reason, end_reason FROM capture_epochs`+coverageCaptureWhere(query)+` ORDER BY runtime, started_at, id`, coverageCaptureArgs(query)...)
	if err != nil {
		return nil, nil, fmt.Errorf("query effective capture epochs: %w", err)
	}
	capture := make([]history.CaptureEpoch, 0)
	for captureRows.Next() {
		var epoch history.CaptureEpoch
		var started, ended, startReason, endReason sql.NullString
		if err := captureRows.Scan(&epoch.Runtime, &started, &ended, &startReason, &endReason); err != nil {
			captureRows.Close()
			return nil, nil, fmt.Errorf("scan effective capture epoch: %w", err)
		}
		epoch.Interval.Start, err = parseEpochTimestamp(started.String, "capture epoch start")
		if err != nil {
			captureRows.Close()
			return nil, nil, err
		}
		if ended.Valid && ended.String != "" {
			epoch.Interval.End, err = parseEpochTimestamp(ended.String, "capture epoch end")
			if err != nil {
				captureRows.Close()
				return nil, nil, err
			}
		}
		epoch.StartReason = history.CaptureStartReason(startReason.String)
		epoch.EndReason = history.CaptureEndReason(endReason.String)
		if err := epoch.Validate(); err != nil {
			captureRows.Close()
			return nil, nil, fmt.Errorf("validate persisted effective capture epoch: %w", err)
		}
		capture = append(capture, epoch)
	}
	if err := captureRows.Err(); err != nil {
		captureRows.Close()
		return nil, nil, fmt.Errorf("iterate effective capture epochs: %w", err)
	}
	if err := captureRows.Close(); err != nil {
		return nil, nil, fmt.Errorf("close effective capture epochs: %w", err)
	}

	presenceRows, err := s.db.QueryContext(ctx, `SELECT runtime, capability_type, capability_name, started_at, ended_at FROM capability_presence_epochs`+coveragePresenceWhere(query)+` ORDER BY runtime, capability_type, capability_name, started_at, id`, coveragePresenceArgs(query)...)
	if err != nil {
		return nil, nil, fmt.Errorf("query effective presence epochs: %w", err)
	}
	presence := make([]history.CapabilityPresenceEpoch, 0)
	for presenceRows.Next() {
		var epoch history.CapabilityPresenceEpoch
		var started, ended sql.NullString
		if err := presenceRows.Scan(&epoch.Runtime, &epoch.CapabilityType, &epoch.CapabilityName, &started, &ended); err != nil {
			presenceRows.Close()
			return nil, nil, fmt.Errorf("scan effective presence epoch: %w", err)
		}
		epoch.Interval.Start, err = parseEpochTimestamp(started.String, "capability presence start")
		if err != nil {
			presenceRows.Close()
			return nil, nil, err
		}
		if ended.Valid && ended.String != "" {
			epoch.Interval.End, err = parseEpochTimestamp(ended.String, "capability presence end")
			if err != nil {
				presenceRows.Close()
				return nil, nil, err
			}
		}
		if err := epoch.Validate(); err != nil {
			presenceRows.Close()
			return nil, nil, fmt.Errorf("validate persisted effective presence epoch: %w", err)
		}
		presence = append(presence, epoch)
	}
	if err := presenceRows.Err(); err != nil {
		presenceRows.Close()
		return nil, nil, fmt.Errorf("iterate effective presence epochs: %w", err)
	}
	if err := presenceRows.Close(); err != nil {
		return nil, nil, fmt.Errorf("close effective presence epochs: %w", err)
	}
	return capture, presence, nil
}

func computeQueryCoverage(key history.CoverageKey, capture []history.CaptureEpoch, presence []history.CapabilityPresenceEpoch, query history.CoverageQuery) (history.EffectiveCoverage, error) {
	coverage, err := history.ComputeEffectiveCoverage(key, capture, presence, nil)
	if err != nil {
		return history.EffectiveCoverage{}, err
	}
	start, end := query.Start, query.End
	if !start.IsZero() && !end.IsZero() && !end.After(start) {
		coverage.Intervals = []history.Interval{}
		coverage.Status = history.CoverageUnknown
		return coverage, nil
	}
	if !start.IsZero() && !end.IsZero() {
		coverage.Intervals, err = history.ClipIntervals(coverage.Intervals, history.Interval{Start: start, End: end})
	} else if !start.IsZero() {
		coverage.Intervals, err = history.ClipIntervals(coverage.Intervals, history.Interval{Start: start})
	} else if !end.IsZero() {
		coverage.Intervals, err = history.ClipIntervalsAsOf(coverage.Intervals, end)
	}
	if err != nil {
		return history.EffectiveCoverage{}, err
	}
	if len(coverage.Intervals) == 0 {
		coverage.Status = history.CoverageUnknown
	} else {
		coverage.Status = history.CoveragePartial
	}
	if err := coverage.Validate(); err != nil {
		return history.EffectiveCoverage{}, err
	}
	return coverage, nil
}

func coverageCaptureWhere(query history.CoverageQuery) string {
	conditions := make([]string, 0, 3)
	if query.Runtime != "" {
		conditions = append(conditions, "runtime = ?")
	}
	if coverageSQLBoundRepresentable(query.Start) {
		conditions = append(conditions, "(ended_at IS NULL OR ended_at > ?)")
	}
	if coverageSQLBoundRepresentable(query.End) {
		conditions = append(conditions, "started_at < ?")
	}
	if len(conditions) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(conditions, " AND ")
}

func coverageCaptureArgs(query history.CoverageQuery) []any {
	args := make([]any, 0, 3)
	if query.Runtime != "" {
		args = append(args, query.Runtime)
	}
	if coverageSQLBoundRepresentable(query.Start) {
		args = append(args, formatEpochTimestamp(query.Start))
	}
	if coverageSQLBoundRepresentable(query.End) {
		args = append(args, formatEpochTimestamp(query.End))
	}
	return args
}

func coveragePresenceWhere(query history.CoverageQuery) string {
	conditions := make([]string, 0, 5)
	if query.Runtime != "" {
		conditions = append(conditions, "runtime = ?")
	}
	if query.CapabilityType != "" {
		conditions = append(conditions, "capability_type = ?")
	}
	if query.CapabilityName != "" {
		conditions = append(conditions, "capability_name = ?")
	}
	if coverageSQLBoundRepresentable(query.Start) {
		conditions = append(conditions, "(ended_at IS NULL OR ended_at > ?)")
	}
	if coverageSQLBoundRepresentable(query.End) {
		conditions = append(conditions, "started_at < ?")
	}
	if len(conditions) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(conditions, " AND ")
}

func coveragePresenceArgs(query history.CoverageQuery) []any {
	args := make([]any, 0, 5)
	if query.Runtime != "" {
		args = append(args, query.Runtime)
	}
	if query.CapabilityType != "" {
		args = append(args, query.CapabilityType)
	}
	if query.CapabilityName != "" {
		args = append(args, query.CapabilityName)
	}
	if coverageSQLBoundRepresentable(query.Start) {
		args = append(args, formatEpochTimestamp(query.Start))
	}
	if coverageSQLBoundRepresentable(query.End) {
		args = append(args, formatEpochTimestamp(query.End))
	}
	return args
}

// coverageSQLBoundRepresentable reports whether a query bound can be compared
// safely with migration 007's fixed-width four-digit UTC timestamps. Bounds
// outside that persisted domain are applied only by in-memory clipping; an
// omitted SQL predicate avoids invalid values and lexicographic mismatches.
func coverageSQLBoundRepresentable(value time.Time) bool {
	if value.IsZero() {
		return false
	}
	value = value.UTC()
	return value.Year() >= 0 && value.Year() <= 9999 && len(value.Format(epochTimestampLayout)) == len(epochTimestampLayout)
}

func coverageIdentityFilters(alias, nameColumn string, query history.CoverageQuery) (string, []any) {
	conditions := make([]string, 0, 3)
	args := make([]any, 0, 3)
	if query.Runtime != "" {
		conditions = append(conditions, alias+`.runtime = ?`)
		args = append(args, query.Runtime)
	}
	if query.CapabilityType != "" {
		conditions = append(conditions, alias+`.capability_type = ?`)
		args = append(args, query.CapabilityType)
	}
	if query.CapabilityName != "" {
		conditions = append(conditions, alias+`.`+nameColumn+` = ?`)
		args = append(args, query.CapabilityName)
	}
	if len(conditions) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}
