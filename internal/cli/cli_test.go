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
	runtimepkg "github.com/kespineira/harness-lint/internal/runtime"
	"github.com/kespineira/harness-lint/internal/store"
)

func TestExecuteScanAndReportTracer(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	project := filepath.Join(root, "project")
	hook := filepath.Join(root, "hook.jsonl")
	db := filepath.Join(root, "state", "harness-lint.db")
	if err := os.MkdirAll(filepath.Join(home, ".agents", "skills", "lint"), 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".agents", "skills", "lint", "SKILL.md"), []byte("---\nname: lint\ndescription: lint files\n---\nRun lint.\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	now := time.Date(2026, 8, 13, 15, 0, 0, 0, time.UTC)
	if err := os.WriteFile(hook, []byte(`{"hook_event_name":"PostToolUse","timestamp":"2026-08-13T14:30:00Z","session_id":"session","cwd":"`+project+`","tool_name":"Bash"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	options := Options{Home: home, CWD: project, ProjectRoot: project, Now: func() time.Time { return now }}
	var stdout, stderr bytes.Buffer
	if err := ExecuteWithOptions(options, []string{"scan", "--db", db, "--home", home, "--project", project, "--hook-capture", hook, "--since", "2026-08-13T14:30:00Z"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("scan error = %v\nstderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Scan complete") || !strings.Contains(stdout.String(), "Codex") || !strings.Contains(stdout.String(), "1 capabilities discovered") {
		t.Fatalf("scan output = %q, want codex capability total", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if err := ExecuteWithOptions(options, []string{"report", "--db", db, "--home", home, "--project", project}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("report error = %v\nstderr=%s", err, stderr.String())
	}
	report := stdout.String()
	if !strings.Contains(report, "Runtime overview") || !strings.Contains(report, "advertised ·") || !strings.Contains(report, "loaded ·") || !strings.Contains(report, "invoked") {
		t.Fatalf("report output = %q, want structured runtime and totals sections", report)
	}
	if !strings.Contains(report, "usage-only") {
		t.Fatalf("report output = %q, want usage-only summary", report)
	}
}

func TestExecuteScanIsIdempotentAndEmptySnapshotPreservesHistory(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	project := filepath.Join(root, "project")
	emptyHome := filepath.Join(root, "empty-home")
	emptyProject := filepath.Join(root, "empty-project")
	hook := filepath.Join(root, "hook.jsonl")
	db := filepath.Join(root, "state", "harness-lint.db")
	if err := os.MkdirAll(filepath.Join(home, ".agents", "skills", "lint"), 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := os.MkdirAll(emptyHome, 0o755); err != nil {
		t.Fatalf("mkdir empty home: %v", err)
	}
	if err := os.MkdirAll(emptyProject, 0o755); err != nil {
		t.Fatalf("mkdir empty project: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".agents", "skills", "lint", "SKILL.md"), []byte("---\nname: lint\ndescription: lint files\n---\nRun lint.\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	now := time.Date(2026, 8, 13, 15, 0, 0, 0, time.UTC)
	if err := os.WriteFile(hook, []byte(`{"hook_event_name":"PostToolUse","timestamp":"2026-08-13T14:30:00Z","session_id":"session","cwd":"`+project+`","tool_name":"Bash"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	options := Options{Home: home, CWD: project, ProjectRoot: project, Now: func() time.Time { return now }}
	scan := func(home, project string, withHook bool) string {
		t.Helper()
		args := []string{"scan", "--db", db, "--home", home, "--project", project}
		if withHook {
			args = append(args, "--hook-capture", hook)
		}
		var stdout, stderr bytes.Buffer
		if err := ExecuteWithOptions(options, args, nil, &stdout, &stderr); err != nil {
			t.Fatalf("scan error = %v\nstderr=%s", err, stderr.String())
		}
		return stdout.String()
	}
	first := scan(home, project, true)
	second := scan(home, project, true)
	if first != second {
		t.Fatalf("repeated scan output differs:\nfirst=%s\nsecond=%s", first, second)
	}

	var stdout, stderr bytes.Buffer
	if err := ExecuteWithOptions(options, []string{"scan", "--db", db, "--home", emptyHome, "--project", emptyProject}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("empty scan error = %v\nstderr=%s", err, stderr.String())
	}
	historicalStore, err := store.Open(db)
	if err != nil {
		t.Fatalf("open history store: %v", err)
	}
	historical, err := historicalStore.ListCapabilities(context.Background())
	if err != nil {
		t.Fatalf("list historical capabilities: %v", err)
	}
	if err := historicalStore.Close(); err != nil {
		t.Fatalf("close history store: %v", err)
	}
	if len(historical) != 1 || historical[0].Name != "lint" {
		t.Fatalf("historical capabilities after empty scan = %#v, want prior lint row preserved", historical)
	}
	stdout.Reset()
	stderr.Reset()
	if err := ExecuteWithOptions(options, []string{"report", "--db", db, "--home", emptyHome, "--project", emptyProject}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("report after empty scan error = %v\nstderr=%s", err, stderr.String())
	}
	report := stdout.String()
	if !strings.Contains(report, "Codex") || !strings.Contains(report, "Claude Code") || !strings.Contains(report, "Runtime overview") {
		t.Fatalf("report after empty scan = %q, want empty current inventories", report)
	}
	if !strings.Contains(report, "usage-only") {
		t.Fatalf("report after empty scan = %q, want preserved usage history", report)
	}
}

func TestExecuteStaleUsesStrictDaysBoundaryAndEvidenceStatuses(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "state", "harness-lint.db")
	now := time.Date(2026, 8, 13, 15, 0, 0, 0, time.UTC)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatalf("mkdir database parent: %v", err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	capability := func(name string, metadata domain.Measurement) domain.Capability {
		return domain.Capability{
			Runtime:        domain.RuntimeCodex,
			Type:           domain.CapabilitySkill,
			Name:           name,
			Scope:          domain.ScopeProject,
			Source:         filepath.Join(root, name, "SKILL.md"),
			Enabled:        domain.EnabledStateEnabled,
			Advertisement:  domain.AdvertisementStateFullyAdvertised,
			MetadataTokens: metadata,
			BodyTokens:     domain.Measurement{Confidence: domain.ConfidenceUnknown},
		}
	}
	caps := []domain.Capability{
		capability("dead", domain.Measurement{Confidence: domain.ConfidenceUnknown}),
		capability("stale", domain.Measurement{Confidence: domain.ConfidenceUnknown}),
		capability("boundary", domain.Measurement{Confidence: domain.ConfidenceUnknown}),
		capability("future", domain.Measurement{Confidence: domain.ConfidenceUnknown}),
		capability("review", domain.Measurement{Value: 1200, Confidence: domain.ConfidenceEstimated, Basis: "fixture"}),
		capability("keep", domain.Measurement{Value: 10, Confidence: domain.ConfidenceExact, Basis: "fixture"}),
	}
	if err := db.RecordInventory(context.Background(), domain.RuntimeCodex, now, caps); err != nil {
		t.Fatalf("record inventory: %v", err)
	}
	event := func(name string, timestamp time.Time, eventType domain.EventType, fingerprint string) domain.UsageEvent {
		return domain.UsageEvent{
			ObservedAt:       timestamp,
			Runtime:          domain.RuntimeCodex,
			SessionID:        "session-" + name,
			ProjectID:        "project",
			CapabilityType:   domain.CapabilitySkill,
			CapabilityName:   name,
			EventType:        eventType,
			Provenance:       domain.ProvenanceImport,
			InvocationOrigin: domain.InvocationOriginUnknown,
			SchemaVersion:    domain.CurrentUsageEventSchemaVersion,
			Fingerprint:      fingerprint,
		}
	}
	events := []domain.UsageEvent{
		event("stale", now.Add(-61*24*time.Hour), domain.EventInvoked, "stale-event"),
		event("boundary", now.Add(-60*24*time.Hour), domain.EventInvoked, "boundary-event"),
		event("future", now.Add(time.Hour), domain.EventInvoked, "future-event"),
		event("review", now.Add(-24*time.Hour), domain.EventInvoked, "review-event"),
		event("keep", now.Add(-24*time.Hour), domain.EventLoaded, "keep-event"),
	}
	if err := db.InsertUsageEvents(context.Background(), events); err != nil {
		t.Fatalf("insert usage: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := ExecuteWithOptions(Options{Home: filepath.Join(root, "home"), CWD: root, ProjectRoot: root, Now: func() time.Time { return now }}, []string{"stale", "--db", dbPath, "--days", "60"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("stale error = %v\nstderr=%s", err, stderr.String())
	}
	output := stdout.String()
	for _, want := range []string{"Stale capabilities", "Threshold: 60 days", "Stale", "stale"} {
		if !strings.Contains(output, want) {
			t.Fatalf("stale output = %q, missing %q", output, want)
		}
	}
	for _, forbidden := range []string{"dead", "boundary", "future", "review", "keep", "="} {
		if strings.Contains(strings.ToLower(output), forbidden) {
			t.Fatalf("stale output includes non-stale or legacy detail %q: %q", forbidden, output)
		}
	}
	if strings.Contains(strings.ToLower(output), "score") {
		t.Fatalf("stale output = %q, must not contain numeric scores", output)
	}
}

func TestHomeOverrideDoesNotChangeConfiguredDefaultDatabase(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	home := filepath.Join(root, "synthetic-home")
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if err := ExecuteWithOptions(Options{
		Home:        home,
		CWD:         project,
		ProjectRoot: project,
		ConfigDir:   configDir,
		Now:         func() time.Time { return time.Date(2026, 8, 13, 15, 0, 0, 0, time.UTC) },
	}, []string{"scan", "--project", project}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("scan error = %v\nstderr=%s", err, stderr.String())
	}
	defaultDB := filepath.Join(configDir, "harness-lint", "harness-lint.db")
	if _, err := os.Stat(defaultDB); err != nil {
		t.Fatalf("configured default database stat: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "harness-lint", "harness-lint.db")); !os.IsNotExist(err) {
		t.Fatalf("home override unexpectedly changed database location; stat error=%v", err)
	}
}

func TestExecuteContextLabelsBaselineOnLoadConfidenceAndPartialTotals(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "harness-lint.db")
	now := time.Date(2026, 8, 13, 15, 0, 0, 0, time.UTC)
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	capability := func(typ domain.CapabilityType, name string) domain.Capability {
		return domain.Capability{
			Runtime:        domain.RuntimeCodex,
			Type:           typ,
			Name:           name,
			Scope:          domain.ScopeProject,
			Source:         filepath.Join(root, name),
			Enabled:        domain.EnabledStateEnabled,
			Advertisement:  domain.AdvertisementStateFullyAdvertised,
			MetadataTokens: domain.Measurement{Confidence: domain.ConfidenceUnknown},
			BodyTokens:     domain.Measurement{Confidence: domain.ConfidenceUnknown},
		}
	}
	skillKnown := capability(domain.CapabilitySkill, "known")
	skillKnown.MetadataTokens = domain.Measurement{Value: 10, Confidence: domain.ConfidenceExact, Basis: "fixture metadata"}
	skillKnown.BodyTokens = domain.Measurement{Value: 20, Confidence: domain.ConfidenceEstimated, Basis: "skill on-load estimate"}
	skillUnknown := capability(domain.CapabilitySkill, "unknown")
	instruction := capability(domain.CapabilityInstructionFile, "AGENTS.md")
	instruction.BodyTokens = domain.Measurement{Value: 4, Confidence: domain.ConfidenceObserved, Basis: "effective instruction"}
	agent := capability(domain.CapabilityAgent, "reviewer")
	agent.MetadataTokens = domain.Measurement{Value: 5, Confidence: domain.ConfidenceExact, Basis: "fixture metadata"}
	agent.BodyTokens = domain.Measurement{Value: 6, Confidence: domain.ConfidenceEstimated, Basis: "agent on-load estimate"}
	mcp := capability(domain.CapabilityMCPServer, "remote")
	if err := db.RecordInventory(context.Background(), domain.RuntimeCodex, now, []domain.Capability{skillKnown, skillUnknown, instruction, agent, mcp}); err != nil {
		t.Fatalf("record inventory: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := ExecuteWithOptions(Options{Home: filepath.Join(root, "home"), CWD: root, ProjectRoot: root, Now: func() time.Time { return now }}, []string{"context", "--db", dbPath}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("context error = %v\nstderr=%s", err, stderr.String())
	}
	output := stdout.String()
	for _, want := range []string{
		"Context footprint",
		"Configured baseline metadata",
		"On-load body estimate",
		"Configured baseline body",
		"Exact",
		"Estimated",
		"Observed",
		"unknown",
		"partial",
		"MCP schema cost is unknown",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("context output = %q, missing %q", output, want)
		}
	}
	if strings.Contains(output, "remote") && !strings.Contains(output, "MCP server") {
		t.Fatalf("context output = %q, MCP semantics not explicit", output)
	}
	if strings.Contains(output, "runtime=") || strings.Contains(output, "type=") {
		t.Fatalf("context output retained legacy key-value serialization: %q", output)
	}
}

func TestExecuteDoctorShowsDuplicateMalformedAndUnresolvedFindingsWithoutDatabase(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	project := filepath.Join(root, "project")
	dbPath := filepath.Join(root, "must-not-be-created", "harness-lint.db")
	for _, path := range []string{
		filepath.Join(home, ".agents", "skills", "shared"),
		filepath.Join(project, ".agents", "skills", "shared"),
		filepath.Join(home, ".codex", "agents"),
		filepath.Join(home, ".codex"),
		filepath.Join(project, ".codex"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".agents", "skills", "shared", "SKILL.md"), []byte("---\nname: shared\ndescription: user\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write user skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(project, ".agents", "skills", "shared", "SKILL.md"), []byte("---\nname: shared\ndescription: project\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("write project skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("[mcp_servers.broken]\ncommand = \"definitely-not-installed\"\n"), 0o644); err != nil {
		t.Fatalf("write user Codex config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(project, ".codex", "config.toml"), []byte("[mcp_servers.project]\ncommand = \"definitely-not-installed\"\n"), 0o644); err != nil {
		t.Fatalf("write project Codex config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "agents", "bad.toml"), []byte("[broken\n"), 0o644); err != nil {
		t.Fatalf("write malformed agent: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := ExecuteWithOptions(Options{
		Home:        home,
		CWD:         project,
		ProjectRoot: project,
		Now:         func() time.Time { return time.Date(2026, 8, 13, 15, 0, 0, 0, time.UTC) },
		LookPath:    func(string) (string, error) { return "", os.ErrNotExist },
	}, []string{"doctor", "--db", dbPath}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("doctor error = %v\nstderr=%s", err, stderr.String())
	}
	output := stdout.String()
	for _, want := range []string{"Duplicate capability", "Unresolved MCP command", "Malformed agent TOML"} {
		if !strings.Contains(output, want) {
			t.Fatalf("doctor output = %q, missing %q", output, want)
		}
	}
	for _, legacy := range []string{"duplicate-capability", "unresolved-mcp-command", "malformed-agent-toml"} {
		if strings.Contains(output, legacy) {
			t.Fatalf("doctor output = %q, contains internal finding code %q", output, legacy)
		}
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("doctor created or changed database; stat error=%v", err)
	}
}

type failingDiscoveryAdapter struct{}

func (failingDiscoveryAdapter) Runtime() domain.Runtime { return domain.RuntimeCodex }

func (failingDiscoveryAdapter) Discover(context.Context) (domain.Discovery, error) {
	return domain.Discovery{}, errors.New("synthetic discovery failure")
}

func (failingDiscoveryAdapter) ImportUsage(context.Context, time.Time) ([]domain.UsageEvent, error) {
	return nil, nil
}

var _ runtimepkg.Adapter = failingDiscoveryAdapter{}

func TestScanDiscoveryFailureDoesNotReplaceCurrentInventoryWithEmpty(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "harness-lint.db")
	now := time.Date(2026, 8, 13, 15, 0, 0, 0, time.UTC)
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	capability := domain.Capability{
		Runtime:        domain.RuntimeCodex,
		Type:           domain.CapabilitySkill,
		Name:           "still-present",
		Scope:          domain.ScopeProject,
		Source:         filepath.Join(root, "SKILL.md"),
		Enabled:        domain.EnabledStateEnabled,
		Advertisement:  domain.AdvertisementStateFullyAdvertised,
		MetadataTokens: domain.Measurement{Confidence: domain.ConfidenceUnknown},
		BodyTokens:     domain.Measurement{Confidence: domain.ConfidenceUnknown},
	}
	if err := db.RecordInventory(context.Background(), domain.RuntimeCodex, now, []domain.Capability{capability}); err != nil {
		t.Fatalf("seed inventory: %v", err)
	}
	var stdout bytes.Buffer
	err = runScanWithAdapters(context.Background(), commandConfig{now: now, verbose: true}, db, &stdout, []runtimepkg.Adapter{failingDiscoveryAdapter{}})
	if err == nil || !strings.Contains(err.Error(), "runtime codex discovery") {
		t.Fatalf("scan failure = %v, want runtime-qualified error", err)
	}
	current, err := db.ListCurrentCapabilities(context.Background(), domain.RuntimeCodex)
	if err != nil {
		t.Fatalf("list current inventory: %v", err)
	}
	if len(current) != 1 || current[0].Name != capability.Name {
		t.Fatalf("current inventory after failed discovery = %#v, want previous capability preserved", current)
	}
	if !strings.Contains(stdout.String(), "Inventory") || !strings.Contains(stdout.String(), "not recorded") {
		t.Fatalf("scan output = %q, want not-recorded status", stdout.String())
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
}

func TestExecuteRejectsUnknownCommandsAndInvalidFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "unknown command", args: []string{"unknown"}, want: "unknown command"},
		{name: "unknown flag", args: []string{"scan", "--not-a-flag"}, want: "flag provided but not defined"},
		{name: "negative days", args: []string{"stale", "--days", "-1"}, want: "--days cannot be negative"},
		{name: "bad time", args: []string{"scan", "--since", "not-a-time"}, want: "parse --since"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home")
			project := filepath.Join(root, "project")
			if err := os.MkdirAll(project, 0o755); err != nil {
				t.Fatalf("mkdir project: %v", err)
			}
			var stdout, stderr bytes.Buffer
			err := ExecuteWithOptions(Options{Home: home, CWD: project, ProjectRoot: project, Now: func() time.Time { return time.Date(2026, 8, 13, 15, 0, 0, 0, time.UTC) }}, test.args, nil, &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, stderr=%q, want substring %q", err, stderr.String(), test.want)
			}
		})
	}
}

func TestExecuteHelpFlagsSucceedForGlobalAndEveryCommand(t *testing.T) {
	args := [][]string{{"--help"}, {"-h"}}
	for _, command := range []string{"scan", "report", "context", "stale", "doctor", "ingest", "hooks"} {
		args = append(args, []string{command, "--help"}, []string{command, "-h"})
	}
	for _, testArgs := range args {
		t.Run(strings.Join(testArgs, "-"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := ExecuteWithOptions(Options{}, testArgs, nil, &stdout, &stderr); err != nil {
				t.Fatalf("help error = %v\nstderr=%s", err, stderr.String())
			}
			if !strings.Contains(strings.ToLower(stdout.String()), "usage:") || !strings.Contains(stdout.String(), "harness-lint") {
				t.Fatalf("help output = %q, want usage", stdout.String())
			}
		})
	}
}

func TestExecuteIngestHelpListsOnlySupportedOptions(t *testing.T) {
	for _, args := range [][]string{{"ingest", "--help"}, {"--help", "ingest"}} {
		t.Run(strings.Join(args, "-"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := ExecuteWithOptions(Options{}, args, nil, &stdout, &stderr); err != nil {
				t.Fatalf("help error = %v\nstderr=%s", err, stderr.String())
			}
			output := stdout.String()
			for _, want := range []string{
				"usage: harness-lint ingest [options]",
				"--db PATH",
				"--config-dir PATH",
				"--runtime claude|codex",
				"--event EVENT",
				"--managed-by harness-lint-hooks/v1",
				"stdin",
			} {
				if !strings.Contains(output, want) {
					t.Fatalf("ingest help = %q, missing %q", output, want)
				}
			}
			for _, forbidden := range []string{
				"--home",
				"--project",
				"--codex-home",
				"--claude-config",
				"--since",
				"--days",
				"--hook-capture",
				"--now",
				"--dry-run",
				"--json",
			} {
				if strings.Contains(output, forbidden) {
					t.Fatalf("ingest help = %q, unexpectedly contains %q", output, forbidden)
				}
			}
		})
	}
}

func TestCleanTextReplacesControlCharacters(t *testing.T) {
	got := cleanText("a\nb\tc\x00d\x1b")
	if got != "a b c d" {
		t.Fatalf("cleanText() = %q, want control characters replaced with spaces", got)
	}
}
