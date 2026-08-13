package claude

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
)

func TestDiscoverInventoryFrontmatterNestedMCPHooksAgentsInstructions(t *testing.T) {
	root := t.TempDir()
	userHome := filepath.Join(root, "user")
	projectRoot := filepath.Join(root, "project")
	currentDirectory := filepath.Join(projectRoot, "packages", "app")
	mustMkdirAll(t, filepath.Join(userHome, ".claude", "skills", "personal"))
	mustMkdirAll(t, filepath.Join(projectRoot, ".claude", "skills", "nested", "deep"))
	mustMkdirAll(t, filepath.Join(projectRoot, ".claude", "skills", "nested", "shared"))
	mustMkdirAll(t, filepath.Join(projectRoot, ".claude", "commands", "legacy"))
	mustMkdirAll(t, filepath.Join(userHome, ".claude", "agents"))
	mustMkdirAll(t, filepath.Join(projectRoot, ".claude", "agents"))
	mustMkdirAll(t, currentDirectory)

	userSkill := filepath.Join(userHome, ".claude", "skills", "personal", "SKILL.md")
	projectSkill := filepath.Join(projectRoot, ".claude", "skills", "nested", "deep", "SKILL.md")
	mustWrite(t, userSkill, "---\nname: shared\ndescription: user skill\n---\nNever expose this body.\n")
	mustWrite(t, projectSkill, "---\ndescription: nested skill\n---\nNested instructions.\n")
	mustWrite(t, filepath.Join(projectRoot, ".claude", "skills", "nested", "shared", "SKILL.md"), "---\nname: shared\ndescription: project skill\n---\nProject copy.\n")
	mustWrite(t, filepath.Join(projectRoot, ".claude", "commands", "legacy", "deploy.md"), "---\ndescription: legacy command\n---\nDeploy.\n")
	mustWrite(t, filepath.Join(userHome, ".claude", "agents", "reviewer.md"), "---\nname: reviewer\ndescription: reviews\n---\nReview agent.\n")
	mustWrite(t, filepath.Join(projectRoot, ".claude", "agents", "builder.md"), "---\nname: builder\ndescription: builds\n---\nBuild agent.\n")
	mustWrite(t, filepath.Join(userHome, ".claude", "CLAUDE.md"), "User instruction that must not be returned as a body.\n")
	mustWrite(t, filepath.Join(projectRoot, "CLAUDE.md"), "Project instructions.\n")
	mustWrite(t, filepath.Join(currentDirectory, "CLAUDE.local.md"), "Local instructions.\n")

	mustWrite(t, filepath.Join(userHome, ".claude", "settings.json"), `{
  "skillOverrides": {"shared": "off"},
  "hooks": {"PostToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "audit-hook"}]}]}
}`)
	mustWrite(t, filepath.Join(projectRoot, ".claude", "settings.json"), `{
  "hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "init-hook"}]}]}
}`)
	mustWrite(t, filepath.Join(projectRoot, ".mcp.json"), `{
  "mcpServers": {
    "missing-local": {"type": "stdio", "command": "does-not-exist", "args": ["--ignored"]},
    "remote": {"type": "http", "url": "https://example.invalid/mcp"}
  }
}`)
	mustWrite(t, filepath.Join(userHome, ".claude.json"), `{
  "mcpServers": {"user-server": {"type": "http", "url": "https://example.invalid/user"}},
  "projects": {
    "`+currentDirectory+`": {
      "mcpServers": {"local-server": {"type": "stdio", "command": "local-tool"}},
      "disabledMcpServers": ["local-server"]
    }
  }
}`)

	lookupCalls := make([]string, 0, 2)
	clock := time.Date(2026, 8, 13, 15, 0, 0, 123000000, time.FixedZone("CEST", 2*60*60))
	adapter := New(Options{
		UserHome:         userHome,
		ProjectRoot:      projectRoot,
		CurrentDirectory: currentDirectory,
		Now:              func() time.Time { return clock },
		LookPath: func(command string) (string, error) {
			lookupCalls = append(lookupCalls, command)
			if command == "local-tool" {
				return "/bin/local-tool", nil
			}
			return "", errors.New("not found")
		},
	})
	discovery, err := adapter.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if err := discovery.Validate(); err != nil {
		t.Fatalf("Discovery.Validate() error = %v", err)
	}
	if adapter.Runtime() != domain.RuntimeClaudeCode {
		t.Fatalf("Runtime() = %q, want %q", adapter.Runtime(), domain.RuntimeClaudeCode)
	}
	if strings.Join(lookupCalls, ",") != "does-not-exist,local-tool" {
		t.Fatalf("command lookup calls = %v, want deterministic configured commands", lookupCalls)
	}

	findCapability := func(typ domain.CapabilityType, name string) domain.Capability {
		for _, capability := range discovery.Capabilities {
			if capability.Type == typ && capability.Name == name {
				return capability
			}
		}
		t.Fatalf("missing capability type=%q name=%q; got %#v", typ, name, discovery.Capabilities)
		return domain.Capability{}
	}
	findCapabilityScope := func(typ domain.CapabilityType, name string, scope domain.Scope) domain.Capability {
		for _, capability := range discovery.Capabilities {
			if capability.Type == typ && capability.Name == name && capability.Scope == scope {
				return capability
			}
		}
		t.Fatalf("missing capability type=%q name=%q scope=%q; got %#v", typ, name, scope, discovery.Capabilities)
		return domain.Capability{}
	}
	shared := findCapabilityScope(domain.CapabilitySkill, "shared", domain.ScopeUser)
	if shared.Scope != domain.ScopeUser || shared.Enabled != domain.EnabledStateDisabled {
		t.Fatalf("shared skill scope/state = %q/%q, want user/disabled", shared.Scope, shared.Enabled)
	}
	if shared.MetadataTokens.Value != 0 || shared.MetadataTokens.Confidence != domain.ConfidenceObserved || shared.BodyTokens.Confidence != domain.ConfidenceEstimated {
		t.Fatalf("hidden skill measurements = %#v/%#v, want zero configured metadata and separate body estimate", shared.MetadataTokens, shared.BodyTokens)
	}
	if shared.MetadataTokens.Basis != hiddenMetadataBasis || shared.BodyTokens.Basis != tokenEstimateBasis {
		t.Fatalf("skill measurement basis = %#v/%#v, want configured-hidden and documented body estimate", shared.MetadataTokens, shared.BodyTokens)
	}
	if shared.Hash != hashBytes([]byte("---\nname: shared\ndescription: user skill\n---\nNever expose this body.\n")) {
		t.Fatalf("skill hash = %q, want content SHA-256", shared.Hash)
	}
	if strings.Contains(shared.Hash, "Never expose") {
		t.Fatal("skill hash retained body text")
	}
	nested := findCapability(domain.CapabilitySkill, "nested/deep")
	if nested.Scope != domain.ScopeProject {
		t.Fatalf("nested skill scope = %q, want project", nested.Scope)
	}
	_ = findCapability(domain.CapabilityCommand, "legacy/deploy")
	reviewer := findCapability(domain.CapabilityAgent, "reviewer")
	if reviewer.Enabled != domain.EnabledStateUnknown || reviewer.Advertisement != domain.AdvertisementStateFullyAdvertised {
		t.Fatalf("reviewer agent exposure = %q/%q, want unknown/fully advertised", reviewer.Enabled, reviewer.Advertisement)
	}
	if reviewer.MetadataTokens.Confidence != domain.ConfidenceEstimated || reviewer.MetadataTokens.Value == 0 || reviewer.BodyTokens.Confidence != domain.ConfidenceEstimated || reviewer.BodyTokens.Value == 0 {
		t.Fatalf("reviewer agent measurements = %#v/%#v, want separate metadata/body estimates", reviewer.MetadataTokens, reviewer.BodyTokens)
	}
	if !strings.Contains(reviewer.MetadataTokens.Basis, "configured maximum estimate") || strings.Contains(reviewer.MetadataTokens.Basis, "runtime cost") == false {
		t.Fatalf("reviewer metadata basis = %q, want explicit non-runtime-cost estimate", reviewer.MetadataTokens.Basis)
	}
	_ = findCapability(domain.CapabilityAgent, "builder")
	for _, capability := range discovery.Capabilities {
		if capability.Type != domain.CapabilityInstructionFile {
			continue
		}
		if capability.Enabled != domain.EnabledStateEnabled || capability.Advertisement != domain.AdvertisementStateFullyAdvertised {
			t.Fatalf("loaded instruction %q exposure = %q/%q, want enabled/fully advertised", capability.Name, capability.Enabled, capability.Advertisement)
		}
	}
	_ = findCapability(domain.CapabilityInstructionFile, "CLAUDE.md")
	_ = findCapability(domain.CapabilityInstructionFile, "CLAUDE.local.md")
	hook := findCapability(domain.CapabilityHook, "PostToolUse:Bash")
	if hook.Enabled != domain.EnabledStateUnknown {
		t.Fatalf("hook enabled state = %q, want unknown without explicit state", hook.Enabled)
	}
	remote := findCapability(domain.CapabilityMCPServer, "remote")
	if remote.Enabled != domain.EnabledStateUnknown {
		t.Fatalf("remote MCP state = %q, want unknown", remote.Enabled)
	}
	local := findCapability(domain.CapabilityMCPServer, "local-server")
	if local.Enabled != domain.EnabledStateDisabled {
		t.Fatalf("local MCP state = %q, want disabled from explicit config", local.Enabled)
	}

	assertFindingCode(t, discovery.Findings, "mcp-command-unresolved")
	assertFindingCode(t, discovery.Findings, "duplicate-capability")
}

