package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"
)

// DatabaseStatus is the bounded metadata returned by Status. It deliberately
// excludes the database handle and all persisted identifiers or payloads.
// SizeBytes is nil when the database is not backed by a stat-able main file
// (for example, an in-memory database).
type DatabaseStatus struct {
	Path                      string
	Schema                    SchemaStatus
	SizeBytes                 *int64
	UsageEventCount           int64
	OldestEffectiveActivityAt *time.Time
	LatestEffectiveActivityAt *time.Time
}

// IntegrityState is a coarse result for one read-only integrity check.
type IntegrityState string

const (
	IntegrityOK          IntegrityState = "ok"
	IntegrityIssues      IntegrityState = "issues"
	IntegrityUnavailable IntegrityState = "unavailable"
)

const maxIntegrityIssues = 16

// IntegrityIssue identifies only the kind of failed check. SQLite result rows
// are intentionally not returned because they may contain implementation or
// user-controlled names.
type IntegrityIssue struct {
	Check string
}

// IntegrityResult reports bounded, read-only structural diagnostics. Check
// runs quick_check, foreign_key_check, and schema validation independently;
// it never migrates, repairs, checkpoints, or otherwise mutates the database.
type IntegrityResult struct {
	Healthy         bool
	QuickCheck      IntegrityState
	ForeignKeyCheck IntegrityState
	Schema          IntegrityState
	Issues          []IntegrityIssue
}

