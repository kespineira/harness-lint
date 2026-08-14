package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kespineira/harness-lint/internal/store"
)

// DatabaseStatusSchemaVersion is the public JSON schema version for db
// status. The DTO intentionally omits the local database path and all event
// identifiers or payloads.
const DatabaseStatusSchemaVersion = 1

// DatabaseCheckSchemaVersion is the public JSON schema version for db check.
const DatabaseCheckSchemaVersion = 1

type DatabaseSchemaDTO struct {
	Current int `json:"current"`
	Latest  int `json:"latest"`
}

// DatabaseStatusDocument is the stable, privacy-preserving db status DTO.
type DatabaseStatusDocument struct {
	SchemaVersion    int               `json:"schema_version"`
	GeneratedAt      string            `json:"generated_at"`
	Schema           DatabaseSchemaDTO `json:"schema"`
	SizeBytes        *int64            `json:"size_bytes"`
	UsageEventCount  int64             `json:"usage_event_count"`
	OldestObservedAt *string           `json:"oldest_observed_at"`
	LatestObservedAt *string           `json:"latest_observed_at"`
	IntegrityChecked bool              `json:"integrity_checked"`
}

type DatabaseIssueDTO struct {
	Check string `json:"check"`
}

// DatabaseCheckDocument is the stable, privacy-preserving db check DTO.
type DatabaseCheckDocument struct {
	SchemaVersion   int                `json:"schema_version"`
	GeneratedAt     string             `json:"generated_at"`
	Healthy         bool               `json:"healthy"`
	QuickCheck      string             `json:"quick_check"`
	ForeignKeyCheck string             `json:"foreign_key_check"`
	Schema          string             `json:"schema"`
	Issues          []DatabaseIssueDTO `json:"issues"`
}

func runDatabase(ctx context.Context, config commandConfig, flags parsedFlags, out io.Writer) error {
	db, err := openStore(config)
	if err != nil {
		// Diagnostics errors are deliberately bounded: SQLite may echo local
		// paths or implementation details, neither of which is a CLI contract.
		return errors.New("open database for diagnostics")
	}
	defer db.Close()

	switch flags.dbAction {
	case "status":
		return runDatabaseStatus(ctx, db, config, out)
	case "check":
		return runDatabaseCheck(ctx, db, config, out)
	case "backup":
		return runDatabaseBackup(ctx, db, config, flags, out)
	default:
		return errors.New("unknown database action")
	}
}

func runDatabaseStatus(ctx context.Context, db *store.Store, config commandConfig, out io.Writer) error {
	status, err := db.DatabaseStatus(ctx)
	if err != nil {
		return errors.New("read database status")
	}
	document := databaseStatusDocument(status, config.now)
	if config.json {
		return writeDatabaseJSON(out, document)
	}
	size := "unknown"
	if document.SizeBytes != nil {
		size = strconv.FormatInt(*document.SizeBytes, 10)
	}
	fmt.Fprintf(out, "db status schema-current=%d schema-latest=%d size-bytes=%s usage-events=%d oldest-observed=%s latest-observed=%s integrity=not-checked\n",
		document.Schema.Current, document.Schema.Latest, size, document.UsageEventCount,
		statusTimestamp(document.OldestObservedAt), statusTimestamp(document.LatestObservedAt))
	return nil
}

func runDatabaseCheck(ctx context.Context, db *store.Store, config commandConfig, out io.Writer) error {
	check, err := db.CheckDatabase(ctx)
	if err != nil {
		return errors.New("run database integrity check")
	}
	document := databaseCheckDocument(check, config.now)
	if config.json {
		if err := writeDatabaseJSON(out, document); err != nil {
			return err
		}
	} else {
		issues := "none"
		if len(document.Issues) > 0 {
			checks := make([]string, 0, len(document.Issues))
			for _, issue := range document.Issues {
				checks = append(checks, issue.Check)
			}
			issues = strings.Join(checks, ",")
		}
		fmt.Fprintf(out, "db check healthy=%t quick-check=%s foreign-key-check=%s schema=%s issues=%s\n",
			document.Healthy, document.QuickCheck, document.ForeignKeyCheck, document.Schema, issues)
	}
	if !document.Healthy {
		return errors.New("database check found integrity issues")
	}
	return nil
}

func runDatabaseBackup(ctx context.Context, db *store.Store, config commandConfig, flags parsedFlags, out io.Writer) error {
	destination := ""
	if flags.outputSet {
		var err error
		destination, err = absolutePath(flags.output, config.currentDir)
		if err != nil || strings.TrimSpace(destination) == "" {
			return errors.New("resolve backup output")
		}
	} else {
		backupDir := filepath.Join(config.dataDir, "harness-lint", "backups")
		if err := os.MkdirAll(backupDir, 0o700); err != nil {
			return errors.New("create backup directory")
		}
		destination = nextBackupDestination(backupDir, config.now)
	}
	if err := db.Backup(ctx, destination); err != nil {
		switch {
		case errors.Is(err, store.ErrBackupDestinationExists):
			return errors.New("backup destination exists")
		case errors.Is(err, store.ErrBackupSourceDestinationSame):
			return errors.New("backup destination is the source database")
		case errors.Is(err, store.ErrBackupDestinationParent):
			return errors.New("backup destination parent is unavailable")
		default:
			return errors.New("create database backup")
		}
	}
	fmt.Fprintf(out, "db backup output=%s\n", destination)
	return nil
}

func nextBackupDestination(dir string, now time.Time) string {
	base := filepath.Join(dir, "harness-lint-"+now.UTC().Format("20060102T150405Z"))
	for index := 0; ; index++ {
		name := base + ".db"
		if index > 0 {
			name = base + "-" + strconv.Itoa(index) + ".db"
		}
		if _, err := os.Stat(name); errors.Is(err, os.ErrNotExist) {
			return name
		}
	}
}

func databaseStatusDocument(status store.DatabaseStatus, now time.Time) DatabaseStatusDocument {
	return DatabaseStatusDocument{
		SchemaVersion:    DatabaseStatusSchemaVersion,
		GeneratedAt:      now.UTC().Format(time.RFC3339Nano),
		Schema:           DatabaseSchemaDTO{Current: status.Schema.Current, Latest: status.Schema.Latest},
		SizeBytes:        status.SizeBytes,
		UsageEventCount:  status.UsageEventCount,
		OldestObservedAt: timestampPointer(status.OldestObservedAt),
		LatestObservedAt: timestampPointer(status.LatestObservedAt),
		IntegrityChecked: false,
	}
}

func databaseCheckDocument(check store.DatabaseCheck, now time.Time) DatabaseCheckDocument {
	issues := make([]DatabaseIssueDTO, 0, len(check.Issues))
	for _, issue := range check.Issues {
		issues = append(issues, DatabaseIssueDTO{Check: issue.Check})
	}
	return DatabaseCheckDocument{
		SchemaVersion:   DatabaseCheckSchemaVersion,
		GeneratedAt:     now.UTC().Format(time.RFC3339Nano),
		Healthy:         check.Healthy,
		QuickCheck:      string(check.QuickCheck),
		ForeignKeyCheck: string(check.ForeignKeyCheck),
		Schema:          string(check.Schema),
		Issues:          issues,
	}
}

func writeDatabaseJSON(out io.Writer, document any) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(document)
}

func timestampPointer(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339Nano)
	return &formatted
}

func statusTimestamp(value *string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return "never"
	}
	return *value
}
