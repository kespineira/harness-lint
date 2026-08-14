package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/kespineira/harness-lint/internal/history"
)

// QueryEffectiveCoverage computes modeled coverage from the sparse epoch
// tables. Usage events are consulted only for distinct aggregate keys; event
// rows themselves are never loaded into memory. Presence epochs for different
// scopes/sources intentionally collapse to the canonical history key after
// applying any requested scope/source filter.
func (s *Store) QueryEffectiveCoverage(ctx context.Context, query history.Query) ([]history.EffectiveCoverage, error) {
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
func (s *Store) QueryCapabilityCoverage(ctx context.Context, key history.CoverageKey, query history.Query) (history.EffectiveCoverage, error) {
	if s.isClosed() {
		return history.EffectiveCoverage{}, errors.New("store is closed")
	}
	if err := query.Validate(); err != nil {
		return history.EffectiveCoverage{}, err
	}
	if err := key.Validate(); err != nil {
		return history.EffectiveCoverage{}, err
	}
	if query.Runtime != "" && query.Runtime != key.Runtime || query.CapabilityType != "" && query.CapabilityType != key.CapabilityType || query.CapabilityName != "" && query.CapabilityName != key.CapabilityName {
		return history.EffectiveCoverage{Key: key, Status: history.CoverageUnknown, Intervals: []history.Interval{}}, nil
	}
	capture, presence, err := s.readCoverageEpochs(ctx, query)
	if err != nil {
		return history.EffectiveCoverage{}, err
	}
	return computeQueryCoverage(key, capture, presence, query)
}

// ExplainCoverageQueryPlan returns plans for both indexed epoch lookups used
// by QueryEffectiveCoverage. It is intentionally diagnostic-only and does not
// execute the coverage calculation.
func (s *Store) ExplainCoverageQueryPlan(ctx context.Context, query history.Query) ([]string, error) {
	if s.isClosed() {
		return nil, errors.New("store is closed")
	}
	if err := query.Validate(); err != nil {
		return nil, err
	}
	plans := make([]string, 0, 2)
	for _, statement := range []struct {
		statement string
		args      []any
	}{
		{statement: `EXPLAIN QUERY PLAN SELECT runtime, started_at, ended_at FROM capture_epochs` + coverageCaptureWhere(query), args: coverageCaptureArgs(query)},
		{statement: `EXPLAIN QUERY PLAN SELECT runtime, capability_type, capability_name, scope, source, started_at, ended_at FROM capability_presence_epochs` + coveragePresenceWhere(query), args: coveragePresenceArgs(query)},
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

func (s *Store) coverageKeys(ctx context.Context, query history.Query) ([]history.CoverageKey, error) {
	keys := make(map[history.CoverageKey]struct{})
	usageClause, usageArgs := historyUsageFilters("u", query)
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT u.runtime, u.capability_type, u.capability_name FROM usage_events AS u`+usageClause, usageArgs...)
	if err != nil {
		return nil, fmt.Errorf("query effective coverage usage keys: %w", err)
	}
	for rows.Next() {
		var key history.CoverageKey
		if err := rows.Scan(&key.Runtime, &key.CapabilityType, &key.CapabilityName); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan effective coverage usage key: %w", err)
		}
		keys[key] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate effective coverage usage keys: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close effective coverage usage keys: %w", err)
	}

	currentClause, currentArgs := historyCurrentFilters("c", query)
	rows, err = s.db.QueryContext(ctx, `SELECT DISTINCT c.runtime, c.capability_type, c.name FROM current_inventory AS ci INNER JOIN capabilities AS c ON c.runtime = ci.runtime AND c.capability_type = ci.capability_type AND c.name = ci.name AND c.scope = ci.scope AND c.source = ci.source`+currentClause, currentArgs...)
	if err != nil {
		return nil, fmt.Errorf("query effective coverage inventory keys: %w", err)
	}
	for rows.Next() {
		var key history.CoverageKey
		if err := rows.Scan(&key.Runtime, &key.CapabilityType, &key.CapabilityName); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan effective coverage inventory key: %w", err)
		}
		keys[key] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate effective coverage inventory keys: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close effective coverage inventory keys: %w", err)
	}

	// A capability with only epoch evidence is still a valid query result.
	rows, err = s.db.QueryContext(ctx, `SELECT DISTINCT runtime, capability_type, capability_name FROM capability_presence_epochs`+coveragePresenceWhere(query), coveragePresenceArgs(query)...)
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

func (s *Store) readCoverageEpochs(ctx context.Context, query history.Query) ([]history.CaptureEpoch, []history.CapabilityPresenceEpoch, error) {
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

func computeQueryCoverage(key history.CoverageKey, capture []history.CaptureEpoch, presence []history.CapabilityPresenceEpoch, query history.Query) (history.EffectiveCoverage, error) {
	coverage, err := history.ComputeEffectiveCoverage(key, capture, presence, nil)
	if err != nil {
		return history.EffectiveCoverage{}, err
	}
	start, end := query.Start, query.End
	if !query.AsOf.IsZero() && (end.IsZero() || query.AsOf.Before(end)) {
		end = query.AsOf
	}
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

func coverageCaptureWhere(query history.Query) string {
	if query.Runtime == "" {
		return ""
	}
	return " WHERE runtime = ?"
}

func coverageCaptureArgs(query history.Query) []any {
	if query.Runtime == "" {
		return nil
	}
	return []any{query.Runtime}
}

func coveragePresenceWhere(query history.Query) string {
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
	if query.Scope != "" {
		conditions = append(conditions, "scope = ?")
	}
	if query.Source != "" {
		conditions = append(conditions, "source = ?")
	}
	if len(conditions) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(conditions, " AND ")
}

func coveragePresenceArgs(query history.Query) []any {
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
	if query.Scope != "" {
		args = append(args, query.Scope)
	}
	if query.Source != "" {
		args = append(args, query.Source)
	}
	return args
}
