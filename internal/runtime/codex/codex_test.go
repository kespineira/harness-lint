package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
)

func TestDiscoverInventoryUsesInjectedRootsAndPrecedence(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	repo := filepath.Join(root, "repo")
	current := filepath.Join(repo, "packages", "app")
	system := filepath.Join(root, "system-skills")
	for _, dir := range []string{
		filepath.Join(home, ".agents", "skills", "personal"),
		filepath.Join(repo, ".agents", "skills", "project"),
		filepath.Join(current, ".agents", "skills", "nested"),
		filepath.Join(system, "system"),
		filepath.Join(home, ".codex", "agents"),
		filepath.Join(repo, ".codex", "agents"),
		filepath.Join(repo, ".codex"),
	} {
		mkdir(t, dir)
	}
	writeFile(t, filepath.Join(home, ".agents", "skills", "personal", "SKILL.md"), "---\nname: personal\ndescription: personal skill\n---\nprivate body\n")
	writeFile(t, filepath.Join(repo, ".agents", "skills", "project", "SKILL.md"), "---\nname: project\ndescription: project skill\n---\nproject body\n")
	writeFile(t, filepath.Join(current, ".agents", "skills", "nested", "SKILL.md"), "---\nname: nested\ndescription: nested skill\n---\nnested body\n")
	writeFile(t, filepath.Join(system, "system", "SKILL.md"), "---\nname: system\ndescription: system skill\n---\nsystem body\n")

	writeFile(t, filepath.Join(home, ".codex", "AGENTS.md"), "user instructions\n")
	writeFile(t, filepath.Join(repo, "AGENTS.md"), "repo instructions\n")
	writeFile(t, filepath.Join(current, "AGENTS.md"), "ordinary instructions\n")
	writeFile(t, filepath.Join(current, "AGENTS.override.md"), "override instructions\n")
	writeFile(t, filepath.Join(home, ".codex", "agents", "reviewer.toml"), "name = \"reviewer\"\ndescription = \"review code\"\ndeveloper_instructions = \"do not emit secrets\"\n")
	writeFile(t, filepath.Join(repo, ".codex", "agents", "security.toml"), "name = \"security\"\ndescription = \"security pass\"\n")
	writeFile(t, filepath.Join(home, ".codex", "config.toml"), "[mcp_servers.fs]\ncommand = \"missing-mcp\"\nenabled = false\n\n[mcp_servers.fs.tool_filters]\ninclude = [\"read\"]\nexclude = [\"write\"]\n")
	writeFile(t, filepath.Join(repo, ".codex", "config.toml"), "[mcp_servers.repo]\ncommand = \"known-mcp\"\nenabled = true\nenabled_tools = [\"search\"]\n")
	writeFile(t, filepath.Join(home, ".codex", "hooks.json"), "{\"hooks\": {\"PostToolUse\": [{\"type\": \"command\"}]}}")
	writeFile(t, filepath.Join(repo, ".codex", "hooks.json"), "{\"PostToolUseFailure\": [{\"enabled\": false}]}")

	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.FixedZone("CEST", 2*60*60))
	lookupCalls := []string{}
	adapter := New(Options{
		UserHome:         home,
		ConfigRoot:       filepath.Join(home, ".codex"),
		RepoRoot:         repo,
		CurrentDir:       current,
		SystemSkillRoots: []string{system},
		Clock:            func() time.Time { return now },
		CommandLookup: func(command string) (string, error) {
			lookupCalls = append(lookupCalls, command)
			if command == "known-mcp" {
				return "/bin/known-mcp", nil
			}
			return "", errors.New("not found")
		},
	})
	discovery, err := adapter.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(lookupCalls) != 2 || lookupCalls[0] != "missing-mcp" || lookupCalls[1] != "known-mcp" {
		t.Fatalf("command lookups = %#v, want both configured commands in deterministic order", lookupCalls)
	}

	capabilities := capabilitiesByName(discovery.Capabilities)
	for _, name := range []string{"personal", "project", "nested", "system", "reviewer", "security", "PostToolUse", "PostToolUseFailure", "fs", "repo", "mcp__fs__read", "mcp__fs__write", "mcp__repo__search"} {
		if len(capabilities[name]) == 0 {
			t.Fatalf("missing capability %q in %#v", name, discovery.Capabilities)
		}
	}
	if got := capabilities["personal"][0].Scope; got != domain.ScopeUser {
		t.Fatalf("personal skill scope = %q, want user", got)
	}
	if got := capabilities["project"][0].Scope; got != domain.ScopeProject {
		t.Fatalf("project skill scope = %q, want project", got)
	}
	if got := capabilities["system"][0].Scope; got != domain.ScopeGlobal {
		t.Fatalf("system skill scope = %q, want global", got)
	}
	for _, name := range []string{"personal", "project", "nested", "system"} {
		capability := capabilities[name][0]
		if capability.MetadataTokens.Confidence != domain.ConfidenceEstimated || capability.BodyTokens.Confidence != domain.ConfidenceEstimated {
			t.Fatalf("skill %q measurements = %#v/%#v, want explicit estimates", name, capability.MetadataTokens, capability.BodyTokens)
		}
		if capability.Hash == "" || len(capability.Hash) != 64 {
			t.Fatalf("skill %q hash = %q, want SHA-256 hex", name, capability.Hash)
		}
		if !capability.FirstSeen.Equal(now.UTC()) || !capability.LastSeen.Equal(now.UTC()) {
			t.Fatalf("skill %q timestamps = %s..%s, want injected UTC clock", name, capability.FirstSeen, capability.LastSeen)
		}
	}
	if len(capabilities["fs"]) != 1 || capabilities["fs"][0].Enabled != domain.EnabledStateDisabled {
		t.Fatalf("user MCP state = %#v, want disabled", capabilities["fs"])
	}
	if len(capabilities["repo"]) != 1 || capabilities["repo"][0].Enabled != domain.EnabledStateEnabled {
		t.Fatalf("project MCP state = %#v, want enabled", capabilities["repo"])
	}
	if got := capabilities["PostToolUse"][0].Enabled; got != domain.EnabledStateUnknown {
		t.Fatalf("unannotated hook state = %q, want unknown", got)
	}
	if got := capabilities["PostToolUseFailure"][0].Enabled; got != domain.EnabledStateDisabled {
		t.Fatalf("annotated hook state = %q, want disabled", got)
	}
	for _, capability := range discovery.Capabilities {
		if capability.Type == domain.CapabilitySkill && capability.MetadataTokens.Confidence == domain.ConfidenceExact {
			t.Fatal("skill advertised metadata must not claim exact runtime cost")
		}
	}

	var instructionSources []string
	for _, capability := range discovery.Capabilities {
		if capability.Type == domain.CapabilityInstructionFile {
			instructionSources = append(instructionSources, capability.Source)
		}
	}
	sortStrings(instructionSources)
	wantInstructions := []string{filepath.Join(home, ".codex", "AGENTS.md"), filepath.Join(repo, "AGENTS.md"), filepath.Join(current, "AGENTS.override.md")}
	sortStrings(wantInstructions)
	if strings.Join(instructionSources, "\n") != strings.Join(wantInstructions, "\n") {
		t.Fatalf("effective instruction chain = %#v, want %#v", instructionSources, wantInstructions)
	}
}