func TestDiscoverFindingsMalformedConfigAndBrokenSymlinkAreDeterministic(t *testing.T) {
	root := t.TempDir()
	projectRoot := filepath.Join(root, "project")
	userHome := filepath.Join(root, "user")
	mustMkdirAll(t, filepath.Join(projectRoot, ".claude", "skills", "bad"))
	mustMkdirAll(t, filepath.Join(userHome, ".claude", "skills"))
	mustWrite(t, filepath.Join(projectRoot, ".claude", "skills", "bad", "SKILL.md"), "---\nname: bad\ndescription: missing close\nThis line is not body.\n")
	mustWrite(t, filepath.Join(projectRoot, ".mcp.json"), "{\"mcpServers\": [1]}")
	mustWrite(t, filepath.Join(userHome, ".claude", "settings.json"), "{not-json")
	broken := filepath.Join(projectRoot, ".claude", "skills", "broken")
	if err := os.Symlink(filepath.Join(root, "missing-target"), broken); err != nil {
		t.Skipf("symlinks unavailable in fixture filesystem: %v", err)
	}

	adapter := New(Options{
		UserHome:    userHome,
		ProjectRoot: projectRoot,
		Now:         func() time.Time { return time.Date(2026, 8, 13, 15, 0, 0, 0, time.UTC) },
		LookPath:    func(string) (string, error) { return "", errors.New("not looked up") },
	})
	first, err := adapter.Discover(context.Background())
	if err != nil {
		t.Fatalf("first Discover() error = %v", err)
	}
	second, err := adapter.Discover(context.Background())
	if err != nil {
		t.Fatalf("second Discover() error = %v", err)
	}
	if got, want := stableDiscoveryJSON(t, first), stableDiscoveryJSON(t, second); got != want {
		t.Fatalf("discovery output is not deterministic:\nfirst=%s\nsecond=%s", got, want)
	}
	assertFindingCode(t, first.Findings, "malformed-frontmatter")
	assertFindingCode(t, first.Findings, "malformed-config")
	assertFindingCode(t, first.Findings, "broken-symlink")
}

