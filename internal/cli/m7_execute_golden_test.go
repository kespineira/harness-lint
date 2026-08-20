package cli

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/capture"
	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/history"
	"github.com/kespineira/harness-lint/internal/hooks"
	"github.com/kespineira/harness-lint/internal/presentation"
	"github.com/kespineira/harness-lint/internal/store"
)

const m7ExecuteNow = "2026-08-20T12:00:00Z"

var m7DatabaseSizeLine = regexp.MustCompile(`(?m)^  Size\s+.*$`)

func m7FixedNow() time.Time {
	return time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
}

func m7CLIOptions(root string, lookPath func(string) (string, error)) Options {
	if lookPath == nil {
		lookPath = func(string) (string, error) { return "/opt/harness-lint", nil }
	}
	return Options{
		Home:        filepath.Join(root, "home"),
		CWD:         root,
		ProjectRoot: root,
		Now:         m7FixedNow,
		LookPath:    lookPath,
	}
}

func m7Execute(t *testing.T, options Options, args []string, stdin io.Reader) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := ExecuteWithOptions(options, args, stdin, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func m7RequireExecute(t *testing.T, options Options, args []string, stdin io.Reader) string {
	t.Helper()
	stdout, stderr, err := m7Execute(t, options, args, stdin)
	if err != nil {
		t.Fatalf("execute %v: %v\nstdout=%s\nstderr=%s", args, err, stdout, stderr)
	}
	if stderr != "" {
		t.Fatalf("execute %v wrote stderr: %q", args, stderr)
	}
	return stdout
}

func m7AssertGolden(t *testing.T, name, root, output string) {
	t.Helper()
	output = strings.ReplaceAll(output, root, "<ROOT>")
	if name == "db-status" {
		output = m7DatabaseSizeLine.ReplaceAllString(output, "  Size       <SIZE>")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate M7 golden directory")
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", "testdata", "m7-human", name+".golden")
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read M7 golden %s: %v\nactual:\n%s", path, err, output)
	}
	if string(want) != output {
		t.Fatalf("M7 golden %s differs:\nwant:\n%s\n got:\n%s", name, string(want), output)
	}
	for _, line := range strings.Split(output, "\n") {
		if presentation.VisibleWidth(line) > 80 {
			t.Fatalf("M7 golden %s line exceeds 80 columns (%d): %q", name, presentation.VisibleWidth(line), line)
		}
	}
}

func m7WriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func m7InstallClaudeHooks(t *testing.T, root string, lookPath func(string) (string, error)) string {
	t.Helper()
	configRoot := filepath.Join(root, "claude")
	if _, err := hooks.NewClaude(hooks.Options{ConfigRoot: configRoot, LookPath: lookPath}).Install(context.Background()); err != nil {
		t.Fatalf("install Claude hooks: %v", err)
	}
	return configRoot
}

func m7Skill(name string, runtimeName domain.Runtime, scope domain.Scope, root string, advertisement domain.AdvertisementState, metadata, body domain.Measurement) domain.Capability {
	return domain.Capability{
		Runtime:        runtimeName,
		Type:           domain.CapabilitySkill,
		Name:           name,
		Scope:          scope,
		Source:         filepath.Join(root, "definitions", name, "SKILL.md"),
		Enabled:        domain.EnabledStateEnabled,
		Advertisement:  advertisement,
		MetadataTokens: metadata,
		BodyTokens:     body,
	}
}

func m7UnknownMeasurement() domain.Measurement {
	return domain.Measurement{Confidence: domain.ConfidenceUnknown}
}

func m7ExactMeasurement(value int64, basis string) domain.Measurement {
	return domain.Measurement{Value: value, Confidence: domain.ConfidenceExact, Basis: basis}
}

func m7EstimatedMeasurement(value int64, basis string) domain.Measurement {
	return domain.Measurement{Value: value, Confidence: domain.ConfidenceEstimated, Basis: basis}
}

func m7UsageEvent(now time.Time, runtimeName domain.Runtime, typ domain.CapabilityType, name, session string, eventType domain.EventType, provenance domain.Provenance) domain.UsageEvent {
	return domain.UsageEvent{
		ObservedAt:       now,
		Runtime:          runtimeName,
		SessionID:        session,
		ProjectID:        "m7-project",
		CapabilityType:   typ,
		CapabilityName:   name,
		EventType:        eventType,
		Provenance:       provenance,
		InvocationOrigin: domain.InvocationOriginUnknown,
		SchemaVersion:    domain.CurrentUsageEventSchemaVersion,
		Fingerprint:      "m7-" + name + "-" + session + "-" + string(eventType),
	}
}

func m7SeedInventory(t *testing.T, dbPath string, observedAt time.Time, capabilities []domain.Capability) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatalf("mkdir M7 fixture store parent: %v", err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open M7 fixture store: %v", err)
	}
	if err := db.RecordInventory(context.Background(), capabilities[0].Runtime, observedAt, capabilitiesForRuntime(capabilities, capabilities[0].Runtime)); err != nil {
		db.Close()
		t.Fatalf("record M7 inventory: %v", err)
	}
	for _, runtimeName := range []domain.Runtime{domain.RuntimeClaudeCode, domain.RuntimeCodex} {
		if runtimeName == capabilities[0].Runtime {
			continue
		}
		if values := capabilitiesForRuntime(capabilities, runtimeName); len(values) > 0 {
			if err := db.RecordInventory(context.Background(), runtimeName, observedAt, values); err != nil {
				db.Close()
				t.Fatalf("record M7 %s inventory: %v", runtimeName, err)
			}
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close M7 fixture store: %v", err)
	}
}

func capabilitiesForRuntime(capabilities []domain.Capability, runtimeName domain.Runtime) []domain.Capability {
	result := make([]domain.Capability, 0)
	for _, capability := range capabilities {
		if capability.Runtime == runtimeName {
			result = append(result, capability)
		}
	}
	return result
}

func m7SeedEvents(t *testing.T, dbPath string, events []domain.UsageEvent) {
	t.Helper()
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open M7 event store: %v", err)
	}
	if err := db.InsertUsageEvents(context.Background(), events); err != nil {
		db.Close()
		t.Fatalf("insert M7 events: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close M7 event store: %v", err)
	}
}

