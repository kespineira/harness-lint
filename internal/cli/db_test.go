package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/store"
)

func TestDatabaseHelpAndStatusJSONAreStableAndPrivate(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "state", "private-status-sentinel.db")
	options := databaseTestOptions(root)
	var stdout, stderr bytes.Buffer
	if err := ExecuteWithOptions(options, []string{"db", "--help"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("db help: %v", err)
	}
	for _, want := range []string{"db <status|check|backup>", "--json", "--output"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("db help = %q, missing %q", stdout.String(), want)
		}
	}
	stdout.Reset()
	if err := ExecuteWithOptions(options, []string{"db", "status", "--db", dbPath, "--json", "--now", "2026-08-14T12:00:00Z"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("empty status: %v", err)
	}
	var document DatabaseStatusDocument
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("status JSON: %v", err)
	}
	if document.SchemaVersion != DatabaseStatusSchemaVersion || document.UsageEventCount != 0 || document.IntegrityChecked || document.Schema.Current != document.Schema.Latest {
		t.Fatalf("empty status document = %#v", document)
	}
	if document.Path != dbPath {
		t.Fatalf("status path = %q, want %q", document.Path, dbPath)
	}
	if strings.Contains(stdout.String(), "session-private-sentinel") || strings.Contains(stdout.String(), "project-private-sentinel") || strings.Contains(stdout.String(), "PROMPT_SENTINEL") || strings.Contains(stderr.String(), "private-status-sentinel") {
		t.Fatalf("status leaked private sentinel: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestDatabaseStatusPopulatedAndCheckHealthy(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "state", "harness-lint.db")
	seedDatabase(t, dbPath, "private-event-sentinel")
	options := databaseTestOptions(root)
	var stdout, stderr bytes.Buffer
	if err := ExecuteWithOptions(options, []string{"db", "status", "--db", dbPath}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("populated status: %v", err)
	}
	if !strings.Contains(stdout.String(), "path="+dbPath) || !strings.Contains(stdout.String(), "usage-events=1") || !strings.Contains(stdout.String(), "integrity=not-checked") {
		t.Fatalf("populated status = %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "private-event-sentinel") || strings.Contains(stdout.String(), "session-private-sentinel") || strings.Contains(stdout.String(), "project-private-sentinel") || strings.Contains(stdout.String(), "PROMPT_SENTINEL") {
		t.Fatalf("status leaked event sentinel: %q", stdout.String())
	}
	stdout.Reset()
	if err := ExecuteWithOptions(options, []string{"db", "check", "--db", dbPath, "--json"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("healthy check: %v", err)
	}
	var document DatabaseCheckDocument
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("check JSON: %v", err)
	}
	if !document.Healthy || document.QuickCheck != "ok" || document.ForeignKeyCheck != "ok" || document.Schema != "ok" || len(document.Issues) != 0 {
		t.Fatalf("healthy check document = %#v", document)
	}
	if strings.Contains(stdout.String(), "private-event-sentinel") {
		t.Fatalf("check leaked event sentinel: %q", stdout.String())
	}
}

func TestDatabaseCheckErrorAndBackupOutputs(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "state", "source.db")
	seedDatabase(t, dbPath, "backup-private-sentinel")
	options := databaseTestOptions(root)
	xdgData := filepath.Join(root, "xdg-data")
	t.Setenv("XDG_DATA_HOME", xdgData)
	var stdout, stderr bytes.Buffer
	badDB := filepath.Join(root, "state", "invalid-private-sentinel.db")
	if err := os.WriteFile(badDB, []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ExecuteWithOptions(options, []string{"db", "check", "--db", badDB}, nil, &stdout, &stderr); err == nil {
		t.Fatal("invalid database check succeeded")
	}
	if strings.Contains(stdout.String()+stderr.String(), "invalid-private-sentinel") {
		t.Fatalf("check error leaked path sentinel: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	explicit := filepath.Join(root, "explicit-backup.db")
	stdout.Reset()
	if err := ExecuteWithOptions(options, []string{"db", "backup", "--db", dbPath, "--output", explicit}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("explicit backup: %v", err)
	}
	assertOpensAsStore(t, explicit, 1)
	explicitInfo, err := os.Stat(explicit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "size-bytes="+strconv.FormatInt(explicitInfo.Size(), 10)) {
		t.Fatalf("backup output = %q, want final size %d", stdout.String(), explicitInfo.Size())
	}
	if !strings.Contains(stdout.String(), explicit) {
		t.Fatalf("backup output = %q, want explicit destination", stdout.String())
	}
	before, err := os.ReadFile(explicit)
	if err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := ExecuteWithOptions(options, []string{"db", "backup", "--db", dbPath, "--output", explicit}, nil, &stdout, &stderr); err == nil {
		t.Fatal("backup collision succeeded")
	}
	after, err := os.ReadFile(explicit)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("backup collision changed destination")
	}

	stdout.Reset()
	if err := ExecuteWithOptions(options, []string{"db", "backup", "--db", dbPath}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("default backup: %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(root, "state", "backups", "*.db"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("default backups = %v, err=%v output=%q", backups, err, stdout.String())
	}
	assertOpensAsStore(t, backups[0], 1)
	defaultInfo, err := os.Stat(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "size-bytes="+strconv.FormatInt(defaultInfo.Size(), 10)) {
		t.Fatalf("default backup output = %q, want final size %d", stdout.String(), defaultInfo.Size())
	}
	stdout.Reset()
	if err := ExecuteWithOptions(options, []string{"db", "backup", "--db", dbPath}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("colliding default backup: %v", err)
	}
	if !strings.Contains(stdout.String(), filepath.Join(root, "state", "backups", "harness-lint-20260814T120000Z-1.db")) {
		t.Fatalf("colliding default backup output = %q", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(root, "home")); !os.IsNotExist(err) {
		t.Fatalf("backup mutated injected HOME: %v", err)
	}
	if _, err := os.Stat(xdgData); !os.IsNotExist(err) {
		t.Fatalf("backup mutated XDG data directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "data")); !os.IsNotExist(err) {
		t.Fatalf("backup created obsolete data directory: %v", err)
	}

	if err := ExecuteWithOptions(options, []string{"db", "backup", "--db", ":memory:"}, nil, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "filesystem database") {
		t.Fatalf("in-memory default backup error = %v", err)
	}
}

func databaseTestOptions(root string) Options {
	return Options{
		CWD:       root,
		ConfigDir: filepath.Join(root, "config"),
		Home:      filepath.Join(root, "home"),
		Now:       func() time.Time { return time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC) },
	}
}

func seedDatabase(t *testing.T, path, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create seed database parent: %v", err)
	}
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open seed database: %v", err)
	}
	event := domain.UsageEvent{
		ObservedAt:       time.Date(2026, 8, 14, 11, 0, 0, 0, time.UTC),
		Runtime:          domain.RuntimeCodex,
		SessionID:        "session-private-sentinel",
		ProjectID:        "project-private-sentinel",
		CapabilityType:   domain.CapabilityTool,
		CapabilityName:   name,
		EventType:        domain.EventInvoked,
		Provenance:       domain.ProvenanceImport,
		InvocationOrigin: domain.InvocationOriginUnknown,
		SchemaVersion:    domain.CurrentUsageEventSchemaVersion,
	}
	if err := db.InsertUsageEvents(context.Background(), []domain.UsageEvent{event}); err != nil {
		db.Close()
		t.Fatalf("seed database: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seed database: %v", err)
	}
}

func assertOpensAsStore(t *testing.T, path string, wantEvents int) {
	t.Helper()
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open backup %s: %v", path, err)
	}
	defer db.Close()
	status, err := db.DatabaseStatus(context.Background())
	if err != nil || status.UsageEventCount != int64(wantEvents) {
		t.Fatalf("backup status = %#v, err=%v", status, err)
	}
}