func TestDiscoverAgentExposureRequiresValidSelectionDescription(t *testing.T) {
	root := t.TempDir()
	projectRoot := filepath.Join(root, "project")
	agents := filepath.Join(projectRoot, ".claude", "agents")
	mustWrite(t, filepath.Join(agents, "valid.md"), "---\nname: valid\ndescription: delegates valid work\nmodel: sonnet\ntools: Read, Glob\n---\nValid agent system prompt.\n")
	mustWrite(t, filepath.Join(agents, "missing-description.md"), "---\nname: missing-description\n---\nAgent body is not a selection description.\n")
	mustWrite(t, filepath.Join(agents, "malformed.md"), "---\nname: malformed\ndescription: missing close\nAgent body.\n")

	discovery, err := New(Options{
		ProjectRoot: projectRoot,
		Now:         func() time.Time { return time.Date(2026, 8, 13, 15, 0, 0, 0, time.UTC) },
	}).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	find := func(name string) domain.Capability {
		t.Helper()
		for _, capability := range discovery.Capabilities {
			if capability.Type == domain.CapabilityAgent && capability.Name == name {
				return capability
			}
		}
		t.Fatalf("agent %q not found: %#v", name, discovery.Capabilities)
		return domain.Capability{}
	}
	valid := find("valid")
	if valid.Enabled != domain.EnabledStateUnknown || valid.Advertisement != domain.AdvertisementStateFullyAdvertised || valid.MetadataTokens.Confidence != domain.ConfidenceEstimated || valid.BodyTokens.Confidence != domain.ConfidenceEstimated {
		t.Fatalf("valid agent = %#v, want unknown enabled, fully advertised metadata, and body estimates", valid)
	}
	if valid.MetadataTokens.Value != estimateTokens([]byte("valid\ndelegates valid work")) {
		t.Fatalf("valid agent metadata estimate = %d, want name+description estimate", valid.MetadataTokens.Value)
	}
	if valid.BodyTokens.Value != estimateTokens([]byte("Valid agent system prompt.\n")) {
		t.Fatalf("valid agent body estimate = %d, want separate system-prompt estimate", valid.BodyTokens.Value)
	}
	if !strings.Contains(valid.BodyTokens.Basis, "on-invocation") || !strings.Contains(valid.BodyTokens.Basis, "not runtime cost") {
		t.Fatalf("valid agent body basis = %q, want explicit on-invocation non-runtime-cost basis", valid.BodyTokens.Basis)
	}
	for _, name := range []string{"missing-description", "malformed"} {
		capability := find(name)
		if capability.Enabled != domain.EnabledStateUnknown || capability.Advertisement != domain.AdvertisementStateUnknown || capability.MetadataTokens.Confidence != domain.ConfidenceUnknown {
			t.Fatalf("agent %q exposure/metadata = %q/%q/%#v, want unknown", name, capability.Enabled, capability.Advertisement, capability.MetadataTokens)
		}
	}
	missingDescription := find("missing-description")
	if missingDescription.BodyTokens.Confidence != domain.ConfidenceUnknown {
		t.Fatalf("missing-description body measurement = %#v, want unknown with unusable selection description", missingDescription.BodyTokens)
	}
	malformed := find("malformed")
	if malformed.BodyTokens.Confidence != domain.ConfidenceUnknown {
		t.Fatalf("malformed body measurement = %#v, want unknown after malformed frontmatter", malformed.BodyTokens)
	}
	assertFindingCode(t, discovery.Findings, "malformed-frontmatter")
}

