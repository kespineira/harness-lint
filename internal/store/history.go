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
	invocationClause, invocationArgs := historyUsageFilters("u", query)
	evidenceClause, evidenceArgs := historyUsageFilters("u", query)
	stateClause, stateArgs := historyUsageFilters("u", query)
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
), state_counts AS (
	SELECT u.runtime, u.capability_type, u.capability_name,
		SUM(CASE WHEN u.event_type = 'advertised' THEN 1 ELSE 0 END) AS advertised_observations,
		SUM(CASE WHEN u.event_type = 'loaded' THEN 1 ELSE 0 END) AS loaded_observations,
		NULLIF(COUNT(DISTINCT CASE WHEN u.event_type = 'advertised' THEN u.session_id END), 0) AS advertised_sessions
	FROM usage_events AS u` + stateClause + `
	GROUP BY u.runtime, u.capability_type, u.capability_name
), advertised_sessions AS (
	SELECT DISTINCT u.runtime, u.capability_type, u.capability_name, u.session_id
	FROM usage_events AS u` + advertisedClause + `
), invoked_sessions AS (
	SELECT DISTINCT u.runtime, u.capability_type, u.capability_name, u.session_id
	FROM usage_events AS u` + invocationClause + `
), session_relationship AS (
	SELECT a.runtime, a.capability_type, a.capability_name,
		COUNT(DISTINCT i.session_id) AS invoked_in_advertised_sessions
	FROM advertised_sessions AS a
	LEFT JOIN invoked_sessions AS i
		ON i.runtime = a.runtime
		AND i.capability_type = a.capability_type
		AND i.capability_name = a.capability_name
		AND i.session_id = a.session_id
	GROUP BY a.runtime, a.capability_type, a.capability_name
)
SELECT k.runtime, k.capability_type, k.capability_name,
	COALESCE(i.uses, 0), COALESCE(i.distinct_sessions, 0),
	i.first_observed_at, i.last_observed_at,
	i.first_effective_at, i.last_effective_at,
	COALESCE(e.hook_count, 0), COALESCE(e.transcript_count, 0), COALESCE(e.import_count, 0),
	COALESCE(sc.advertised_observations, 0), COALESCE(sc.loaded_observations, 0),
	sc.advertised_sessions, sr.invoked_in_advertised_sessions