func m7SeedCaptureEpoch(t *testing.T, dbPath string, runtimeName domain.Runtime, start, end time.Time) {
	t.Helper()
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open M7 epoch store: %v", err)
	}
	if err := db.OpenCaptureEpoch(context.Background(), runtimeName, start, history.CaptureStartReasonConfirmedDirectDelivery); err != nil {
		db.Close()
		t.Fatalf("open M7 capture epoch: %v", err)
	}
	if !end.IsZero() {
		if err := db.CloseCaptureEpoch(context.Background(), runtimeName, end, history.CaptureEndReasonConfirmedCaptureFailure); err != nil {
			db.Close()
			t.Fatalf("close M7 capture epoch: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close M7 epoch store: %v", err)
	}
}

func TestM7ScanExecuteWithOptionsHumanGoldens(t *testing.T) {
	tests := []struct {
		name     string
		prepare  func(t *testing.T, root string)
		withHook bool
		golden   string
	}{
		{
			name: "healthy",
			prepare: func(t *testing.T, root string) {
				m7WriteFile(t, filepath.Join(root, "home", ".agents", "skills", "lint", "SKILL.md"), "---\nname: lint\ndescription: lint files\n---\nRun lint.\n")
			},
			withHook: true,
			golden:   "scan-healthy",
		},
		{
			name: "findings",
			prepare: func(t *testing.T, root string) {
				m7WriteFile(t, filepath.Join(root, "home", ".codex", "agents", "broken.toml"), "[broken\n")
			},
			golden: "scan-findings",
		},
		{
			name:    "empty",
			prepare: func(*testing.T, string) {},
			golden:  "scan-empty",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			test.prepare(t, root)
			dbPath := filepath.Join(root, "state", "scan.db")
			args := []string{
				"scan", "--db", dbPath, "--home", filepath.Join(root, "home"), "--project", root,
				"--now", m7ExecuteNow, "--color", "never",
			}
			if test.withHook {
				hookPath := filepath.Join(root, "capture.jsonl")
				m7WriteFile(t, hookPath, `{"hook_event_name":"PostToolUse","timestamp":"2026-08-20T11:30:00Z","session_id":"m7-session","cwd":"`+root+`","tool_name":"Bash"}`+"\n")
				args = append(args, "--hook-capture", hookPath)
			}
			output := m7RequireExecute(t, m7CLIOptions(root, nil), args, nil)
			m7AssertGolden(t, test.golden, root, output)
			for _, line := range strings.Split(output, "\n") {
				if presentation.VisibleWidth(line) > 80 {
					t.Fatalf("scan %s line exceeds 80 columns: %q", test.name, line)
				}
			}
		})
	}
}