func TestDiscoverSkillAdvertisementStatesAndEffectiveProjectPrecedence(t *testing.T) {
	root := t.TempDir()
	userHome := filepath.Join(root, "user")
	projectRoot := filepath.Join(root, "project")
	userSkills := filepath.Join(userHome, ".claude", "skills")
	projectSkills := filepath.Join(projectRoot, ".claude", "skills")
	for _, name := range []string{"shared", "name-only", "user-only", "off", "manual", "replaced"} {
		mustMkdirAll(t, filepath.Join(userSkills, name))
	}
	for _, name := range []string{"shared", "name-only", "user-only", "off", "manual", "replaced"} {
		mustMkdirAll(t, filepath.Join(projectSkills, name))
	}
	writeSkill := func(root, name, extra string) {
		mustWrite(t, filepath.Join(root, name, "SKILL.md"), "---\nname: "+name+"\ndescription: description for "+name+"\n"+extra+"---\nPrivate body for "+name+".\n")
	}
	for _, name := range []string{"shared", "name-only", "user-only", "off", "manual", "replaced"} {
		writeSkill(userSkills, name, "")
		writeSkill(projectSkills, name, "")
	}
	writeSkill(userSkills, "manual", "disable-model-invocation: true\n")
	writeSkill(projectSkills, "manual", "disable-model-invocation: true\n")
	writeSkill(userSkills, "replaced", "disable-model-invocation: true\n")
	writeSkill(projectSkills, "replaced", "disable-model-invocation: true\n")
	mustWrite(t, filepath.Join(userHome, ".claude", "settings.json"), `{"skillOverrides":{"shared":"off","name-only":"name-only","user-only":"on","off":"on"}}`)
	mustWrite(t, filepath.Join(projectRoot, ".claude", "settings.json"), `{"skillOverrides":{"shared":"on","name-only":"name-only","user-only":"user-invocable-only","off":"off","replaced":"on"}}`)

	adapter := New(Options{
		UserHome:    userHome,
		ProjectRoot: projectRoot,
		Now:         func() time.Time { return time.Date(2026, 8, 13, 15, 0, 0, 0, time.UTC) },
		LookPath:    func(string) (string, error) { return "", errors.New("not found") },
	})
	discovery, err := adapter.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	find := func(name string, scope domain.Scope) domain.Capability {
		for _, capability := range discovery.Capabilities {
			if capability.Type == domain.CapabilitySkill && capability.Name == name && capability.Scope == scope {
				return capability
			}
		}
		t.Fatalf("skill %q at scope %q not found: %#v", name, scope, discovery.Capabilities)
		return domain.Capability{}
	}
	assertExposure := func(name string, scope domain.Scope, enabled domain.EnabledState, advertisement domain.AdvertisementState) {
		t.Helper()
		capability := find(name, scope)
		if capability.Enabled != enabled || capability.Advertisement != advertisement {
			t.Fatalf("skill %q/%q exposure = %q/%q, want %q/%q", name, scope, capability.Enabled, capability.Advertisement, enabled, advertisement)
		}
	}
	// Project settings have higher precedence and apply by effective name even
	// to the user-scoped copy of the same skill.
	assertExposure("shared", domain.ScopeUser, domain.EnabledStateEnabled, domain.AdvertisementStateFullyAdvertised)
	assertExposure("shared", domain.ScopeProject, domain.EnabledStateEnabled, domain.AdvertisementStateFullyAdvertised)
	assertExposure("name-only", domain.ScopeUser, domain.EnabledStateEnabled, domain.AdvertisementStateNameOnly)
	assertExposure("user-only", domain.ScopeProject, domain.EnabledStateEnabled, domain.AdvertisementStateNotAdvertised)
	assertExposure("off", domain.ScopeProject, domain.EnabledStateDisabled, domain.AdvertisementStateNotAdvertised)
	assertExposure("manual", domain.ScopeProject, domain.EnabledStateEnabled, domain.AdvertisementStateNotAdvertised)
	assertExposure("replaced", domain.ScopeProject, domain.EnabledStateEnabled, domain.AdvertisementStateFullyAdvertised)

	manual := find("manual", domain.ScopeProject)
	if manual.MetadataTokens.Value != 0 || manual.MetadataTokens.Confidence != domain.ConfidenceObserved || manual.MetadataTokens.Basis != hiddenMetadataBasis {
		t.Fatalf("manual hidden metadata = %#v, want zero observed configured basis", manual.MetadataTokens)
	}
	replaced := find("replaced", domain.ScopeProject)
	if replaced.MetadataTokens.Confidence != domain.ConfidenceEstimated ||
		!strings.Contains(replaced.MetadataTokens.Basis, "configured maximum estimate") ||
		!strings.Contains(replaced.MetadataTokens.Basis, "Claude may collapse descriptions to name-only") ||
		!strings.Contains(replaced.MetadataTokens.Basis, "skills context/character budget") ||
		!strings.Contains(replaced.MetadataTokens.Basis, "not runtime cost") {
		t.Fatalf("replaced metadata measurement = %#v, want explicit budget caveat", replaced.MetadataTokens)
	}
	if replaced.MetadataTokens.Value == 0 || replaced.BodyTokens.Value == 0 {
		t.Fatalf("replaced advertised measurements = %#v/%#v, want separate nonzero estimates", replaced.MetadataTokens, replaced.BodyTokens)
	}
}