FROM keys AS k
LEFT JOIN invocation AS i ON i.runtime = k.runtime AND i.capability_type = k.capability_type AND i.capability_name = k.capability_name
LEFT JOIN evidence AS e ON e.runtime = k.runtime AND e.capability_type = k.capability_type AND e.capability_name = k.capability_name
LEFT JOIN state_counts AS sc ON sc.runtime = k.runtime AND sc.capability_type = k.capability_type AND sc.capability_name = k.capability_name
LEFT JOIN session_relationship AS sr ON sr.runtime = k.runtime AND sr.capability_type = k.capability_type AND sr.capability_name = k.capability_name
ORDER BY k.runtime, k.capability_type, k.capability_name`

	args := make([]any, 0, len(usageArgs)+len(currentArgs)+len(invocationArgs)+len(evidenceArgs)+len(stateArgs)+len(advertisedArgs)+len(invocationArgs))
	args = append(args, usageArgs...)
	args = append(args, currentArgs...)
	args = append(args, invocationArgs...)
	args = append(args, evidenceArgs...)
	args = append(args, stateArgs...)
	args = append(args, advertisedArgs...)
	args = append(args, invocationArgs...)
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
		var advertisedObservations, loadedObservations int64
		var advertisedSessions, invokedInAdvertisedSessions sql.NullInt64
		if err := rows.Scan(&aggregate.Runtime, &aggregate.CapabilityType, &aggregate.CapabilityName, &uses, &sessions, &firstObserved, &lastObserved, &firstEffective, &lastEffective, &hookCount, &transcriptCount, &importCount, &advertisedObservations, &loadedObservations, &advertisedSessions, &invokedInAdvertisedSessions); err != nil {
			return nil, fmt.Errorf("scan invocation history: %w", err)
		}
		aggregate.Uses = uses
		aggregate.DistinctInvocationSessions = sessions
		aggregate.AdvertisedObservations = advertisedObservations
		aggregate.LoadedObservations = loadedObservations
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
		if invokedInAdvertisedSessions.Valid {
			value := invokedInAdvertisedSessions.Int64
			aggregate.InvokedInAdvertisedSessions = &value
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
	coverage, err := s.historyCoverage(ctx, query)
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
		result[index].Coverage = coverage[key]
	}
	coverageQuery, err := coverageQueryForHistoryQuery(query)
	if err != nil {
		return nil, err
	}
	effective, err := s.QueryEffectiveCoverage(ctx, coverageQuery)
	if err != nil {
		return nil, err
	}
	effectiveByKey := make(map[historyKey]*history.EffectiveCoverage, len(effective))
	for index := range effective {
		item := effective[index]
		effectiveByKey[historyKey{runtime: item.Key.Runtime, typ: item.Key.CapabilityType, name: item.Key.CapabilityName}] = &item
	}
	for index := range result {
		key := historyKey{runtime: result[index].Runtime, typ: result[index].CapabilityType, name: result[index].CapabilityName}
		if item, ok := effectiveByKey[key]; ok {
			result[index].EffectiveCoverage = cloneEffectiveCoverage(*item)
		} else {
			result[index].EffectiveCoverage = &history.EffectiveCoverage{
				Key:       history.CoverageKey{Runtime: key.runtime, CapabilityType: key.typ, CapabilityName: key.name},
				Intervals: []history.Interval{}, Status: history.CoverageUnknown,
			}
		}
	}
	return result, nil
}

func historyUsageFilters(alias string, query history.Query) (string, []any) {
	conditions, args := historyIdentityConditions(alias, "capability_name", query)
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
	return historyIdentityFilters(alias, "name", query)
}

// coverageQueryForHistoryQuery translates the released closed event interval
// [Start, End] to the explicit half-open coverage interval [Start, End+1ns).
// A nanosecond is the store's timestamp precision. The in-memory coverage
// endpoint may therefore be year 10000 even though migration 007 cannot
// persist that endpoint; coverage SQL omits only out-of-domain time
// predicates and the final interval is clipped in memory.
func coverageQueryForHistoryQuery(query history.Query) (history.CoverageQuery, error) {
	coverage := history.CoverageQuery{
		Start:          query.Start,
		Runtime:        query.Runtime,
		CapabilityType: query.CapabilityType,
		CapabilityName: query.CapabilityName,
	}
	if query.End.IsZero() {
		return coverage, nil
	}
	coverage.End = addCoveragePrecision(query.End)
	if err := coverage.Validate(); err != nil {
		return history.CoverageQuery{}, fmt.Errorf("translate history interval to coverage interval: %w", err)
	}
	return coverage, nil
}

func addCoveragePrecision(value time.Time) (result time.Time) {
	value = value.UTC()
	defer func() {
		if recover() != nil {
			result = value
		}
	}()
	return value.Add(time.Nanosecond)
}

func cloneEffectiveCoverage(value history.EffectiveCoverage) *history.EffectiveCoverage {
	value.Intervals = append([]history.Interval(nil), value.Intervals...)
	return &value
}

func historyIdentityConditions(alias, nameColumn string, query history.Query) ([]string, []any) {
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
	return conditions, args
}

func historyIdentityFilters(alias, nameColumn string, query history.Query) (string, []any) {
	conditions, args := historyIdentityConditions(alias, nameColumn, query)
	if len(conditions) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

func historyIdentityAndFilters(alias, nameColumn string, query history.Query) (string, []any) {
	conditions, args := historyIdentityConditions(alias, nameColumn, query)
	if len(conditions) == 0 {
		return "", args
	}
	return " AND " + strings.Join(conditions, " AND "), args
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

// historyCoverage reads all recorded observation ranges for the query's
// identity filters, without applying its activity interval. Inventory windows
// come from the capability first_seen/last_seen fields; usage windows come
// from usage_events; direct-hook windows come from the normalized evidence
// relation. These are observations only and never imply continuity or
// lifetime completeness.
func (s *Store) historyCoverage(ctx context.Context, query history.Query) (map[historyKey]*history.Coverage, error) {
	if err := query.Validate(); err != nil {
		return nil, err
	}
	inventoryClause, inventoryArgs := historyIdentityFilters("c", "name", query)
	usageClause, usageArgs := historyIdentityFilters("u", "capability_name", query)
	hookClause, hookArgs := historyIdentityAndFilters("u", "capability_name", query)
	args := make([]any, 0, len(inventoryArgs)+len(usageArgs)+len(hookArgs))
	args = append(args, inventoryArgs...)
	args = append(args, usageArgs...)
	args = append(args, hookArgs...)
	rows, err := s.db.QueryContext(ctx, `WITH inventory AS (
	SELECT runtime, capability_type, name,
		MIN(NULLIF(first_seen, '')) AS first_inventory,
		MAX(NULLIF(last_seen, '')) AS last_inventory
	FROM capabilities AS c`+inventoryClause+`
	GROUP BY runtime, capability_type, name
), usage AS (
	SELECT runtime, capability_type, capability_name,
		MIN(observed_at) AS first_usage,
		MAX(observed_at) AS last_usage
	FROM usage_events AS u`+usageClause+`
	GROUP BY runtime, capability_type, capability_name
), direct_hook AS (
	SELECT u.runtime, u.capability_type, u.capability_name,
		MIN(e.observed_at) AS first_hook,
		MAX(e.observed_at) AS last_hook
	FROM usage_events AS u
	INNER JOIN usage_event_evidence AS e ON e.fingerprint = u.fingerprint
	WHERE e.provenance = 'hook'`+hookClause+`
	GROUP BY u.runtime, u.capability_type, u.capability_name
), keys AS (
	SELECT runtime, capability_type, name AS capability_name FROM inventory
	UNION
	SELECT runtime, capability_type, capability_name FROM usage
	UNION
	SELECT runtime, capability_type, capability_name FROM direct_hook
)
SELECT k.runtime, k.capability_type, k.capability_name,
	i.first_inventory, i.last_inventory,
	u.first_usage, u.last_usage,
	h.first_hook, h.last_hook