// Status returns cheap metadata about the store without running integrity
// pragmas. A valid empty database has a zero usage count and nil activity
// timestamps.
func (s *Store) Status(ctx context.Context) (DatabaseStatus, error) {
	if s.isClosed() {
		return DatabaseStatus{}, errors.New("store is closed")
	}
	ctx = nonNilContext(ctx)
	schema, err := s.SchemaStatus(ctx)
	if err != nil {
		return DatabaseStatus{}, fmt.Errorf("read database schema status: %w", err)
	}
	var count int64
	var oldest, latest sql.NullString
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), MIN(COALESCE(source_timestamp, observed_at)),
		       MAX(COALESCE(source_timestamp, observed_at))
		FROM usage_events`).Scan(&count, &oldest, &latest); err != nil {
		return DatabaseStatus{}, fmt.Errorf("read database usage status: %w", err)
	}
	oldestAt, err := parseNullableTimestamp(oldest)
	if err != nil {
		return DatabaseStatus{}, fmt.Errorf("parse oldest activity timestamp: %w", err)
	}
	latestAt, err := parseNullableTimestamp(latest)
	if err != nil {
		return DatabaseStatus{}, fmt.Errorf("parse latest activity timestamp: %w", err)
	}
	return DatabaseStatus{
		Path:                      s.path,
		Schema:                    schema,
		SizeBytes:                 mainDatabaseSize(s.path),
		UsageEventCount:           count,
		OldestEffectiveActivityAt: oldestAt,
		LatestEffectiveActivityAt: latestAt,
	}, nil
}

// Check runs the three independent, read-only database diagnostics. Database
// access failures are returned as errors; failed checks themselves are
// represented in the bounded result so callers can inspect all checks.
func (s *Store) Check(ctx context.Context) (IntegrityResult, error) {
	if s.isClosed() {
		return IntegrityResult{}, errors.New("store is closed")
	}
	return s.checkWithMigrationFS(nonNilContext(ctx), migrations)
}

func (s *Store) checkWithMigrationFS(ctx context.Context, migrationFS fs.FS) (IntegrityResult, error) {
	result := IntegrityResult{
		QuickCheck:      IntegrityUnavailable,
		ForeignKeyCheck: IntegrityUnavailable,
		Schema:          IntegrityUnavailable,
	}

	quickIssues, err := readQuickCheck(ctx, s.db)
	if err != nil {
		return IntegrityResult{}, fmt.Errorf("run sqlite quick check: %w", err)
	}
	result.QuickCheck = stateForIssues(quickIssues)
	result.Issues = appendBoundedIssues(result.Issues, quickIssues)

	foreignKeyIssues, err := readForeignKeyCheck(ctx, s.db)
	if err != nil {
		return IntegrityResult{}, fmt.Errorf("run sqlite foreign key check: %w", err)
	}
	result.ForeignKeyCheck = stateForIssues(foreignKeyIssues)
	result.Issues = appendBoundedIssues(result.Issues, foreignKeyIssues)

	available, err := loadMigrations(migrationFS)
	if err != nil {
		result.Schema = IntegrityIssues
		result.Issues = appendBoundedIssues(result.Issues, []IntegrityIssue{{Check: "schema_migrations"}})
	} else {
		var current int
		current, err = readSchemaVersion(ctx, s.db)
		if err != nil {
			if strings.Contains(err.Error(), "schema version") {
				result.Schema = IntegrityIssues
				result.Issues = appendBoundedIssues(result.Issues, []IntegrityIssue{{Check: "schema_version_invalid"}})
			} else {
				return IntegrityResult{}, fmt.Errorf("read schema version for integrity check: %w", err)
			}
		}
		if err != nil {
			result.Healthy = false
			return result, nil
		}
		latest := available[len(available)-1].version
		switch {
		case current == latest:
			result.Schema = IntegrityOK
		case current > latest:
			result.Schema = IntegrityIssues
			result.Issues = appendBoundedIssues(result.Issues, []IntegrityIssue{{Check: "schema_version_newer"}})
		default:
			result.Schema = IntegrityIssues
			result.Issues = appendBoundedIssues(result.Issues, []IntegrityIssue{{Check: "schema_version_mismatch"}})
		}
	}
	result.Healthy = result.QuickCheck == IntegrityOK && result.ForeignKeyCheck == IntegrityOK && result.Schema == IntegrityOK
	return result, nil
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func mainDatabaseSize(path string) *int64 {
	if path == "" || strings.HasPrefix(path, ":memory:") || strings.HasPrefix(path, "file::memory:") {
		return nil
	}
	statPath := path
	if strings.HasPrefix(statPath, "file:") {
		statPath = strings.TrimPrefix(statPath, "file:")
		if index := strings.IndexByte(statPath, '?'); index >= 0 {
			statPath = statPath[:index]
		}
	}
	if index := strings.IndexByte(statPath, '?'); index >= 0 {
		statPath = statPath[:index]
	}
	info, err := os.Stat(statPath)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	size := info.Size()
	return &size
}

func readQuickCheck(ctx context.Context, db *sql.DB) ([]IntegrityIssue, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA quick_check`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
		if len(values) >= maxIntegrityIssues+1 {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return parseIntegrityRows("quick_check", values), nil
}

func readForeignKeyCheck(ctx context.Context, db *sql.DB) ([]IntegrityIssue, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	issues := make([]IntegrityIssue, 0, maxIntegrityIssues)
	for rows.Next() {
		values := make([]any, len(columns))
		for index := range values {
			values[index] = new(any)
		}
		if err := rows.Scan(values...); err != nil {
			return nil, err
		}
		if len(issues) < maxIntegrityIssues {
			issues = append(issues, IntegrityIssue{Check: "foreign_key_check"})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return issues, nil
}

func parseIntegrityRows(check string, values []string) []IntegrityIssue {
	issues := make([]IntegrityIssue, 0, minInt(len(values), maxIntegrityIssues))
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), "ok") {
			continue
		}
		if len(issues) >= maxIntegrityIssues {
			break
		}
		issues = append(issues, IntegrityIssue{Check: check})
	}
	return issues
}

func appendBoundedIssues(existing, additions []IntegrityIssue) []IntegrityIssue {
	remaining := maxIntegrityIssues - len(existing)
	if remaining <= 0 {
		return existing
	}
	if len(additions) > remaining {
		additions = additions[:remaining]
	}
	return append(existing, additions...)
}

func stateForIssues(issues []IntegrityIssue) IntegrityState {
	if len(issues) == 0 {
		return IntegrityOK
	}
	return IntegrityIssues
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