func TestDiscoverRejectsInventedDefaultSkillOverride(t *testing.T) {
	root := t.TempDir()
	projectRoot := filepath.Join(root, "project")
	mustWrite(t, filepath.Join(projectRoot, ".claude", "skills", "example", "SKILL.md"), "---\nname: example\ndescription: example skill\n---\nBody.\n")
	mustWrite(t, filepath.Join(projectRoot, ".claude", "settings.json"), `{"skillOverrides":{"example":"default"}}`)

	discovery, err := New(Options{
		ProjectRoot: projectRoot,
		Now:         func() time.Time { return time.Date(2026, 8, 13, 15, 0, 0, 0, time.UTC) },
	}).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	assertFindingCode(t, discovery.Findings, "malformed-config")
	for _, capability := range discovery.Capabilities {
		if capability.Type == domain.CapabilitySkill && capability.Name == "example" {
			if capability.Enabled != domain.EnabledStateUnknown || capability.Advertisement != domain.AdvertisementStateUnknown {
				t.Fatalf("invented default override produced exposure = %q/%q, want unknown/unknown", capability.Enabled, capability.Advertisement)
			}
			return
		}
	}
	t.Fatal("example skill was not discovered")
}

func TestImportUsageTranscriptHookPrivacyAndSinceInclusiveUTC(t *testing.T) {
	root := t.TempDir()
	projectRoot := filepath.Join(root, "project")
	transcriptRoot := filepath.Join(root, "transcripts")
	hookRoot := filepath.Join(root, "hooks")
	mustMkdirAll(t, projectRoot)
	mustMkdirAll(t, transcriptRoot)
	mustMkdirAll(t, hookRoot)
	boundary := "2026-08-13T13:00:00.123Z"
	before := "2026-08-13T12:59:59.999Z"
	mustWrite(t, filepath.Join(transcriptRoot, "session-1.jsonl"), `{"type":"assistant","timestamp":"`+boundary+`","session_id":"session-raw","cwd":"`+projectRoot+`","message":{"content":[{"type":"tool_use","name":"Skill","input":{"skill":"review","args":"secret prompt must not escape"}},{"type":"tool_use","name":"Agent","input":{"subagent_type":"Explore","prompt":"model output must not escape"}},{"type":"tool_use","name":"mcp__github__list_issues","input":{"query":"tool output must not escape"}},{"type":"tool_use","name":"Bash","input":{"command":"cat secret"}}]}}
{"type":"assistant","timestamp":"`+before+`","session_id":"session-raw","cwd":"`+projectRoot+`","message":{"content":[{"type":"tool_use","name":"OldTool","input":{}}]}}`)
	mustWrite(t, filepath.Join(hookRoot, "post-tool-use.json"), `{
  "hook_event_name": "PostToolUse",
  "timestamp": "`+boundary+`",
  "session_id": "hook-session",
  "cwd": "`+projectRoot+`",
  "tool_name": "mcp__github__create_issue",
  "tool_input": {"title": "secret hook argument"}
}`)

	clock := time.Date(2026, 8, 13, 13, 0, 0, 123000000, time.UTC)
	adapter := New(Options{
		ProjectRoot:      projectRoot,
		CurrentDirectory: projectRoot,
		TranscriptRoots:  []string{transcriptRoot},
		HookEventPaths:   []string{hookRoot},
		Now:              func() time.Time { return clock },
	})
	events, err := adapter.ImportUsage(context.Background(), clock)
	if err != nil {
		t.Fatalf("ImportUsage() error = %v", err)
	}
	if len(events) != 5 {
		t.Fatalf("event count = %d, want boundary transcript tools plus hook", len(events))
	}
	seen := make(map[string]domain.UsageEvent, len(events))
	for _, event := range events {
		if err := event.Validate(); err != nil {
			t.Fatalf("invalid imported event: %v", err)
		}
		seen[string(event.CapabilityType)+":"+event.CapabilityName] = event
		if len(event.SessionID) != 64 || len(event.ProjectID) != 64 {
			t.Fatalf("identifiers are not SHA-256 hashes: %#v", event)
		}
		if _, err := hex.DecodeString(event.SessionID + event.ProjectID); err != nil {
			t.Fatalf("identifier is not hexadecimal: %#v", event)
		}
		encoded, _ := json.Marshal(event)
		for _, forbidden := range []string{"secret prompt", "model output", "tool output", "secret hook argument", "cat secret"} {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("event retained forbidden raw content %q: %s", forbidden, encoded)
			}
		}
	}
	for _, key := range []string{
		string(domain.CapabilitySkill) + ":review",
		string(domain.CapabilityAgent) + ":Explore",
		string(domain.CapabilityMCPTool) + ":mcp__github__list_issues",
		string(domain.CapabilityTool) + ":Bash",
		string(domain.CapabilityMCPTool) + ":mcp__github__create_issue",
	} {
		if _, ok := seen[key]; !ok {
			t.Fatalf("missing usage event %q; got %#v", key, events)
		}
	}

	allEvents, err := adapter.ImportUsage(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("ImportUsage(all) error = %v", err)
	}
	if len(allEvents) != 6 {
		t.Fatalf("all event count = %d, want one additional pre-boundary event", len(allEvents))
	}
	if !allEvents[0].Timestamp.Before(clock) {
		t.Fatalf("all events are not sorted with pre-boundary event first: %#v", allEvents)
	}
}