func TestDiscoverFindingsMalformedDuplicateAndBrokenPaths(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	repo := filepath.Join(root, "repo")
	userSkill := filepath.Join(home, ".agents", "skills", "one")
	projectSkill := filepath.Join(repo, ".agents", "skills", "one")
	mkdir(t, userSkill)
	mkdir(t, projectSkill)
	writeFile(t, filepath.Join(userSkill, "SKILL.md"), "---\nname: one\ndescription: user\n---\nbody\n")
	writeFile(t, filepath.Join(projectSkill, "SKILL.md"), "not frontmatter\nraw body\n")
	mkdir(t, filepath.Join(home, ".codex", "agents"))
	writeFile(t, filepath.Join(home, ".codex", "agents", "bad.toml"), "name = \"bad\"\nthis is not TOML\n")
	mkdir(t, filepath.Join(repo, ".codex"))
	writeFile(t, filepath.Join(repo, ".codex", "config.toml"), "[mcp_servers.bad]\nenabled = [true]\n")
	writeFile(t, filepath.Join(repo, ".codex", "hooks.json"), "{ malformed")
	if err := os.Symlink(filepath.Join(root, "does-not-exist"), filepath.Join(userSkill, "broken-link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	adapter := New(Options{UserHome: home, RepoRoot: repo, CurrentDir: repo, Clock: func() time.Time { return time.Unix(10, 0) }})
	discovery, err := adapter.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	codes := findingCodes(discovery.Findings)
	for _, code := range []string{"malformed-skill-metadata", "malformed-agent-toml", "malformed-config", "malformed-hooks", "duplicate-capability-name", "broken-symlink"} {
		if !codes[code] {
			t.Fatalf("missing finding code %q in %#v", code, discovery.Findings)
		}
	}
}

func TestImportUsagePrivacyExplicitSignalsAndInclusiveSince(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	sessions := filepath.Join(root, "sessions")
	mkdir(t, sessions)
	since := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	writeFile(t, filepath.Join(sessions, "session.jsonl"), strings.Join([]string{
		`{"timestamp":"2026-08-13T09:59:00Z","type":"session_meta","payload":{"id":"raw-session","cwd":"/secret/repo"}}`,
		`{"timestamp":"2026-08-13T09:59:59Z","session_id":"raw-session","cwd":"/secret/repo","type":"function_call","name":"before","arguments":"do not persist"}`,
		`{"timestamp":"2026-08-13T10:00:00+00:00","session_id":"raw-session","project_id":"raw-project","type":"function_call","name":"terminal","arguments":"do not persist"}`,
		`{"timestamp":"2026-08-13T10:01:00Z","sessionId":"raw-session","cwd":"/secret/repo","type":"custom_tool_call","name":"custom","input":{"prompt":"private"}}`,
		`{"timestamp":"2026-08-13T10:02:00Z","sessionId":"raw-session","cwd":"/secret/repo","type":"dynamic_tool_call","name":"mcp__server__search","arguments":{"query":"secret"}}`,
		`{"timestamp":"2026-08-13T10:03:00Z","sessionId":"raw-session","cwd":"/secret/repo","event_type":"loaded","capability_type":"skill","skill_name":"lint"}`,
		`{"timestamp":"2026-08-13T10:04:00Z","sessionId":"raw-session","cwd":"/secret/repo","event_type":"invoked","capability_type":"skill","skill_name":"lint"}`,
		`{"timestamp":"2026-08-13T10:05:00Z","sessionId":"raw-session","cwd":"/secret/repo","hook_event_name":"PostToolUse","tool_name":"mcp__server__search","tool_input":{"query":"secret"}}`,
	}, "\n"))
	adapter := New(Options{RepoRoot: repo, TranscriptRoots: []string{sessions}})
	events, err := adapter.ImportUsage(context.Background(), since)
	if err != nil {
		t.Fatalf("ImportUsage() error = %v", err)
	}
	if len(events) != 6 {
		t.Fatalf("event count = %d, want 6 (inclusive since, duplicate MCP identity deduped)", len(events))
	}
	for _, event := range events {
		if event.Timestamp.Before(since) || event.Timestamp.Location() != time.UTC {
			t.Fatalf("event timestamp = %s, expected UTC at/after since", event.Timestamp)
		}
		if event.SessionID == "raw-session" || event.ProjectID == "raw-project" || strings.Contains(event.SessionID, "secret") || strings.Contains(event.ProjectID, "secret") {
			t.Fatalf("raw identifier leaked in usage event: %#v", event)
		}
		if len(event.SessionID) != 64 || len(event.ProjectID) != 64 || len(event.Fingerprint) != 64 {
			t.Fatalf("usage hashes = %#v, want SHA-256 hex", event)
		}
		if event.CapabilityName == "before" {
			t.Fatal("pre-since event imported")
		}
	}
	if countUsage(events, domain.CapabilityMCPTool, "mcp__server__search") != 2 {
		t.Fatalf("MCP usage events = %#v, want both explicit MCP calls", events)
	}
	if countUsage(events, domain.CapabilityTool, "terminal") != 1 || countUsage(events, domain.CapabilityTool, "custom") != 1 {
		t.Fatalf("function/custom usage events = %#v", events)
	}
	if countUsage(events, domain.CapabilitySkill, "lint") != 2 {
		t.Fatalf("skill usage events = %#v, want loaded and invoked", events)
	}
	for _, event := range events {
		if event.CapabilityType == domain.CapabilitySkill && event.CapabilityName == "lint" {
			if event.EventType != domain.EventLoaded && event.EventType != domain.EventInvoked {
				t.Fatalf("skill event type = %q", event.EventType)
			}
		}
	}
}

func TestParseTOMLDeterministicMetadataSubset(t *testing.T) {
	values, err := parseTOML([]byte(`name = "reviewer"
enabled = true
args = ["--quiet", "--json"]
labels = {team = "lint", priority = 2}
instructions = """line one
line two"""
`))
	if err != nil {
		t.Fatalf("parseTOML() error = %v", err)
	}
	if name, ok := values["name"].(string); !ok || name != "reviewer" {
		t.Fatalf("name = %#v", values["name"])
	}
	if enabled, ok := values["enabled"].(bool); !ok || !enabled {
		t.Fatalf("enabled = %#v", values["enabled"])
	}
	if args, ok := values["args"].([]any); !ok || len(args) != 2 {
		t.Fatalf("args = %#v", values["args"])
	}
	if labels, ok := values["labels"].(map[string]any); !ok || labels["team"] != "lint" {
		t.Fatalf("labels = %#v", values["labels"])
	}
	if instructions, ok := values["instructions"].(string); !ok || !strings.Contains(instructions, "line two") {
		t.Fatalf("multiline instructions = %#v", values["instructions"])
	}
	if _, err := parseTOML([]byte("name = \"one\"\nname = \"two\"\n")); err == nil {
		t.Fatal("duplicate TOML key accepted")
	}
}

func TestNoImplicitLiveHome(t *testing.T) {
	adapter := New(Options{Clock: func() time.Time { return time.Unix(1, 0) }})
	discovery, err := adapter.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(discovery.Capabilities) != 0 || len(discovery.Findings) != 0 {
		t.Fatalf("empty injected roots should produce no live-home inventory: %#v", discovery)
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", path, err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	mkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

func capabilitiesByName(capabilities []domain.Capability) map[string][]domain.Capability {
	result := make(map[string][]domain.Capability)
	for _, capability := range capabilities {
		result[capability.Name] = append(result[capability.Name], capability)
	}
	return result
}

func findingCodes(findings []domain.Finding) map[string]bool {
	result := make(map[string]bool)
	for _, finding := range findings {
		result[finding.Code] = true
	}
	return result
}

func countUsage(events []domain.UsageEvent, typ domain.CapabilityType, name string) int {
	count := 0
	for _, event := range events {
		if event.CapabilityType == typ && event.CapabilityName == name {
			count++
		}
	}
	return count
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
