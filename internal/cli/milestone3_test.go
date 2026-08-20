package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/capture"
	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/hooks"
	"github.com/kespineira/harness-lint/internal/store"
	usagedto "github.com/kespineira/harness-lint/internal/usage"
)

func TestExecuteUsageJSONFiltersMCPAndFillsMonthlyEvidence(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "state", "harness-lint.db")
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	capability := func(typ domain.CapabilityType, name string) domain.Capability {
		return domain.Capability{
			Runtime:        domain.RuntimeCodex,
			Type:           typ,
			Name:           name,
			Scope:          domain.ScopeUser,
			Source:         filepath.Join(root, "private", name),
			Enabled:        domain.EnabledStateEnabled,
			Advertisement:  domain.AdvertisementStateUnknown,
			MetadataTokens: domain.Measurement{Confidence: domain.ConfidenceUnknown},
			BodyTokens:     domain.Measurement{Confidence: domain.ConfidenceUnknown},
		}
	}
	if err := db.RecordInventory(context.Background(), domain.RuntimeCodex, now, []domain.Capability{
		capability(domain.CapabilityMCPServer, "server"),
		capability(domain.CapabilityMCPTool, "remote-tool"),
		capability(domain.CapabilityTool, "local-tool"),
	}); err != nil {
		t.Fatal(err)
	}
	event := func(typ domain.CapabilityType, name string, at time.Time, identity string) domain.UsageEvent {
		return domain.UsageEvent{
			ObservedAt:       at,
			Runtime:          domain.RuntimeCodex,
			SessionID:        identity,
			ProjectID:        "project-" + identity,
			CapabilityType:   typ,
			CapabilityName:   name,
			EventType:        domain.EventInvoked,
			Provenance:       domain.ProvenanceImport,
			InvocationOrigin: domain.InvocationOriginUnknown,
			SchemaVersion:    domain.CurrentUsageEventSchemaVersion,
			SourceIdentity:   identity,
		}
	}
	events := []domain.UsageEvent{
		event(domain.CapabilityMCPServer, "server", now.Add(-60*24*time.Hour), "boundary"),
		event(domain.CapabilityMCPTool, "remote-tool", now.Add(-2*24*time.Hour), "recent"),
		event(domain.CapabilityTool, "local-tool", now.Add(-time.Hour), "local"),
		event(domain.CapabilityMCPServer, "server", now.Add(-91*24*time.Hour), "outside"),
	}
	if err := db.InsertUsageEvents(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err = ExecuteWithOptions(Options{Home: filepath.Join(root, "home"), CWD: root, ProjectRoot: root, Now: func() time.Time { return now }}, []string{
		"usage", "--json", "--db", dbPath, "--days", "90", "--runtime", "codex", "--type", "mcp", "--monthly",
	}, nil, &stdout, &stderr)
	if err != nil {
		t.Fatalf("usage error = %v\nstderr=%s", err, stderr.String())
	}
	var document usagedto.UsageDocument
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("decode usage JSON: %v\n%s", err, stdout.String())
	}
	if document.SchemaVersion != usagedto.SchemaVersion || document.Period.Days != 90 || !document.Period.Inclusive {
		t.Fatalf("usage envelope = %#v", document)
	}
	if document.Filters.Runtime == nil || *document.Filters.Runtime != "codex" || document.Filters.Type == nil || *document.Filters.Type != "mcp" {
		t.Fatalf("usage filters = %#v", document.Filters)
	}
	if len(document.Capabilities) != 2 {
		t.Fatalf("MCP union rows = %#v", document.Capabilities)
	}
	for _, row := range document.Capabilities {
		if row.Type != string(domain.CapabilityMCPServer) && row.Type != string(domain.CapabilityMCPTool) {
			t.Fatalf("MCP filter returned non-MCP row = %#v", row)
		}
		if len(row.Monthly) != 4 {
			t.Fatalf("monthly buckets for %s = %#v", row.Name, row.Monthly)
		}
	}
	for _, forbidden := range []string{root, "project-boundary", "fingerprint", "source_identity"} {
		if strings.Contains(stdout.String(), forbidden) {
			t.Fatalf("usage output leaked %q: %s", forbidden, stdout.String())
		}
	}
}

