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

	"github.com/kespineira/harness-lint/internal/presentation"
	"github.com/kespineira/harness-lint/internal/store"
)

// DatabaseStatusSchemaVersion is the public JSON schema version for db
// status. The DTO includes only diagnostic metadata and omits all event
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
	Path             string            `json:"path"`
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
	renderDatabaseStatusView(out, config.renderer, config.verbose, document)
	return nil
}

func runDatabaseCheck(ctx context.Context, db *store.Store, config commandConfig, out io.Writer) error {
	check, err := db.CheckDatabase(ctx)
	if err != nil {
		if !config.json {
			fmt.Fprintln(out, "Database check")
			fmt.Fprintln(out)
			fmt.Fprintf(out, "  %s\n", config.renderer.Status("unavailable"))
			fmt.Fprintln(out)
			fmt.Fprintln(out, "Database could not be checked.")
		}
		return errors.New("run database integrity check")
	}
	document := databaseCheckDocument(check, config.now)
	if config.json {
		if err := writeDatabaseJSON(out, document); err != nil {
			return err
		}
	} else {
		renderDatabaseCheckView(out, config.renderer, config.verbose, document)
	}
	if !document.Healthy {
		return errors.New("database check found integrity issues")
	}
	return nil
}

func runDatabaseBackup(ctx context.Context, db *store.Store, config commandConfig, flags parsedFlags, out io.Writer) error {
	if flags.outputSet {
		var err error
		destination, err := absolutePath(flags.output, config.currentDir)
		if err != nil || strings.TrimSpace(destination) == "" {
			return errors.New("resolve backup output")
		}
		if err := db.Backup(ctx, destination); err != nil {
			return databaseBackupError(err)
		}
		return writeDatabaseBackupResultWithRenderer(out, config.renderer, config.verbose, destination)
	}

	if !isFilesystemDatabase(config.dbPath) {
		return errors.New("default backup requires a filesystem database")
	}
	backupDir := filepath.Join(filepath.Dir(config.dbPath), "backups")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return errors.New("create backup directory")
	}
	base := filepath.Join(backupDir, "harness-lint-"+config.now.UTC().Format("20060102T150405Z"))
	for attempt := 0; attempt < maxDefaultBackupAttempts; attempt++ {
		destination := base + ".db"
		if attempt > 0 {
			destination = base + "-" + strconv.Itoa(attempt) + ".db"
		}
		if err := db.Backup(ctx, destination); err != nil {
			if errors.Is(err, store.ErrBackupDestinationExists) {
				continue
			}
			return databaseBackupError(err)
		}
		return writeDatabaseBackupResultWithRenderer(out, config.renderer, config.verbose, destination)
	}
	return errors.New("too many database backup destinations")
}

const maxDefaultBackupAttempts = 100

func databaseBackupError(err error) error {
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

func writeDatabaseBackupResultWithRenderer(out io.Writer, renderer presentation.HumanRenderer, verbose bool, destination string) error {
	info, err := os.Stat(destination)
	if err != nil {
		return errors.New("read database backup size")
	}
	renderDatabaseBackupView(out, renderer, verbose, destination, info.Size())
	return nil
}

func isFilesystemDatabase(path string) bool {
	return path != ":memory:" && !strings.HasPrefix(path, "file:")
}

func databaseStatusDocument(status store.DatabaseStatus, now time.Time) DatabaseStatusDocument {
	return DatabaseStatusDocument{
		SchemaVersion:    DatabaseStatusSchemaVersion,
		GeneratedAt:      now.UTC().Format(time.RFC3339Nano),
		Path:             status.Path,
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