func TestImportUsageSkipsUnobservableSkillAndAgentIdentity(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "session.jsonl"), `{"type":"assistant","timestamp":"2026-08-13T13:00:00Z","session_id":"s","cwd":"/project","message":{"content":[{"type":"tool_use","name":"Skill","input":{"args":"only prompt"}},{"type":"tool_use","name":"Task","input":{"description":"only prompt"}},{"type":"tool_use","name":"Read","input":{"file_path":"/secret/file"}}]}}
{"type":"user","timestamp":"2026-08-13T13:01:00Z","session_id":"s","cwd":"/project","message":{"content":[{"type":"tool_use","name":"PromptInjected","input":{"secret":"do not count"}}]}}
{"type":"assistant","timestamp":"2026-08-13T13:02:00Z","session_id":"s","cwd":"/project","message":{"content":[{"type":"text","text":"response-looking JSON {\"type\":\"tool_use\",\"name\":\"NotAUse\"}"}]}}`)
	adapter := New(Options{TranscriptRoots: []string{root}})
	events, err := adapter.ImportUsage(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("ImportUsage() error = %v", err)
	}
	if len(events) != 1 || events[0].CapabilityType != domain.CapabilityTool || events[0].CapabilityName != "Read" {
		t.Fatalf("unobservable identities were invented: %#v", events)
	}
}