func TestExecuteHooksTestIsReadOnlyAndReportsDeliveryState(t *testing.T) {
	root := t.TempDir()
	claudeRoot := filepath.Join(root, "claude")
	codexRoot := filepath.Join(root, "codex")
	dbPath := filepath.Join(root, "state", "harness-lint.db")
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	lookPath := func(string) (string, error) { return "/usr/local/bin/harness-lint", nil }
	if _, err := hooks.NewClaude(hooks.Options{ConfigRoot: claudeRoot, LookPath: lookPath}).Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := hooks.NewCodex(hooks.Options{ConfigRoot: codexRoot, LookPath: lookPath}).Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	initializeTestStore(t, dbPath)
	var stdout, stderr bytes.Buffer
	options := Options{CWD: root, ProjectRoot: root, Now: func() time.Time { return now }, LookPath: lookPath}
	args := []string{"hooks", "test", "--db", dbPath, "--claude-config", claudeRoot, "--codex-home", codexRoot}
	if err := ExecuteWithOptions(options, args, nil, &stdout, &stderr); err != nil {
		t.Fatalf("hooks test idle error = %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Hook health") || !strings.Contains(stdout.String(), "Claude Code  - Idle") || !strings.Contains(stdout.String(), "Codex        - Idle") || !strings.Contains(stdout.String(), "0/2 runtimes healthy · 2 idle") {
		t.Fatalf("hooks test output = %q", stdout.String())
	}
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	events, err := db.ListUsageEvents(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("hooks test self-test polluted usage history = %#v", events)
	}
	if err := db.RecordCaptureFailure(context.Background(), capture.CaptureFailure{Runtime: domain.RuntimeCodex, FailedAt: now, Kind: capture.FailureDatabaseBusy}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	err = ExecuteWithOptions(options, append(append([]string(nil), args...), "--verbose"), nil, &stdout, &stderr)
	if err == nil || err.Error() != hookTestFailureMessage {
		t.Fatalf("hooks test degraded error = %v", err)
	}
	if !strings.Contains(stdout.String(), "! Degraded") || !strings.Contains(stdout.String(), "1 recent delivery failure") || !strings.Contains(stdout.String(), "Failure kind") || !strings.Contains(stdout.String(), "database_busy") || !strings.Contains(stdout.String(), "Last failure") {
		t.Fatalf("hooks test degraded output = %q", stdout.String())
	}
	stdout.Reset()
	err = ExecuteWithOptions(options, []string{"hooks", "test", "--json", "--db", dbPath, "--claude-config", claudeRoot, "--codex-home", codexRoot}, nil, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "does not support --json") || stdout.Len() != 0 {
		t.Fatalf("hooks test JSON behavior error=%v output=%q", err, stdout.String())
	}
}

func TestExecuteUsageNoHistoryCurrentCapabilityUsesObservationWording(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "empty.db")
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	capability := domain.Capability{
		Runtime:        domain.RuntimeClaudeCode,
		Type:           domain.CapabilitySkill,
		Name:           "unused",
		Scope:          domain.ScopeUser,
		Enabled:        domain.EnabledStateEnabled,
		Advertisement:  domain.AdvertisementStateUnknown,
		MetadataTokens: domain.Measurement{Confidence: domain.ConfidenceUnknown},
		BodyTokens:     domain.Measurement{Confidence: domain.ConfidenceUnknown},
	}
	if err := db.RecordInventory(context.Background(), domain.RuntimeClaudeCode, now, []domain.Capability{capability}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := ExecuteWithOptions(Options{Home: filepath.Join(root, "home"), CWD: root, ProjectRoot: root, Now: func() time.Time { return now }}, []string{"usage", "--db", dbPath, "--runtime", "claude-code"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("usage no-history error = %v", err)
	}
	if !strings.Contains(stdout.String(), "No capability invocations were observed in this period.") || strings.Contains(stdout.String(), "lifetime non-use") || strings.Contains(stdout.String(), "complete coverage") {
		t.Fatalf("usage no-history wording = %q", stdout.String())
	}
}

func TestUsageAndHooksTestRelativeDatabasePathsUseInjectedCWD(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	options := Options{CWD: root, Now: func() time.Time { return now }}
	hooksConfig, err := resolveHooksTestConfig(options, parsedFlags{
		dbSet:        true,
		dbPath:       filepath.Join("state", "hooks.db"),
		claudeSet:    true,
		claudeConfig: filepath.Join(root, "claude"),
		codexSet:     true,
		codexHome:    filepath.Join(root, "codex"),
	})
	if err != nil {
		t.Fatalf("resolveHooksTestConfig() error = %v", err)
	}
	if got, want := hooksConfig.dbPath, filepath.Join(root, "state", "hooks.db"); got != want {
		t.Fatalf("hooks test relative db = %q, want %q", got, want)
	}
	usageConfig, err := resolveUsageConfig(options, parsedFlags{dbSet: true, dbPath: filepath.Join("state", "usage.db"), days: 90})
	if err != nil {
		t.Fatalf("resolveUsageConfig() error = %v", err)
	}
	if got, want := usageConfig.dbPath, filepath.Join(root, "state", "usage.db"); got != want {
		t.Fatalf("usage relative db = %q, want %q", got, want)
	}
	if _, err := resolveHooksTestConfig(options, parsedFlags{dbSet: true, dbPath: ""}); err == nil {
		t.Fatal("empty explicit hooks-test db unexpectedly accepted")
	}
}

func TestUsageRejectsRuntimeConfigurationFlags(t *testing.T) {
	checks := []struct {
		name string
		set  func(*parsedFlags)
	}{
		{name: "home", set: func(flags *parsedFlags) { flags.homeSet = true }},
		{name: "project", set: func(flags *parsedFlags) { flags.projectSet = true }},
		{name: "codex-home", set: func(flags *parsedFlags) { flags.codexSet = true }},
		{name: "claude-config", set: func(flags *parsedFlags) { flags.claudeSet = true }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			flags := parsedFlags{days: 90}
			check.set(&flags)
			if err := validateCommandFlags("usage", flags); err == nil {
				t.Fatalf("validateCommandFlags() accepted --%s", check.name)
			}
		})
	}
}
