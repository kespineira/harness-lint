package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/history"
)

// These aliases keep the store-facing API discoverable while the DTOs remain
// neutral and reusable by later analysis/report layers.
type HistoryQuery = history.Query
type HistoryAggregate = history.Aggregate
type MonthlyHistoryAggregate = history.MonthlyAggregate
type UsageEventEvidence = history.EventEvidence

type historyKey struct {
	runtime domain.Runtime
	typ     domain.CapabilityType
	name    string
}

// QueryInvocationHistory returns current-inventory keys and observed usage
// keys in deterministic runtime/type/name order. The interval in query is a
// closed UTC interval [Start, End] over effective activity time; exact
// boundaries are included. A current capability with no invocation rows is
// returned with zero uses, while an observed usage-only key has Installed=false.
func (s *Store) QueryInvocationHistory(ctx context.Context, query history.Query) ([]history.Aggregate, error) {
	if s.isClosed() {
		return nil, errors.New("store is closed")
	}
	if err := query.Validate(); err != nil {
		return nil, err
	}
	usageClause, usageArgs := historyUsageFilters("u", query)
	currentClause, currentArgs := historyCurrentFilters("c", query)
	activityClause, activityArgs := historyUsageFilters("u", query)
	invocationClause, invocationArgs := historyUsageFilters("u", query)
	evidenceClause, evidenceArgs := historyUsageFilters("u", query)
	advertisedClause, advertisedArgs := historyUsageFilters("u", query)
	invocationClause += historyEventTypeFilter(invocationClause, "invoked")
	evidenceClause += historyEventTypeFilter(evidenceClause, "invoked")
	advertisedClause += historyEventTypeFilter(advertisedClause, "advertised")
	statement := `WITH keys AS (
	SELECT u.runtime, u.capability_type, u.capability_name
	FROM usage_events AS u` + usageClause + `
	UNION
SELECT c.runtime, c.capability_type, c.name
	FROM current_inventory AS ci
	INNER JOIN capabilities AS c
		ON c.runtime = ci.runtime
		AND c.capability_type = ci.capability_type
		AND c.name = ci.name
		AND c.scope = ci.scope
		AND c.source = ci.source` + currentClause + `
), activity AS (
	SELECT u.runtime, u.capability_type, u.capability_name,
		MIN(u.observed_at) AS first_observed_at,
		MAX(u.observed_at) AS last_observed_at,
		MIN(COALESCE(u.source_timestamp, u.observed_at)) AS first_effective_at,
		MAX(COALESCE(u.source_timestamp, u.observed_at)) AS last_effective_at
	FROM usage_events AS u` + activityClause + `
	GROUP BY u.runtime, u.capability_type, u.capability_name
), invocation AS (
	SELECT u.runtime, u.capability_type, u.capability_name,
		COUNT(*) AS uses,
		COUNT(DISTINCT u.session_id) AS distinct_sessions,
		MIN(u.observed_at) AS first_observed_at,
		MAX(u.observed_at) AS last_observed_at,
		MIN(COALESCE(u.source_timestamp, u.observed_at)) AS first_effective_at,
		MAX(COALESCE(u.source_timestamp, u.observed_at)) AS last_effective_at
	FROM usage_events AS u` + invocationClause + `
	GROUP BY u.runtime, u.capability_type, u.capability_name
), evidence AS (
	SELECT u.runtime, u.capability_type, u.capability_name,
		SUM(CASE WHEN e.provenance = 'hook' THEN 1 ELSE 0 END) AS hook_count,
		SUM(CASE WHEN e.provenance = 'transcript' THEN 1 ELSE 0 END) AS transcript_count,
		SUM(CASE WHEN e.provenance = 'import' THEN 1 ELSE 0 END) AS import_count
	FROM usage_events AS u
	INNER JOIN usage_event_evidence AS e ON e.fingerprint = u.fingerprint` + evidenceClause + `
	GROUP BY u.runtime, u.capability_type, u.capability_name
), advertised AS (
	SELECT u.runtime, u.capability_type, u.capability_name,
		COUNT(DISTINCT u.session_id) AS advertised_sessions
	FROM usage_events AS u` + advertisedClause + `
	GROUP BY u.runtime, u.capability_type, u.capability_name
)
SELECT k.runtime, k.capability_type, k.capability_name,
	COALESCE(i.uses, 0), COALESCE(i.distinct_sessions, 0),
	activity.first_observed_at, activity.last_observed_at,
	activity.first_effective_at, activity.last_effective_at,
	COALESCE(e.hook_count, 0), COALESCE(e.transcript_count, 0), COALESCE(e.import_count, 0),
	ad.advertised_sessions
FROM keys AS k
LEFT JOIN activity ON activity.runtime = k.runtime AND activity.capability_type = k.capability_type AND activity.capability_name = k.capability_name
LEFT JOIN invocation AS i ON i.runtime = k.runtime AND i.capability_type = k.capability_type AND i.capability_name = k.capability_name
LEFT JOIN evidence AS e ON e.runtime = k.runtime AND e.capability_type = k.capability_type AND e.capability_name = k.capability_name
LEFT JOIN advertised AS ad ON ad.runtime = k.runtime AND ad.capability_type = k.capability_type AND ad.capability_name = k.capability_name
ORDER BY k.runtime, k.capability_type, k.capability_name`

	args := make([]any, 0, len(usageArgs)+len(currentArgs)+len(activityArgs)+len(invocationArgs)+len(evidenceArgs)+len(advertisedArgs))
	args = append(args, usageArgs...)
	args = append(args, currentArgs...)
	args = append(args, activityArgs...)
	args = append(args, invocationArgs...)
	args = append(args, evidenceArgs...)
	args = append(args, advertisedArgs...)
	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("query invocation history: %w", err)
	}
	defer rows.Close()
	result := make([]history.Aggregate, 0)
	for rows.Next() {
		var aggregate history.Aggregate
		var firstObserved, lastObserved, firstEffective, lastEffective sql.NullString
		var uses, sessions, hookCount, transcriptCount, importCount int64
		var advertisedSessions sql.NullInt64
		if err := rows.Scan(&aggregate.Runtime, &aggregate.CapabilityType, &aggregate.CapabilityName, &uses, &sessions, &firstObserved, &lastObserved, &firstEffective, &lastEffective, &hookCount, &transcriptCount, &importCount, &advertisedSessions); err != nil {
			return nil, fmt.Errorf("scan invocation history: %w", err)
		}
		aggregate.Uses = uses
		aggregate.InvocationUses = uses
		aggregate.DistinctInvocationSessions = sessions
		aggregate.FirstObservedAt, err = parseNullableTimestamp(firstObserved)
		if err != nil {
			return nil, fmt.Errorf("parse history first observed time: %w", err)
		}
		aggregate.LastObservedAt, err = parseNullableTimestamp(lastObserved)
		if err != nil {
			return nil, fmt.Errorf("parse history last observed time: %w", err)
		}
		aggregate.FirstEffectiveActivityAt, err = parseNullableTimestamp(firstEffective)
		if err != nil {
			return nil, fmt.Errorf("parse history first effective time: %w", err)
		}
		aggregate.LastEffectiveActivityAt, err = parseNullableTimestamp(lastEffective)
		if err != nil {
			return nil, fmt.Errorf("parse history last effective time: %w", err)
		}
		aggregate.InvocationEvidence = map[domain.Provenance]int64{
			domain.ProvenanceHook:       hookCount,
			domain.ProvenanceTranscript: transcriptCount,
			domain.ProvenanceImport:     importCount,
		}
		if advertisedSessions.Valid {
			value := advertisedSessions.Int64
			aggregate.ObservedAdvertisedSessions = &value
		}
		result = append(result, aggregate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate invocation history: %w", err)
	}

	installed, err := s.currentInventoryScopes(ctx, query)
	if err != nil {
		return nil, err
	}
	for index := range result {
		key := historyKey{runtime: result[index].Runtime, typ: result[index].CapabilityType, name: result[index].CapabilityName}
		scopes := installed[key]
		if len(scopes) > 0 {
			result[index].Installed = true
			result[index].InstalledScopes = scopes
		}
		result[index] = result[index].Normalize()
	}
	return result, nil
}

