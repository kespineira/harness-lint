package codex

import (
	"context"
	"encoding/json"
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
	writeFile(t, filepath.Join(home, ".codex", "config.toml"), "[mcp_servers.fs]\ncommand = \"missing-mcp\"\nenabled = false\nenabled_tools = [\"read\", \"shared\"]\ndisabled_tools = [\"write\", \"shared\"]\n\n[mcp_servers.fs.tools.read]\napproval_mode = \"always\"\n")
	writeFile(t, filepath.Join(repo, ".codex", "config.toml"), "[mcp_servers.repo]\ncommand = \"known-mcp\"\nenabled = true\nenabled_tools = [\"search\"]\n\n[hooks.SessionStart]\ncommand = \"echo\"\n")
	writeFile(t, filepath.Join(home, ".codex", "hooks.json"), "{\"hooks\": {\"PostToolUse\": [{\"type\": \"command\"}]}}")
	writeFile(t, filepath.Join(repo, ".codex", "hooks.json"), "{\"PostToolUseFailure\": [{\"enabled\": false}]}")

	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.FixedZone("CEST", 2*60*60))
	lookupCalls := []string{}
	adapter := New(Options{
		UserHome:         home,
		ConfigRoot:       filepath.Join(home, ".codex"),
		ProjectRoot:      repo,
		CurrentDirectory: current,
		SystemSkillRoots: []string{system},
		Now:              func() time.Time { return now },
		LookPath: func(command string) (string, error) {
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
	for _, name := range []string{"personal", "project", "nested", "system", "reviewer", "security", "PostToolUse", "PostToolUseFailure", "SessionStart", "fs", "repo", "mcp__fs__read", "mcp__fs__write", "mcp__fs__shared", "mcp__repo__search"} {
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
		if capability.Enabled != domain.EnabledStateEnabled {
			t.Fatalf("skill %q enabled state = %q, want enabled by default", name, capability.Enabled)
		}
		if capability.Advertisement != domain.AdvertisementStateFullyAdvertised {
			t.Fatalf("skill %q advertisement = %q, want fully advertised", name, capability.Advertisement)
		}
		if capability.MetadataTokens.Confidence != domain.ConfidenceEstimated || capability.BodyTokens.Confidence != domain.ConfidenceEstimated {
			t.Fatalf("skill %q measurements = %#v/%#v, want explicit estimates", name, capability.MetadataTokens, capability.BodyTokens)
		}
		if capability.MetadataTokens.Value <= 0 {
			t.Fatalf("skill %q metadata estimate = %#v, want nonzero configured-list maximum", name, capability.MetadataTokens)
		}
		for _, phrase := range []string{"configured-list maximum", "SKILL.md path", "descriptions may be shortened", "skills omitted by budget"} {
			if !strings.Contains(capability.MetadataTokens.Basis, phrase) {
				t.Fatalf("skill %q metadata basis %q missing %q", name, capability.MetadataTokens.Basis, phrase)
			}
		}
		if capability.Hash == "" || len(capability.Hash) != 64 {
			t.Fatalf("skill %q hash = %q, want SHA-256 hex", name, capability.Hash)
		}
		if !capability.FirstSeen.Equal(now.UTC()) || !capability.LastSeen.Equal(now.UTC()) {
			t.Fatalf("skill %q timestamps = %s..%s, want injected UTC clock", name, capability.FirstSeen, capability.LastSeen)
		}
	}
	for _, name := range []string{"reviewer", "security"} {
		if capabilities[name][0].Advertisement != domain.AdvertisementStateUnknown || capabilities[name][0].MetadataTokens.Confidence != domain.ConfidenceUnknown {
			t.Fatalf("agent %q exposure = %q/%#v, want unknown", name, capabilities[name][0].Advertisement, capabilities[name][0].MetadataTokens)
		}
	}
	if capabilities["reviewer"][0].BodyTokens.Confidence != domain.ConfidenceEstimated {
		t.Fatalf("agent developer-instructions body = %#v, want estimate", capabilities["reviewer"][0].BodyTokens)
	}
	if len(capabilities["fs"]) != 1 || capabilities["fs"][0].Enabled != domain.EnabledStateDisabled || capabilities["fs"][0].Advertisement != domain.AdvertisementStateUnknown {
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
	if got := capabilities["SessionStart"][0].Enabled; got != domain.EnabledStateUnknown {
		t.Fatalf("inline hook state = %q, want unknown", got)
	}
	if len(capabilities["mcp__fs__shared"]) != 1 || capabilities["mcp__fs__shared"][0].Enabled != domain.EnabledStateDisabled {
		t.Fatalf("deny-after-allow tool state = %#v, want disabled", capabilities["mcp__fs__shared"])
	}
	if len(capabilities["mcp__fs__read"]) != 1 || capabilities["mcp__fs__read"][0].Enabled != domain.EnabledStateDisabled {
		t.Fatalf("disabled server enabled-tool state = %#v, want disabled", capabilities["mcp__fs__read"])
	}
	if len(capabilities["mcp__fs__approval_only"]) != 0 {
		t.Fatalf("approval_mode-only tool became available: %#v", capabilities["mcp__fs__approval_only"])
	}
	for _, capability := range discovery.Capabilities {
		if capability.Type == domain.CapabilityInstructionFile && capability.Advertisement != domain.AdvertisementStateFullyAdvertised {
			t.Fatalf("instruction advertisement = %q, want fully advertised", capability.Advertisement)
		}
		if capability.Type == domain.CapabilityInstructionFile && capability.BodyTokens.Confidence != domain.ConfidenceEstimated {
			t.Fatalf("instruction body measurement = %#v, want configured baseline estimate", capability.BodyTokens)
		}
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

func TestDiscoverInlineHooksIgnoresCodexHookStateRegistry(t *testing.T) {
	tests := []struct {
		name      string
		config    string
		wantHooks []string
	}{
		{
			name: "state only",
			config: `[hooks.state."/tmp/hooks.json"]
enabled = true
trusted_hash = "hash"
`,
		},
		{
			name: "state and real event",
			config: `[hooks.state."/tmp/hooks.json"]
enabled = true
trusted_hash = "hash"

[hooks.PostToolUse]
matcher = "Bash"
`,
			wantHooks: []string{"PostToolUse"},
		},
		{
			name: "unknown event remains reviewable",
			config: `[hooks.UnknownEvent]
matcher = "Bash"
`,
			wantHooks: []string{"UnknownEvent"},
		},
		{
			name: "version remains reviewable inline",
			config: `[hooks.version]
value = 1
`,
			wantHooks: []string{"version"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configRoot := filepath.Join(t.TempDir(), ".codex")
			writeFile(t, filepath.Join(configRoot, "config.toml"), test.config)
			discovery, err := New(Options{
				ConfigRoot: configRoot,
				Now:        func() time.Time { return time.Unix(22, 0) },
			}).Discover(context.Background())
			if err != nil {
				t.Fatalf("Discover() error = %v", err)
			}
			var gotHooks []string
			for _, capability := range discovery.Capabilities {
				if capability.Type == domain.CapabilityHook {
					gotHooks = append(gotHooks, capability.Name)
				}
			}
			sortStrings(gotHooks)
			sortStrings(test.wantHooks)
			if strings.Join(gotHooks, ",") != strings.Join(test.wantHooks, ",") {
				t.Fatalf("discovered inline hooks = %#v, want %#v; capabilities = %#v", gotHooks, test.wantHooks, discovery.Capabilities)
			}
		})
	}
}

func TestUserSkillConfigurationDisablesCanonicalPathOnly(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	repo := filepath.Join(root, "repo")
	configRoot := filepath.Join(home, ".codex")
	skillDir := filepath.Join(home, ".agents", "skills", "configured")
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), "---\nname: configured\ndescription: configured skill\n---\nbody\n")
	// A project config entry must not influence the user skill inventory.
	writeFile(t, filepath.Join(repo, ".codex", "config.toml"), "[[skills.config]]\npath = \""+skillDir+"\"\nenabled = false\n")
	writeFile(t, filepath.Join(configRoot, "config.toml"), "[[skills.config]]\npath = \""+skillDir+"\"\nenabled = false\n")

	adapter := New(Options{
		UserHome:    home,
		ConfigRoot:  configRoot,
		ProjectRoot: repo,
		Now:         func() time.Time { return time.Unix(20, 0) },
	})
	discovery, err := adapter.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	capabilities := capabilitiesByName(discovery.Capabilities)
	if len(capabilities["configured"]) != 1 {
		t.Fatalf("configured skill capabilities = %#v, want one", capabilities["configured"])
	}
	disabled := capabilities["configured"][0]
	if disabled.Enabled != domain.EnabledStateDisabled || disabled.Advertisement != domain.AdvertisementStateNotAdvertised {
		t.Fatalf("disabled skill state = %q/%q, want disabled/not advertised", disabled.Enabled, disabled.Advertisement)
	}
	if disabled.MetadataTokens.Value != 0 || disabled.MetadataTokens.Confidence != domain.ConfidenceObserved {
		t.Fatalf("disabled skill metadata = %#v, want observed zero", disabled.MetadataTokens)
	}

	writeFile(t, filepath.Join(configRoot, "config.toml"), "[[skills.config]]\npath = \""+filepath.Join(skillDir, "SKILL.md")+"\"\nenabled = true\n")
	discovery, err = adapter.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() after enabling error = %v", err)
	}
	enabled := capabilitiesByName(discovery.Capabilities)["configured"][0]
	if enabled.Enabled != domain.EnabledStateEnabled || enabled.Advertisement != domain.AdvertisementStateFullyAdvertised {
		t.Fatalf("enabled skill state = %q/%q, want enabled/fully advertised", enabled.Enabled, enabled.Advertisement)
	}
}

func TestSkillMetadataEstimateIncludesCanonicalPath(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	shortDir := filepath.Join(home, ".agents", "skills", "short")
	longDir := filepath.Join(home, ".agents", "skills", "skill-with-a-much-longer-canonical-directory-name")
	content := "---\nname: same\ndescription: same description\n---\nbody\n"
	writeFile(t, filepath.Join(shortDir, "SKILL.md"), content)
	writeFile(t, filepath.Join(longDir, "SKILL.md"), content)
	discovery, err := New(Options{UserHome: home, Now: func() time.Time { return time.Unix(21, 0) }}).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	var shortEstimate, longEstimate int64
	for _, capability := range discovery.Capabilities {
		if capability.Type != domain.CapabilitySkill || capability.Name != "same" {
			continue
		}
		if strings.Contains(capability.Source, "skill-with-a-much-longer") {
			longEstimate = capability.MetadataTokens.Value
		} else if strings.Contains(capability.Source, string(filepath.Separator)+"short"+string(filepath.Separator)) {
			shortEstimate = capability.MetadataTokens.Value
		}
	}
	if shortEstimate == 0 || longEstimate == 0 || longEstimate <= shortEstimate {
		t.Fatalf("path-inclusive metadata estimates = short %d, long %d; want long path to increase estimate", shortEstimate, longEstimate)
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
	adapter := New(Options{UserHome: home, ProjectRoot: repo, CurrentDirectory: repo, Now: func() time.Time { return time.Unix(10, 0) }})
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
	for _, capability := range discovery.Capabilities {
		if capability.Type == domain.CapabilitySkill && capability.Source == filepath.Join(projectSkill, "SKILL.md") {
			if capability.Enabled != domain.EnabledStateUnknown || capability.Advertisement != domain.AdvertisementStateUnknown || capability.MetadataTokens.Confidence != domain.ConfidenceUnknown {
				t.Fatalf("malformed skill state = %#v, want unknown/unknown/unknown", capability)
			}
		}
	}
}

func TestImportUsagePrivacyExplicitSignalsAndInclusiveSince(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	sessions := filepath.Join(root, "sessions")
	mkdir(t, sessions)
	since := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	hookCapture := filepath.Join(root, "hook-events.json")
	writeFile(t, filepath.Join(sessions, "session.jsonl"), strings.Join([]string{
		`{"timestamp":"2026-08-13T09:59:00Z","type":"session_meta","payload":{"id":"raw-session","cwd":"/secret/repo"}}`,
		`{"timestamp":"2026-08-13T09:59:59Z","session_id":"raw-session","cwd":"/secret/repo","type":"function_call","name":"before","arguments":"do not persist"}`,
		`{"timestamp":"2026-08-13T10:00:00+00:00","session_id":"raw-session","project_id":"raw-project","type":"response_item","payload":{"type":"function_call","name":"terminal","arguments":"do not persist"}}`,
		`{"timestamp":"2026-08-13T10:01:00Z","sessionId":"raw-session","cwd":"/secret/repo","type":"response_item","payload":{"type":"custom_tool_call","name":"custom","input":{"prompt":"private"}}}`,
		`{"timestamp":"2026-08-13T10:02:00Z","sessionId":"raw-session","cwd":"/secret/repo","type":"response_item","payload":{"type":"dynamic_tool_call","name":"mcp__server__search","arguments":{"query":"secret"}}}`,
		`{"timestamp":"2026-08-13T10:03:00Z","sessionId":"raw-session","cwd":"/secret/repo","type":"message","payload":{"type":"function_call","name":"fake"},"prompt":{"type":"function_call","name":"also-fake"}}`,
		`{"sessionId":"raw-session","cwd":"/secret/repo","type":"function_call","name":"missing-timestamp"}`,
		`{"timestamp":"2026-08-13T10:05:00Z","sessionId":"raw-session","cwd":"/secret/repo","event_type":"loaded","capability_type":"skill","skill_name":"lint"}`,
	}, "\n"))
	writeFile(t, hookCapture, `{"timestamp":"2026-08-13T10:06:00Z","session_id":"raw-session","cwd":"/secret/repo","hook_event_name":"PostToolUse","tool_name":"mcp__server__search","tool_input":{"query":"secret"}}`)
	adapter := New(Options{ProjectRoot: repo, TranscriptRoots: []string{sessions}, HookEventPaths: []string{hookCapture}})
	events, err := adapter.ImportUsage(context.Background(), since)
	if err != nil {
		t.Fatalf("ImportUsage() error = %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("event count = %d, want 4 (inclusive since, explicit signals only)", len(events))
	}
	for _, event := range events {
		if event.EffectiveActivityTime().Before(since) || event.EffectiveActivityTime().Location() != time.UTC {
			t.Fatalf("event activity time = %s, expected UTC at/after since", event.EffectiveActivityTime())
		}
		if event.Provenance != domain.ProvenanceTranscript && event.Provenance != domain.ProvenanceImport {
			t.Fatalf("unexpected Codex event provenance: %#v", event)
		}
		if event.SchemaVersion != domain.CurrentUsageEventSchemaVersion || event.ObservedAt.IsZero() {
			t.Fatalf("Codex event contract fields = %#v", event)
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
		t.Fatalf("MCP usage events = %#v, want rollout and PostToolUse calls", events)
	}
	if countUsage(events, domain.CapabilityTool, "terminal") != 1 || countUsage(events, domain.CapabilityTool, "custom") != 1 {
		t.Fatalf("function/custom usage events = %#v", events)
	}
	if countUsage(events, domain.CapabilitySkill, "lint") != 0 {
		t.Fatalf("fabricated skill usage events = %#v, want unknown/skipped", events)
	}
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal usage event: %v", err)
		}
		for _, secret := range []string{"do not persist", "private", "secret"} {
			if strings.Contains(string(encoded), secret) {
				t.Fatalf("raw usage payload leaked %q in %s", secret, encoded)
			}
		}
	}
}

func TestNoImplicitLiveHome(t *testing.T) {
	adapter := New(Options{Now: func() time.Time { return time.Unix(1, 0) }})
	discovery, err := adapter.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(discovery.Capabilities) != 0 || len(discovery.Findings) != 0 {
		t.Fatalf("empty injected roots should produce no live-home inventory: %#v", discovery)
	}
}

func TestMCPToolIdentityPreservesDoubleUnderscoresInToolName(t *testing.T) {
	for _, name := range []string{"mcp__server__search", "mcp__server__tool__with__underscores"} {
		if !isMCPToolIdentity(name) {
			t.Fatalf("isMCPToolIdentity(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"server__search", "mcp__server", "mcp____search"} {
		if isMCPToolIdentity(name) {
			t.Fatalf("isMCPToolIdentity(%q) = true, want false", name)
		}
	}
}

func TestMCPServerEnabledDefaultsAndDisabledFilters(t *testing.T) {
	root := t.TempDir()
	configRoot := filepath.Join(root, ".codex")
	writeFile(t, filepath.Join(configRoot, "config.toml"), "[mcp_servers.default]\nenabled_tools = [\"read\"]\n\n[mcp_servers.off]\nenabled = false\nenabled_tools = [\"read\"]\ndisabled_tools = [\"write\"]\n")
	discovery, err := New(Options{
		ConfigRoot: configRoot,
		Now:        func() time.Time { return time.Unix(30, 0) },
	}).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	capabilities := capabilitiesByName(discovery.Capabilities)
	for _, name := range []string{"default", "mcp__default__read", "off", "mcp__off__read", "mcp__off__write"} {
		if len(capabilities[name]) != 1 {
			t.Fatalf("missing capability %q in %#v", name, discovery.Capabilities)
		}
	}
	if capabilities["default"][0].Enabled != domain.EnabledStateEnabled || capabilities["mcp__default__read"][0].Enabled != domain.EnabledStateEnabled {
		t.Fatalf("default MCP states = %#v/%#v, want enabled", capabilities["default"][0], capabilities["mcp__default__read"][0])
	}
	if capabilities["off"][0].Enabled != domain.EnabledStateDisabled || capabilities["mcp__off__read"][0].Enabled != domain.EnabledStateDisabled || capabilities["mcp__off__write"][0].Enabled != domain.EnabledStateDisabled {
		t.Fatalf("disabled MCP states = %#v/%#v/%#v, want disabled", capabilities["off"][0], capabilities["mcp__off__read"][0], capabilities["mcp__off__write"][0])
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
