package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/hooks"
	"github.com/kespineira/harness-lint/internal/presentation"
)

func TestM7ReadOnlyCommandsDoNotCreateMissingDatabase(t *testing.T) {
	commands := []struct {
		name string
		args func(string) []string
	}{
		{name: "database status", args: func(path string) []string { return []string{"db", "status", "--db", path} }},
		{name: "database check", args: func(path string) []string { return []string{"db", "check", "--db", path} }},
		{name: "database backup", args: func(path string) []string { return []string{"db", "backup", "--db", path} }},
		{name: "hooks test", args: func(path string) []string { return []string{"hooks", "test", "claude", "--db", path} }},
		{name: "report", args: func(path string) []string { return []string{"report", "--db", path} }},
		{name: "stale", args: func(path string) []string { return []string{"stale", "--db", path} }},
		{name: "context", args: func(path string) []string { return []string{"context", "--db", path} }},
		{name: "usage", args: func(path string) []string { return []string{"usage", "--db", path} }},
		{name: "explain", args: func(path string) []string { return []string{"explain", "missing", "--db", path} }},
	}
	for _, test := range commands {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			dbPath := filepath.Join(root, "missing", "state.db")
			_, _, err := m7Execute(t, m7CLIOptions(root, func(string) (string, error) { return "", os.ErrNotExist }), test.args(dbPath), nil)
			if err == nil {
				t.Fatalf("%s unexpectedly succeeded", test.name)
			}
			if !errors.Is(err, errDatabaseNotInitialized) && !strings.Contains(err.Error(), "initialized database") {
				t.Fatalf("%s error = %v, want initialization guidance", test.name, err)
			}
			if _, statErr := os.Stat(dbPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("%s created missing database: %v", test.name, statErr)
			}
		})
	}
}

func TestM7LongPathsAndExplainIdentityStayWithinEightyColumns(t *testing.T) {
	root := t.TempDir()
	longSegment := strings.Repeat("x", 110)
	longRoot := filepath.Join(root, longSegment)
	dbPath := filepath.Join(longRoot, "state.db")
	initializeTestStore(t, dbPath)
	options := m7CLIOptions(root, func(string) (string, error) { return "/opt/harness-lint", nil })

	status := m7RequireExecute(t, options, []string{"db", "status", "--db", dbPath, "--color", "never"}, nil)
	assertM7Width(t, "database status", status)
	assertM7TextPreserved(t, "database status path", status, dbPath)

	backupPath := filepath.Join(root, strings.Repeat("y", 110)+".db")
	backup := m7RequireExecute(t, options, []string{"db", "backup", "--db", dbPath, "--output", backupPath, "--color", "never"}, nil)
	assertM7Width(t, "database backup", backup)
	assertM7TextPreserved(t, "database backup destination", backup, backupPath)

	hookRoot := filepath.Join(root, strings.Repeat("h", 110))
	manager := hooks.NewClaude(hooks.Options{ConfigRoot: hookRoot, LookPath: func(string) (string, error) { return "/opt/harness-lint", nil }})
	if _, err := manager.Install(context.Background()); err != nil {
		t.Fatalf("install long-path hook fixture: %v", err)
	}
	hookStatus := m7RequireExecute(t, options, []string{"hooks", "status", "claude", "--claude-config", hookRoot, "--verbose", "--color", "never"}, nil)
	assertM7Width(t, "hook status", hookStatus)
	assertM7TextPreserved(t, "hook status path", hookStatus, filepath.Join(hookRoot, "settings.json"))

	dryRunRoot := filepath.Join(root, strings.Repeat("d", 110))
	dryRun := m7RequireExecute(t, options, []string{"hooks", "install", "claude", "--claude-config", dryRunRoot, "--dry-run", "--color", "never"}, nil)
	assertM7Width(t, "hook install dry run", dryRun)
	assertM7TextPreserved(t, "hook dry-run path", dryRun, filepath.Join(dryRunRoot, "settings.json"))

	name := strings.Repeat("long-capability-", 8)
	capability := m7Skill(name, domain.RuntimeCodex, domain.ScopeUser, root, domain.AdvertisementStateUnknown, m7UnknownMeasurement(), m7UnknownMeasurement())
	explainDB := filepath.Join(root, "explain.db")
	m7SeedInventory(t, explainDB, m7FixedNow(), []domain.Capability{capability})
	explain := m7RequireExecute(t, options, []string{"explain", name, "--db", explainDB, "--now", m7ExecuteNow, "--color", "never"}, nil)
	assertM7Width(t, "explain", explain)
	assertM7TextPreserved(t, "explain identity", explain, name)
}

func TestM7OneDayPeriodsUseSingularLanguage(t *testing.T) {
	root := t.TempDir()
	options := m7CLIOptions(root, func(string) (string, error) { return "", os.ErrNotExist })
	for _, args := range [][]string{
		{"usage", "--db", ":memory:", "--days", "1", "--color", "never"},
		{"stale", "--db", ":memory:", "--days", "1", "--color", "never"},
		{"report", "--db", ":memory:", "--days", "1", "--color", "never"},
	} {
		output := m7RequireExecute(t, options, args, nil)
		if !strings.Contains(output, "1 day") || strings.Contains(output, "1 days") {
			t.Fatalf("%v did not use singular day language: %s", args, output)
		}
	}
}

func assertM7Width(t *testing.T, label, output string) {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if width := presentation.VisibleWidth(line); width > presentation.DefaultWidth {
			t.Fatalf("%s line is %d columns: %q", label, width, line)
		}
	}
}

func assertM7TextPreserved(t *testing.T, label, output, value string) {
	t.Helper()
	compactOutput := strings.Join(strings.Fields(output), "")
	compactValue := strings.Join(strings.Fields(value), "")
	if !strings.Contains(compactOutput, compactValue) {
		t.Fatalf("%s lost value %q: %s", label, value, output)
	}
}