// ListHistoricalAggregates is an alias for QueryInvocationHistory.
func (s *Store) ListHistoricalAggregates(ctx context.Context, query history.Query) ([]history.Aggregate, error) {
	return s.QueryInvocationHistory(ctx, query)
}

// QueryHistoricalAggregates is an alias for QueryInvocationHistory.
func (s *Store) QueryHistoricalAggregates(ctx context.Context, query history.Query) ([]history.Aggregate, error) {
	return s.QueryInvocationHistory(ctx, query)
}

// QueryUsageHistory is a concise alias for QueryInvocationHistory.
func (s *Store) QueryUsageHistory(ctx context.Context, query history.Query) ([]history.Aggregate, error) {
	return s.QueryInvocationHistory(ctx, query)
}

func historyUsageFilters(alias string, query history.Query) (string, []any) {
	conditions := make([]string, 0, 5)
	args := make([]any, 0, 5)
	if query.Runtime != "" {
		conditions = append(conditions, alias+`.runtime = ?`)
		args = append(args, query.Runtime)
	}
	if typ := query.ResolvedType(); typ != "" {
		conditions = append(conditions, alias+`.capability_type = ?`)
		args = append(args, typ)
	}
	if name := query.ResolvedName(); name != "" {
		conditions = append(conditions, alias+`.capability_name = ?`)
		args = append(args, name)
	}
	if !query.Start.IsZero() {
		conditions = append(conditions, `COALESCE(`+alias+`.source_timestamp, `+alias+`.observed_at) >= ?`)
		args = append(args, query.Start.UTC().Format(time.RFC3339Nano))
	}
	if !query.End.IsZero() {
		conditions = append(conditions, `COALESCE(`+alias+`.source_timestamp, `+alias+`.observed_at) <= ?`)
		args = append(args, query.End.UTC().Format(time.RFC3339Nano))
	}
	if len(conditions) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

func historyCurrentFilters(alias string, query history.Query) (string, []any) {
	conditions := make([]string, 0, 3)
	args := make([]any, 0, 3)
	if query.Runtime != "" {
		conditions = append(conditions, alias+`.runtime = ?`)
		args = append(args, query.Runtime)
	}
	if typ := query.ResolvedType(); typ != "" {
		conditions = append(conditions, alias+`.capability_type = ?`)
		args = append(args, typ)
	}
	if name := query.ResolvedName(); name != "" {
		conditions = append(conditions, alias+`.name = ?`)
		args = append(args, name)
	}
	if len(conditions) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

func historyEventTypeFilter(clause, eventType string) string {
	if clause == "" {
		return " WHERE u.event_type = '" + eventType + "'"
	}
	return " AND u.event_type = '" + eventType + "'"
}

func (s *Store) currentInventoryScopes(ctx context.Context, query history.Query) (map[historyKey][]domain.Scope, error) {
	clause, args := historyCurrentFilters("c", query)
	rows, err := s.db.QueryContext(ctx, `SELECT c.runtime, c.capability_type, c.name, c.scope
	FROM current_inventory AS ci
	INNER JOIN capabilities AS c
		ON c.runtime = ci.runtime
		AND c.capability_type = ci.capability_type
		AND c.name = ci.name
		AND c.scope = ci.scope
		AND c.source = ci.source`+clause+` ORDER BY c.runtime, c.capability_type, c.name, c.scope`, args...)
	if err != nil {
		return nil, fmt.Errorf("query current history inventory: %w", err)
	}
	defer rows.Close()
	result := make(map[historyKey][]domain.Scope)
	for rows.Next() {
		var runtime domain.Runtime
		var typ domain.CapabilityType
		var name string
		var scope domain.Scope
		if err := rows.Scan(&runtime, &typ, &name, &scope); err != nil {
			return nil, fmt.Errorf("scan current history inventory: %w", err)
		}
		key := historyKey{runtime: runtime, typ: typ, name: name}
		if !containsScope(result[key], scope) {
			result[key] = append(result[key], scope)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate current history inventory: %w", err)
	}
	for key := range result {
		sort.Slice(result[key], func(i, j int) bool { return result[key][i] < result[key][j] })
	}
	return result, nil
}

func containsScope(scopes []domain.Scope, want domain.Scope) bool {
	for _, scope := range scopes {
		if scope == want {
			return true
		}
	}
	return false
}

// ListUsageEventEvidence returns all normalized evidence sources for one
// canonical fingerprint in deterministic provenance order.
func (s *Store) ListUsageEventEvidence(ctx context.Context, fingerprint string) ([]history.EventEvidence, error) {
	if s.isClosed() {
		return nil, errors.New("store is closed")
	}
	if strings.TrimSpace(fingerprint) == "" {
		return nil, errors.New("usage event fingerprint is required")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT fingerprint, provenance, observed_at, source_timestamp, invocation_origin, source_identity FROM usage_event_evidence WHERE fingerprint = ? ORDER BY CASE provenance WHEN 'hook' THEN 0 WHEN 'transcript' THEN 1 WHEN 'import' THEN 2 ELSE 3 END`, fingerprint)
	if err != nil {
		return nil, fmt.Errorf("list usage event evidence: %w", err)
	}
	defer rows.Close()
	var result []history.EventEvidence
	for rows.Next() {
		var evidence history.EventEvidence
		var observedAt, sourceTimestamp sql.NullString
		if err := rows.Scan(&evidence.Fingerprint, &evidence.Provenance, &observedAt, &sourceTimestamp, &evidence.InvocationOrigin, &evidence.SourceIdentity); err != nil {
			return nil, fmt.Errorf("scan usage event evidence: %w", err)
		}
		if !observedAt.Valid || observedAt.String == "" {
			return nil, errors.New("usage event evidence observed_at is empty")
		}
		evidence.ObservedAt, err = time.Parse(time.RFC3339Nano, observedAt.String)
		if err != nil {
			return nil, fmt.Errorf("parse usage evidence observed_at: %w", err)
		}
		evidence.ObservedAt = evidence.ObservedAt.UTC()
		if sourceTimestamp.Valid && sourceTimestamp.String != "" {
			parsed, parseErr := time.Parse(time.RFC3339Nano, sourceTimestamp.String)
			if parseErr != nil {
				return nil, fmt.Errorf("parse usage evidence source timestamp: %w", parseErr)
			}
			parsed = parsed.UTC()
			evidence.SourceTimestamp = &parsed
		}
		result = append(result, evidence)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage event evidence: %w", err)
	}
	return result, nil
}

// ListEventEvidence is a descriptive alias for ListUsageEventEvidence.
func (s *Store) ListEventEvidence(ctx context.Context, fingerprint string) ([]history.EventEvidence, error) {
	return s.ListUsageEventEvidence(ctx, fingerprint)
}

// QueryMonthlyInvocations returns UTC calendar-month usage subtotals. Query's
// Start and End remain closed [Start, End] boundaries, and runtime/type/name
// filters compose with them.
func (s *Store) QueryMonthlyInvocations(ctx context.Context, query history.Query) ([]history.MonthlyAggregate, error) {
	if s.isClosed() {
		return nil, errors.New("store is closed")
	}
	if err := query.Validate(); err != nil {
		return nil, err
	}
	clause, args := historyUsageFilters("u", query)
	clause += historyEventTypeFilter(clause, "invoked")
	rows, err := s.db.QueryContext(ctx, `SELECT u.runtime, u.capability_type,
	strftime('%Y-%m-01T00:00:00Z', COALESCE(u.source_timestamp, u.observed_at)) AS month,
	COUNT(*) AS uses, COUNT(DISTINCT u.session_id) AS distinct_sessions
	FROM usage_events AS u`+clause+`
	GROUP BY u.runtime, u.capability_type, month
	ORDER BY month, u.runtime, u.capability_type`, args...)
	if err != nil {
		return nil, fmt.Errorf("query monthly invocations: %w", err)
	}
	defer rows.Close()
	var result []history.MonthlyAggregate
	for rows.Next() {
		var aggregate history.MonthlyAggregate
		var month string
		if err := rows.Scan(&aggregate.Runtime, &aggregate.CapabilityType, &month, &aggregate.Uses, &aggregate.DistinctInvocationSessions); err != nil {
			return nil, fmt.Errorf("scan monthly invocations: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339, month)
		if err != nil {
			return nil, fmt.Errorf("parse monthly invocation month: %w", err)
		}
		aggregate.Month = parsed.UTC()
		aggregate.InvocationUses = aggregate.Uses
		aggregate.DistinctSessions = aggregate.DistinctInvocationSessions
		result = append(result, aggregate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate monthly invocations: %w", err)
	}
	return result, nil
}

// MonthlyInvocationAggregates is an alias for QueryMonthlyInvocations.
func (s *Store) MonthlyInvocationAggregates(ctx context.Context, query history.Query) ([]history.MonthlyAggregate, error) {
	return s.QueryMonthlyInvocations(ctx, query)
}

// QueryMonthlyUsage is a concise alias for QueryMonthlyInvocations.
func (s *Store) QueryMonthlyUsage(ctx context.Context, query history.Query) ([]history.MonthlyAggregate, error) {
	return s.QueryMonthlyInvocations(ctx, query)
}

// ExplainHistoryQueryPlan returns the SQLite plan for the primary filtered
// invocation aggregate path. It is intentionally small so tests and health
// checks can assert that the v6 history index is used for composed filters.
func (s *Store) ExplainHistoryQueryPlan(ctx context.Context, query history.Query) ([]string, error) {
	if s.isClosed() {
		return nil, errors.New("store is closed")
	}
	if err := query.Validate(); err != nil {
		return nil, err
	}
	clause, args := historyUsageFilters("u", query)
	clause += historyEventTypeFilter(clause, "invoked")
	rows, err := s.db.QueryContext(ctx, `EXPLAIN QUERY PLAN SELECT u.runtime, u.capability_type, u.capability_name, COUNT(*) FROM usage_events AS u`+clause+` GROUP BY u.runtime, u.capability_type, u.capability_name`, args...)
	if err != nil {
		return nil, fmt.Errorf("explain history query: %w", err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, detail int
		var textDetail string
		if err := rows.Scan(&id, &parent, &detail, &textDetail); err != nil {
			return nil, fmt.Errorf("scan history query plan: %w", err)
		}
		plan = append(plan, textDetail)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate history query plan: %w", err)
	}
	return plan, nil
}