func TestM7HooksTestExecuteWithOptionsHealthMatrix(t *testing.T) {
	setup := func(t *testing.T, root string, state string) (Options, []string, error) {
		t.Helper()
		resolved := func(string) (string, error) { return "/opt/harness-lint", nil }
		missing := func(string) (string, error) { return "", os.ErrNotExist }
		options := m7CLIOptions(root, resolved)
		configRoot := filepath.Join(root, "claude")
		dbPath := filepath.Join(root, "state", "hooks.db")
		var lookPath func(string) (string, error) = resolved
		if state == "missing executable" {
			lookPath = missing
		}
		if state != "broken" {
			if _, err := hooks.NewClaude(hooks.Options{ConfigRoot: configRoot, LookPath: resolved}).Install(context.Background()); err != nil {
				t.Fatalf("install hook fixture: %v", err)
			}
		}
		options.LookPath = lookPath
		args := []string{"hooks", "test", "claude", "--db", dbPath, "--claude-config", configRoot, "--now", m7ExecuteNow, "--color", "never"}
		if state == "verbose" {
			args = append(args, "--verbose")
		}
		if state == "relative" {
			args = []string{"hooks", "test", "claude", "--db", "state/hooks.db", "--claude-config", "claude", "--now", m7ExecuteNow, "--color", "never"}
		}
		if state == "degraded" {
			if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
				t.Fatalf("mkdir degraded hook store parent: %v", err)
			}
			db, err := store.Open(dbPath)
			if err != nil {
				t.Fatalf("open degraded hook store: %v", err)
			}
			if err := db.RecordCaptureFailure(context.Background(), capture.CaptureFailure{
				Runtime: domain.RuntimeClaudeCode, FailedAt: m7FixedNow().Add(-time.Minute), Kind: capture.FailureDatabaseBusy,
			}); err != nil {
				db.Close()
				t.Fatalf("record degraded hook failure: %v", err)
			}
			if err := db.Close(); err != nil {
				t.Fatalf("close degraded hook store: %v", err)
			}
		}
		if state == "healthy" {
			payload := `{"hook_event_name":"PostToolUse","session_id":"m7-session","cwd":"/m7-project","tool_name":"Bash","tool_use_id":"m7-delivery"}`
			// Ingest intentionally has no --now flag: its observed_at value is
			// the receive clock injected through Options.Now. Seed it 15 minutes
			// before the command clock so the golden locks relative delivery time.
			seedOptions := options
			seedOptions.Now = func() time.Time { return m7FixedNow().Add(-15 * time.Minute) }
			_, _, err := m7Execute(t, seedOptions, []string{"ingest", "--runtime", "claude", "--event", "PostToolUse", "--db", dbPath}, strings.NewReader(payload))
			if err != nil {
				t.Fatalf("seed healthy delivery: %v", err)
			}
		}
		if state != "healthy" && state != "degraded" {
			initializeTestStore(t, dbPath)
		}
		if state == "broken" {
			m7WriteFile(t, filepath.Join(configRoot, "settings.json"), "{\n")
		}
		return options, args, nil
	}

	tests := []struct {
		name     string
		state    string
		wantErr  bool
		contains []string
		golden   string
	}{
		{name: "healthy", state: "healthy", contains: []string{"✓ Healthy", "last event", "1/1 runtime healthy"}, golden: "hooks-healthy"},
		{name: "idle", state: "idle", contains: []string{"- Idle", "no direct activity yet", "0/1 runtime healthy · 1 idle"}, golden: "hooks-idle"},
		{name: "degraded", state: "degraded", wantErr: true, contains: []string{"! Degraded", "1 recent delivery failure"}, golden: "hooks-degraded"},
		{name: "broken", state: "broken", wantErr: true, contains: []string{"✗ Broken", "hook configuration is malformed"}, golden: "hooks-broken"},
		{name: "missing executable", state: "missing executable", wantErr: true, contains: []string{"✗ Broken", "harness-lint executable not found"}, golden: "hooks-missing-executable"},
		{name: "relative", state: "relative", contains: []string{"- Idle", "0/1 runtime healthy · 1 idle"}, golden: "hooks-relative"},
		{name: "verbose", state: "verbose", contains: []string{"Configuration", "Limitations", "Runtime version"}, golden: "hooks-verbose"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			options, args, _ := setup(t, root, test.state)
			output, stderr, err := m7Execute(t, options, args, nil)
			if test.wantErr && err == nil {
				t.Fatalf("hooks %s unexpectedly succeeded: %s", test.name, output)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("hooks %s error = %v\nstdout=%s\nstderr=%s", test.name, err, output, stderr)
			}
			if stderr != "" {
				t.Fatalf("hooks %s wrote stderr: %q", test.name, stderr)
			}
			for _, want := range test.contains {
				if !strings.Contains(output, want) {
					t.Fatalf("hooks %s output missing %q: %s", test.name, want, output)
				}
			}
			m7AssertGolden(t, test.golden, root, output)
			for _, line := range strings.Split(output, "\n") {
				if presentation.VisibleWidth(line) > 80 {
					t.Fatalf("hooks %s line exceeds 80 columns: %q", test.name, line)
				}
			}
		})
	}
}

