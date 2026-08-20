package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/history"
	"github.com/kespineira/harness-lint/internal/hooks"
	"github.com/kespineira/harness-lint/internal/store"
)

func TestHooksUninstallClosesRuntimeCaptureEpochAfterMutation(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	claudeRoot := filepath.Join(root, "claude")
	base := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	now := base
	options := Options{
		CWD: root, ConfigDir: configDir, Now: func() time.Time { return now },
		LookPath: func(string) (string, error) { return "/usr/local/bin/harness-lint", nil },
	}
	execute := func(args []string, stdin string) error {
		var stdout, stderr bytes.Buffer
		return ExecuteWithOptions(options, args, strings.NewReader(stdin), &stdout, &stderr)
	}
	if err := execute([]string{"hooks", "install", "claude", "--claude-config", claudeRoot}, ""); err != nil {
		t.Fatalf("install: %v", err)
	}
	now = base.Add(time.Hour)
	payload := `{"hook_event_name":"PostToolUse","session_id":"s","cwd":"/project","tool_name":"Skill","tool_use_id":"delivery","tool_input":{"skill":"review"},"tool_response":{"text":"output"}}`
	dbPath := filepath.Join(configDir, "harness-lint", "harness-lint.db")
	if err := execute([]string{"ingest", "--runtime", "claude", "--event", "PostToolUse", "--db", dbPath}, payload); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	now = base.Add(2 * time.Hour)
	if err := execute([]string{"hooks", "uninstall", "claude", "--claude-config", claudeRoot}, ""); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	epochs, err := db.ListCaptureEpochs(context.Background(), domain.RuntimeClaudeCode)
	if err != nil || len(epochs) != 1 || !epochs[0].End.Equal(now) || epochs[0].EndReason != history.CaptureEndReasonManagedHookUninstall {
		t.Fatalf("uninstall lifecycle epochs = %#v, err=%v", epochs, err)
	}
	if _, err := os.Stat(filepath.Join(claudeRoot, "settings.json")); err != nil {
		t.Fatalf("uninstall removed config unexpectedly: %v", err)
	}
}

func TestHooksInstallAndReadOnlyStatusDoNotCreateCaptureEpochs(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	claudeRoot := filepath.Join(root, "claude")
	options := Options{
		CWD: root, ConfigDir: configDir, Now: func() time.Time { return time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC) },
		LookPath: func(string) (string, error) { return "/usr/local/bin/harness-lint", nil },
	}
	var stdout, stderr bytes.Buffer
	for _, args := range [][]string{
		{"hooks", "install", "claude", "--claude-config", claudeRoot},
		{"hooks", "install", "claude", "--claude-config", claudeRoot},
		{"hooks", "status", "claude", "--claude-config", claudeRoot},
	} {
		stdout.Reset()
		stderr.Reset()
		if err := ExecuteWithOptions(options, args, nil, &stdout, &stderr); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	dbPath := filepath.Join(configDir, "harness-lint", "harness-lint.db")
	if _, err := os.Stat(dbPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("install/status created lifecycle database: %v", err)
	}
}

func TestHooksInstallThenUninstallWithoutDatabaseLeavesDatabaseAbsent(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	claudeRoot := filepath.Join(root, "claude")
	options := Options{
		CWD: root, ConfigDir: configDir,
		LookPath: func(string) (string, error) { return "/usr/local/bin/harness-lint", nil },
		Now:      func() time.Time { return time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC) },
	}
	execute := func(args []string) error {
		var stdout, stderr bytes.Buffer
		return ExecuteWithOptions(options, args, nil, &stdout, &stderr)
	}
	if err := execute([]string{"hooks", "install", "claude", "--claude-config", claudeRoot}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := execute([]string{"hooks", "uninstall", "claude", "--claude-config", claudeRoot}); err != nil {
		t.Fatalf("uninstall without database: %v", err)
	}
	status, err := hooks.NewClaude(hooks.Options{
		ConfigRoot: claudeRoot,
		LookPath:   options.LookPath,
	}).Status(context.Background())
	if err != nil || status.Code != hooks.StatusNotInstalled {
		t.Fatalf("managed hooks remain installed: status=%#v err=%v", status, err)
	}
	dbPath := filepath.Join(configDir, "harness-lint", "harness-lint.db")
	if _, err := os.Stat(dbPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("uninstall created lifecycle database: %v", err)
	}
}

func TestHooksUninstallLifecycleFailureDoesNotUndoConfigMutation(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config-file")
	if err := os.WriteFile(configDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	claudeRoot := filepath.Join(root, "claude")
	lookPath := func(string) (string, error) { return "/usr/local/bin/harness-lint", nil }
	if _, err := hooks.NewClaude(hooks.Options{ConfigRoot: claudeRoot, LookPath: lookPath}).Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	options := Options{CWD: root, ConfigDir: configDir, LookPath: lookPath, Now: func() time.Time { return time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC) }}
	var stdout, stderr bytes.Buffer
	err := ExecuteWithOptions(options, []string{"hooks", "uninstall", "claude", "--claude-config", claudeRoot}, nil, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "capture lifecycle") || !strings.Contains(stdout.String(), "Capture lifecycle close failed") {
		t.Fatalf("uninstall lifecycle failure err=%v stdout=%q", err, stdout.String())
	}
	status, statusErr := hooks.NewClaude(hooks.Options{ConfigRoot: claudeRoot, LookPath: lookPath}).Status(context.Background())
	if statusErr != nil || status.Code != hooks.StatusNotInstalled {
		t.Fatalf("config mutation was undone after lifecycle failure: status=%#v err=%v", status, statusErr)
	}
}