FROM keys AS k
LEFT JOIN inventory AS i ON i.runtime = k.runtime AND i.capability_type = k.capability_type AND i.name = k.capability_name
LEFT JOIN usage AS u ON u.runtime = k.runtime AND u.capability_type = k.capability_type AND u.capability_name = k.capability_name
LEFT JOIN direct_hook AS h ON h.runtime = k.runtime AND h.capability_type = k.capability_type AND h.capability_name = k.capability_name
	ORDER BY k.runtime, k.capability_type, k.capability_name`, args...)
	if err != nil {
		return nil, fmt.Errorf("query history coverage: %w", err)
	}
	defer rows.Close()
	result := make(map[historyKey]*history.Coverage)
	for rows.Next() {
		var runtime domain.Runtime
		var typ domain.CapabilityType
		var name string
		var firstInventory, lastInventory, firstUsage, lastUsage, firstHook, lastHook sql.NullString
		if err := rows.Scan(&runtime, &typ, &name, &firstInventory, &lastInventory, &firstUsage, &lastUsage, &firstHook, &lastHook); err != nil {
			return nil, fmt.Errorf("scan history coverage: %w", err)
		}
		coverage := &history.Coverage{}
		var err error
		coverage.FirstInventoryObservedAt, err = parseNullableTimestamp(firstInventory)
		if err != nil {
			return nil, fmt.Errorf("parse first inventory coverage: %w", err)
		}
		coverage.LastInventoryObservedAt, err = parseNullableTimestamp(lastInventory)
		if err != nil {
			return nil, fmt.Errorf("parse last inventory coverage: %w", err)
		}
		coverage.FirstUsageObservedAt, err = parseNullableTimestamp(firstUsage)
		if err != nil {
			return nil, fmt.Errorf("parse first usage coverage: %w", err)
		}
		coverage.LastUsageObservedAt, err = parseNullableTimestamp(lastUsage)
		if err != nil {
			return nil, fmt.Errorf("parse last usage coverage: %w", err)
		}
		coverage.FirstDirectHookObservedAt, err = parseNullableTimestamp(firstHook)
		if err != nil {
			return nil, fmt.Errorf("parse first direct-hook coverage: %w", err)
		}
		coverage.LastDirectHookObservedAt, err = parseNullableTimestamp(lastHook)
		if err != nil {
			return nil, fmt.Errorf("parse last direct-hook coverage: %w", err)
		}
		if coverage.FirstInventoryObservedAt == nil && coverage.LastInventoryObservedAt == nil && coverage.FirstUsageObservedAt == nil && coverage.LastUsageObservedAt == nil && coverage.FirstDirectHookObservedAt == nil && coverage.LastDirectHookObservedAt == nil {
			continue
		}
		result[historyKey{runtime: runtime, typ: typ, name: name}] = coverage
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate history coverage: %w", err)
	}
	return result, nil
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
	rows, err := s.db.QueryContext(ctx, `SELECT u.runtime, u.capability_type, u.capability_name,
	strftime('%Y-%m-01T00:00:00Z', COALESCE(u.source_timestamp, u.observed_at)) AS month,
	COUNT(*) AS uses, COUNT(DISTINCT u.session_id) AS distinct_sessions
	FROM usage_events AS u`+clause+`
	GROUP BY u.runtime, u.capability_type, u.capability_name, month
	ORDER BY u.runtime, u.capability_type, u.capability_name, month`, args...)
	if err != nil {
		return nil, fmt.Errorf("query monthly invocations: %w", err)
	}
	defer rows.Close()
	var result []history.MonthlyAggregate
	for rows.Next() {
		var aggregate history.MonthlyAggregate
		var month string
		if err := rows.Scan(&aggregate.Runtime, &aggregate.CapabilityType, &aggregate.CapabilityName, &month, &aggregate.Uses, &aggregate.DistinctInvocationSessions); err != nil {
			return nil, fmt.Errorf("scan monthly invocations: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339, month)
		if err != nil {
			return nil, fmt.Errorf("parse monthly invocation month: %w", err)
		}
		aggregate.Month = parsed.UTC()
		result = append(result, aggregate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate monthly invocations: %w", err)
	}
	return result, nil
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