func TestM7HookStatusExecuteWithOptionsCoversRelativeMissingAndJSONColor(t *testing.T) {
	root := t.TempDir()
	configRoot := m7InstallClaudeHooks(t, root, func(string) (string, error) { return "/opt/harness-lint", nil })
	options := m7CLIOptions(root, func(string) (string, error) { return "", os.ErrNotExist })
	output, stderr, err := m7Execute(t, options, []string{
		"hooks", "status", "claude", "--claude-config", "claude", "--now", m7ExecuteNow, "--verbose", "--color", "never",
	}, nil)
	if err != nil {
		t.Fatalf("relative hook status error: %v\nstdout=%s\nstderr=%s", err, output, stderr)
	}
	if stderr != "" || !strings.Contains(output, "Executable") || !strings.Contains(output, "Unavailable") || !strings.Contains(output, "Configuration") {
		t.Fatalf("relative/missing hook status = %q (stderr=%q)", output, stderr)
	}
	compactOutput := strings.Join(strings.Fields(output), "")
	compactConfigPath := strings.Join(strings.Fields(filepath.Join(configRoot, "settings.json")), "")
	if !strings.Contains(compactOutput, compactConfigPath) {
		t.Fatalf("relative hook status did not resolve config path %q: %s", configRoot, output)
	}

	jsonOutput, stderr, err := m7Execute(t, options, []string{
		"hooks", "status", "claude", "--claude-config", configRoot, "--json", "--color", "always", "--now", m7ExecuteNow,
	}, nil)
	if err != nil || stderr != "" {
		t.Fatalf("hook JSON status error=%v stderr=%q output=%s", err, stderr, jsonOutput)
	}
	if strings.Contains(jsonOutput, "\x1b[") {
		t.Fatalf("hook JSON status contains ANSI: %q", jsonOutput)
	}
}

func TestM7DatabaseExecuteWithOptionsHumanGoldens(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "state", "database.db")
	initializeTestStore(t, dbPath)
	options := m7CLIOptions(root, nil)
	statusOptions := options
	statusOptions.Home = root
	status := m7RequireExecute(t, statusOptions, []string{"db", "status", "--db", dbPath, "--now", m7ExecuteNow, "--color", "never"}, nil)
	m7AssertGolden(t, "db-status", root, status)
	healthy := m7RequireExecute(t, statusOptions, []string{"db", "check", "--db", dbPath, "--now", m7ExecuteNow, "--color", "never"}, nil)
	m7AssertGolden(t, "db-check-healthy", root, healthy)

	if err := m7InsertOrphanEvidence(dbPath); err != nil {
		t.Fatalf("seed failed database check: %v", err)
	}
	failed, stderr, err := m7Execute(t, statusOptions, []string{"db", "check", "--db", dbPath, "--now", m7ExecuteNow, "--color", "never"}, nil)
	if err == nil || !strings.Contains(err.Error(), "integrity issues") {
		t.Fatalf("failed database check error = %v\nstdout=%s\nstderr=%s", err, failed, stderr)
	}
	if stderr != "" {
		t.Fatalf("failed database check wrote stderr: %q", stderr)
	}
	m7AssertGolden(t, "db-check-failed", root, failed)
}