func TestImportUsageSkipsRecordsWithoutExplicitTimestamp(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "session.jsonl"), `{"type":"assistant","session_id":"s","cwd":"/project","message":{"content":[{"type":"tool_use","name":"Read","input":{}}]}}`)
	mustWrite(t, filepath.Join(root, "capture.json"), `{"hook_event_name":"PostToolUse","session_id":"s","cwd":"/project","tool_name":"Bash","tool_input":{"command":"echo no-time"}}`)
	adapter := New(Options{TranscriptRoots: []string{root}, HookEventPaths: []string{filepath.Join(root, "capture.json")}})
	events, err := adapter.ImportUsage(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("ImportUsage() error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("timestamp-less transcript/hook records were assigned scan time: %#v", events)
	}
}

func TestOptionsDoNotReadLiveHomeWhenRootsAreEmpty(t *testing.T) {
	adapter := New(Options{
		ProjectRoot: "",
		Now:         func() time.Time { return time.Date(2026, 8, 13, 15, 0, 0, 0, time.UTC) },
	})
	discovery, err := adapter.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(discovery.Capabilities) != 0 || len(discovery.Findings) != 0 {
		t.Fatalf("empty roots unexpectedly inspected local files: %#v", discovery)
	}
	events, err := adapter.ImportUsage(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("ImportUsage() error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("empty roots unexpectedly imported usage: %#v", events)
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", path, err)
	}
}

func mustWrite(t *testing.T, path, contents string) {
	t.Helper()
	mustMkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

func assertFindingCode(t *testing.T, findings []domain.Finding, code string) {
	t.Helper()
	for _, finding := range findings {
		if finding.Code == code {
			return
		}
	}
	t.Fatalf("finding code %q missing from %#v", code, findings)
}

func stableDiscoveryJSON(t *testing.T, discovery domain.Discovery) string {
	t.Helper()
	encoded, err := json.Marshal(discovery)
	if err != nil {
		t.Fatalf("Marshal(discovery): %v", err)
	}
	return string(encoded)
}