func m7InsertOrphanEvidence(dbPath string) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO usage_event_evidence(fingerprint, provenance, observed_at, invocation_origin) VALUES ('m7-orphan', 'import', '2026-08-20T11:00:00Z', 'unknown')`)
	return err
}

func TestM7ReportExecuteWithOptionsMixedNoStaleAllVerboseAndCoverage(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "state", "report.db")
	now := m7FixedNow()
	keep := m7Skill("keep", domain.RuntimeCodex, domain.ScopeUser, root, domain.AdvertisementStateFullyAdvertised, m7ExactMeasurement(10, "fixture metadata"), m7UnknownMeasurement())
	review := m7Skill("review-never", domain.RuntimeCodex, domain.ScopeProject, root, domain.AdvertisementStateUnknown, m7UnknownMeasurement(), m7UnknownMeasurement())
	stale := m7Skill("stale", domain.RuntimeCodex, domain.ScopeProject, root, domain.AdvertisementStateNameOnly, m7UnknownMeasurement(), m7UnknownMeasurement())
	lowCoverage := m7Skill("low-coverage", domain.RuntimeCodex, domain.ScopeUser, root, domain.AdvertisementStateFullyAdvertised, m7ExactMeasurement(10, "fixture metadata"), m7ExactMeasurement(20, "fixture body"))
	highCoverage := m7Skill("high-coverage", domain.RuntimeCodex, domain.ScopeUser, root, domain.AdvertisementStateFullyAdvertised, m7ExactMeasurement(10, "fixture metadata"), m7ExactMeasurement(20, "fixture body"))
	unknownCoverage := m7Skill("unknown-coverage", domain.RuntimeClaudeCode, domain.ScopeUser, root, domain.AdvertisementStateUnknown, m7UnknownMeasurement(), m7UnknownMeasurement())
	m7SeedInventory(t, dbPath, now.Add(-6*time.Hour), []domain.Capability{keep, review, stale, lowCoverage, highCoverage, unknownCoverage})
	m7SeedEvents(t, dbPath, []domain.UsageEvent{
		m7UsageEvent(now.Add(-time.Hour), domain.RuntimeCodex, domain.CapabilitySkill, "keep", "keep-a", domain.EventInvoked, domain.ProvenanceHook),
		m7UsageEvent(now.Add(-2*time.Hour), domain.RuntimeCodex, domain.CapabilitySkill, "keep", "keep-b", domain.EventInvoked, domain.ProvenanceTranscript),
		m7UsageEvent(now.Add(-61*24*time.Hour), domain.RuntimeCodex, domain.CapabilitySkill, "stale", "stale-a", domain.EventInvoked, domain.ProvenanceImport),
		m7UsageEvent(now.Add(-30*time.Minute), domain.RuntimeCodex, domain.CapabilitySkill, "low-coverage", "low-a", domain.EventInvoked, domain.ProvenanceHook),
	})
	// Re-recording the inventory transitions low-coverage out of the current
	// presence interval and back in at --now, while high-coverage remains
	// present. The resulting report proves low, high, and unknown modeled
	// coverage without manufacturing a completeness claim.
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open report fixture for coverage transitions: %v", err)
	}
	if err := db.RecordInventory(context.Background(), domain.RuntimeCodex, now.Add(-5*time.Hour), []domain.Capability{keep, review, stale, highCoverage}); err != nil {
		db.Close()
		t.Fatalf("close low coverage presence: %v", err)
	}
	if err := db.RecordInventory(context.Background(), domain.RuntimeCodex, now, []domain.Capability{keep, review, stale, lowCoverage, highCoverage}); err != nil {
		db.Close()
		t.Fatalf("reopen low coverage presence: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close report coverage fixture: %v", err)
	}
	m7SeedCaptureEpoch(t, dbPath, domain.RuntimeCodex, now.Add(-6*time.Hour), now)

	options := m7CLIOptions(root, nil)
	mixed := m7RequireExecute(t, options, []string{"report", "--db", dbPath, "--days", "60", "--now", m7ExecuteNow, "--color", "never"}, nil)
	m7AssertGolden(t, "report-mixed", root, mixed)
	if !strings.Contains(mixed, "Advertisement is unknown") || !strings.Contains(mixed, "low-coverage") || !strings.Contains(mixed, "stale") {
		t.Fatalf("mixed report omitted requested evidence states: %s", mixed)
	}
	all := m7RequireExecute(t, options, []string{"report", "--all", "--db", dbPath, "--days", "60", "--now", m7ExecuteNow, "--color", "never"}, nil)
	m7AssertGolden(t, "report-all", root, all)
	allVerbose := m7RequireExecute(t, options, []string{"report", "--all", "--verbose", "--db", dbPath, "--days", "60", "--now", m7ExecuteNow, "--color", "never"}, nil)
	for _, want := range []string{
		"Capabilities (6)", "Exact timestamps", "Coverage basis", "Provenance", "modeled intersection",
		"6h modeled intersection", "1h modeled intersection", "unknown (no confirmed capture/presence intersection)",
	} {
		if !strings.Contains(allVerbose, want) {
			t.Fatalf("all verbose report missing %q: %s", want, allVerbose)
		}
	}
	staleOutput := m7RequireExecute(t, options, []string{"stale", "--db", dbPath, "--days", "60", "--now", m7ExecuteNow, "--color", "never"}, nil)
	m7AssertGolden(t, "stale-items", root, staleOutput)
	for _, line := range strings.Split(allVerbose, "\n") {
		if presentation.VisibleWidth(line) > 80 {
			t.Fatalf("all verbose report line exceeds 80 columns: %q", line)
		}
	}

	noStaleRoot := t.TempDir()
	noStaleDB := filepath.Join(noStaleRoot, "state", "report.db")
	noStaleCap := m7Skill("recent", domain.RuntimeCodex, domain.ScopeUser, noStaleRoot, domain.AdvertisementStateFullyAdvertised, m7UnknownMeasurement(), m7UnknownMeasurement())
	m7SeedInventory(t, noStaleDB, now, []domain.Capability{noStaleCap})
	m7SeedEvents(t, noStaleDB, []domain.UsageEvent{m7UsageEvent(now.Add(-time.Hour), domain.RuntimeCodex, domain.CapabilitySkill, "recent", "recent", domain.EventInvoked, domain.ProvenanceImport)})
	noStale := m7RequireExecute(t, m7CLIOptions(noStaleRoot, nil), []string{"report", "--db", noStaleDB, "--days", "60", "--now", m7ExecuteNow, "--color", "never"}, nil)
	if !strings.Contains(noStale, "No stale or review capabilities need attention.") || strings.Contains(noStale, "STALE") {
		t.Fatalf("no-stale report = %s", noStale)
	}
	m7AssertGolden(t, "report-no-stale", noStaleRoot, noStale)

	reviewOnlyRoot := t.TempDir()
	reviewOnlyDB := filepath.Join(reviewOnlyRoot, "state", "stale.db")
	reviewOnly := m7Skill("review-only", domain.RuntimeCodex, domain.ScopeUser, reviewOnlyRoot, domain.AdvertisementStateUnknown, m7UnknownMeasurement(), m7UnknownMeasurement())
	m7SeedInventory(t, reviewOnlyDB, now, []domain.Capability{reviewOnly})
	verbose := m7RequireExecute(t, m7CLIOptions(reviewOnlyRoot, nil), []string{"report", "--verbose", "--db", reviewOnlyDB, "--days", "60", "--now", m7ExecuteNow, "--color", "never"}, nil)
	m7AssertGolden(t, "report-verbose", reviewOnlyRoot, verbose)
	staleReviewOnly := m7RequireExecute(t, m7CLIOptions(reviewOnlyRoot, nil), []string{"stale", "--db", reviewOnlyDB, "--days", "60", "--now", m7ExecuteNow, "--color", "never"}, nil)
	m7AssertGolden(t, "stale-review-only", reviewOnlyRoot, staleReviewOnly)
}

func TestM7ExplainExecuteWithOptionsDecisionAndSelectionMatrix(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "state", "explain.db")
	now := m7FixedNow()
	capabilities := []domain.Capability{
		m7Skill("keep", domain.RuntimeCodex, domain.ScopeUser, root, domain.AdvertisementStateFullyAdvertised, m7UnknownMeasurement(), m7UnknownMeasurement()),
		m7Skill("review-never", domain.RuntimeCodex, domain.ScopeProject, root, domain.AdvertisementStateUnknown, m7UnknownMeasurement(), m7UnknownMeasurement()),
		m7Skill("stale", domain.RuntimeCodex, domain.ScopeProject, root, domain.AdvertisementStateNotAdvertised, m7UnknownMeasurement(), m7UnknownMeasurement()),
		m7Skill("same", domain.RuntimeCodex, domain.ScopeUser, root, domain.AdvertisementStateFullyAdvertised, m7UnknownMeasurement(), m7UnknownMeasurement()),
		m7Skill("same", domain.RuntimeCodex, domain.ScopeProject, root, domain.AdvertisementStateFullyAdvertised, m7UnknownMeasurement(), m7UnknownMeasurement()),
	}
	// Keep duplicate definitions distinct while preserving the canonical event
	// key; the explain command must require --scope before choosing one.
	capabilities[4].Source = filepath.Join(root, "definitions", "same-project", "SKILL.md")
	m7SeedInventory(t, dbPath, now, capabilities)
	m7SeedEvents(t, dbPath, []domain.UsageEvent{
		m7UsageEvent(now.Add(-time.Hour), domain.RuntimeCodex, domain.CapabilitySkill, "keep", "keep-a", domain.EventInvoked, domain.ProvenanceImport),
		m7UsageEvent(now.Add(-2*time.Hour), domain.RuntimeCodex, domain.CapabilitySkill, "keep", "keep-b", domain.EventInvoked, domain.ProvenanceImport),
		m7UsageEvent(now.Add(-61*24*time.Hour), domain.RuntimeCodex, domain.CapabilitySkill, "stale", "stale-a", domain.EventInvoked, domain.ProvenanceImport),
	})
	options := m7CLIOptions(root, nil)
	for _, test := range []struct {
		name   string
		args   []string
		golden string
	}{
		{name: "KEEP", args: []string{"explain", "keep", "--db", dbPath, "--now", m7ExecuteNow, "--color", "never"}, golden: "explain-keep"},
		{name: "REVIEW never", args: []string{"explain", "review-never", "--db", dbPath, "--now", m7ExecuteNow, "--color", "never"}, golden: "explain-review-never"},
		{name: "STALE", args: []string{"explain", "stale", "--db", dbPath, "--now", m7ExecuteNow, "--color", "never"}, golden: "explain-stale"},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := m7RequireExecute(t, options, test.args, nil)
			m7AssertGolden(t, test.golden, root, output)
			for _, line := range strings.Split(output, "\n") {
				if presentation.VisibleWidth(line) > 80 {
					t.Fatalf("explain %s line exceeds 80 columns: %q", test.name, line)
				}
			}
		})
	}
	if output, stderr, err := m7Execute(t, options, []string{"explain", "same", "--db", dbPath, "--now", m7ExecuteNow, "--color", "never"}, nil); err == nil || stderr != "" || output != "" || !strings.Contains(err.Error(), "matches multiple") {
		t.Fatalf("ambiguous explain output=%q stderr=%q error=%v", output, stderr, err)
	}
	if output, stderr, err := m7Execute(t, options, []string{"explain", "missing", "--db", dbPath, "--now", m7ExecuteNow, "--color", "never"}, nil); err == nil || stderr != "" || output != "" || !strings.Contains(err.Error(), "was found") {
		t.Fatalf("unknown explain output=%q stderr=%q error=%v", output, stderr, err)
	}
	filtered := m7RequireExecute(t, options, []string{"explain", "same", "--scope", "project", "--db", dbPath, "--now", m7ExecuteNow, "--color", "never"}, nil)
	if !strings.Contains(filtered, "same") || !strings.Contains(filtered, "Why REVIEW?") {
		t.Fatalf("scope-filtered ambiguous explain = %s", filtered)
	}
}

func TestM7ContextExecuteWithOptionsEstimatesAndUnknownMCPGolden(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "state", "context.db")
	caps := []domain.Capability{
		m7Skill("estimated", domain.RuntimeCodex, domain.ScopeUser, root, domain.AdvertisementStateFullyAdvertised, m7ExactMeasurement(12, "fixture metadata"), m7EstimatedMeasurement(34, "fixture body")),
		{
			Runtime: domain.RuntimeCodex, Type: domain.CapabilityMCPServer, Name: "remote", Scope: domain.ScopeUser,
			Source: filepath.Join(root, "definitions", "remote"), Enabled: domain.EnabledStateEnabled, Advertisement: domain.AdvertisementStateUnknown,
			MetadataTokens: m7UnknownMeasurement(), BodyTokens: m7UnknownMeasurement(),
		},
	}
	m7SeedInventory(t, dbPath, m7FixedNow(), caps)
	output := m7RequireExecute(t, m7CLIOptions(root, nil), []string{"context", "--db", dbPath, "--now", m7ExecuteNow, "--color", "never"}, nil)
	m7AssertGolden(t, "context-estimates", root, output)
	for _, want := range []string{"~12 tokens", "~34 tokens", "MCP schema cost is unknown", "token measurements are unknown"} {
		if !strings.Contains(output, want) {
			t.Fatalf("context output missing %q: %s", want, output)
		}
	}
}

func TestM7UsageExecuteWithOptionsTableMonthlyAndEmptyGoldens(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "state", "usage.db")
	now := m7FixedNow()
	caps := []domain.Capability{
		m7Skill("alpha", domain.RuntimeCodex, domain.ScopeUser, root, domain.AdvertisementStateFullyAdvertised, m7UnknownMeasurement(), m7UnknownMeasurement()),
		m7Skill("beta", domain.RuntimeCodex, domain.ScopeUser, root, domain.AdvertisementStateFullyAdvertised, m7UnknownMeasurement(), m7UnknownMeasurement()),
	}
	m7SeedInventory(t, dbPath, now, caps)
	m7SeedEvents(t, dbPath, []domain.UsageEvent{
		m7UsageEvent(now.Add(-time.Hour), domain.RuntimeCodex, domain.CapabilitySkill, "alpha", "a-1", domain.EventInvoked, domain.ProvenanceHook),
		m7UsageEvent(now.Add(-2*time.Hour), domain.RuntimeCodex, domain.CapabilitySkill, "alpha", "a-2", domain.EventInvoked, domain.ProvenanceTranscript),
		m7UsageEvent(now.Add(-25*24*time.Hour), domain.RuntimeCodex, domain.CapabilitySkill, "alpha", "a-3", domain.EventInvoked, domain.ProvenanceImport),
		m7UsageEvent(now.Add(-3*time.Hour), domain.RuntimeCodex, domain.CapabilitySkill, "beta", "b-1", domain.EventLoaded, domain.ProvenanceImport),
	})
	options := m7CLIOptions(root, nil)
	output := m7RequireExecute(t, options, []string{"usage", "--db", dbPath, "--days", "30", "--now", m7ExecuteNow, "--color", "never"}, nil)
	m7AssertGolden(t, "usage-table", root, output)
	for _, want := range []string{"Capabilities ranked by uses", "Observation totals", "Invocation evidence"} {
		if !strings.Contains(output, want) {
			t.Fatalf("usage output missing %q: %s", want, output)
		}
	}
	monthlyOutput := m7RequireExecute(t, options, []string{"usage", "--db", dbPath, "--days", "30", "--monthly", "--now", m7ExecuteNow, "--color", "never"}, nil)
	m7AssertGolden(t, "usage-monthly", root, monthlyOutput)
	if !strings.Contains(monthlyOutput, "Monthly usage") || !strings.Contains(monthlyOutput, "2026-08") || strings.Contains(monthlyOutput, "Capabilities ranked by uses") {
		t.Fatalf("monthly usage output is not compact: %s", monthlyOutput)
	}
	emptyRoot := t.TempDir()
	emptyDB := filepath.Join(emptyRoot, "state", "usage.db")
	initializeTestStore(t, emptyDB)
	empty := m7RequireExecute(t, m7CLIOptions(emptyRoot, nil), []string{"usage", "--db", emptyDB, "--days", "30", "--now", m7ExecuteNow, "--color", "never"}, nil)
	m7AssertGolden(t, "usage-empty", emptyRoot, empty)
	if !strings.Contains(empty, "No usage observations were found in this period.") {
		t.Fatalf("empty usage output = %s", empty)
	}
}

func TestM7DoctorExecuteWithOptionsCleanAndFindingsGoldens(t *testing.T) {
	cleanRoot := t.TempDir()
	clean := m7RequireExecute(t, m7CLIOptions(cleanRoot, nil), []string{"doctor", "--now", m7ExecuteNow, "--color", "never"}, nil)
	m7AssertGolden(t, "doctor-clean", cleanRoot, clean)
	if !strings.Contains(clean, "✓ Healthy") || !strings.Contains(clean, "No configuration problems found.") {
		t.Fatalf("clean doctor output = %s", clean)
	}

	findingsRoot := t.TempDir()
	m7WriteFile(t, filepath.Join(findingsRoot, "home", ".codex", "agents", "broken.toml"), "[broken\n")
	findings := m7RequireExecute(t, m7CLIOptions(findingsRoot, nil), []string{"doctor", "--home", filepath.Join(findingsRoot, "home"), "--project", findingsRoot, "--now", m7ExecuteNow, "--color", "never"}, nil)
	m7AssertGolden(t, "doctor-findings", findingsRoot, findings)
	if !strings.Contains(findings, "Findings") || !strings.Contains(findings, "Malformed") || !strings.Contains(findings, "requires attention") {
		t.Fatalf("findings doctor output = %s", findings)
	}
}

func TestM7ExecuteWithOptionsColorRedirectJSONAndWidthContracts(t *testing.T) {
	root := t.TempDir()
	configRoot := m7InstallClaudeHooks(t, root, func(string) (string, error) { return "/opt/harness-lint", nil })
	base := m7CLIOptions(root, func(string) (string, error) { return "/opt/harness-lint", nil })
	base.IsTerminal = func(io.Writer) bool { return true }
	base.LookupEnv = func(string) (string, bool) { return "", false }
	args := []string{"hooks", "status", "claude", "--claude-config", configRoot, "--now", m7ExecuteNow}
	for _, test := range []struct {
		name     string
		options  Options
		color    string
		wantANSI bool
	}{
		{name: "tty auto", options: base, color: "auto", wantANSI: true},
		{name: "redirect auto", options: func() Options { value := base; value.IsTerminal = func(io.Writer) bool { return false }; return value }(), color: "auto"},
		{name: "always", options: base, color: "always", wantANSI: true},
		{name: "never", options: base, color: "never"},
		{name: "NO_COLOR", options: func() Options {
			value := base
			value.LookupEnv = func(string) (string, bool) { return "", true }
			return value
		}(), color: "auto"},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := m7RequireExecute(t, test.options, append(append([]string(nil), args...), "--color", test.color), nil)
			if strings.Contains(output, "\x1b[") != test.wantANSI {
				t.Fatalf("%s ANSI=%t output=%q", test.name, strings.Contains(output, "\x1b["), output)
			}
			for _, line := range strings.Split(output, "\n") {
				if presentation.VisibleWidth(line) > 80 {
					t.Fatalf("%s line exceeds 80 columns: %q", test.name, line)
				}
			}
		})
	}

	dbPath := filepath.Join(root, "state", "json.db")
	initializeTestStore(t, dbPath)
	jsonCommands := [][]string{
		{"hooks", "status", "claude", "--claude-config", configRoot, "--json"},
		{"db", "status", "--db", dbPath, "--json"},
		{"db", "check", "--db", dbPath, "--json"},
		{"report", "--db", dbPath, "--json", "--days", "60"},
		{"stale", "--db", dbPath, "--json", "--days", "60"},
		{"usage", "--db", dbPath, "--json", "--days", "30"},
	}
	for _, command := range jsonCommands {
		args := append(append([]string(nil), command...), "--now", m7ExecuteNow, "--color", "always")
		output, stderr, err := m7Execute(t, base, args, nil)
		if err != nil {
			t.Fatalf("JSON %v error = %v\nstdout=%s\nstderr=%s", command, err, output, stderr)
		}
		if stderr != "" || strings.Contains(output, "\x1b[") {
			t.Fatalf("JSON %v terminal leakage: stderr=%q output=%q", command, stderr, output)
		}
	}
}
