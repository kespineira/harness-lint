package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func resolvedManager(runtime Runtime, root string) Manager {
	return New(runtime, Options{
		ConfigRoot: root,
		LookPath: func(command string) (string, error) {
			if command != BinaryName {
				return "", errors.New("unexpected lookup")
			}
			return "/usr/local/bin/harness-lint", nil
		},
	})
}

func unresolvedManager(runtime Runtime, root string) Manager {
	return New(runtime, Options{
		ConfigRoot: root,
		LookPath:   func(string) (string, error) { return "", os.ErrNotExist },
	})
}

func writeHookConfig(t *testing.T, root string, runtime Runtime, content string) string {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, configName(runtime))
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	return path
}

func configName(runtime Runtime) string {
	if runtime == RuntimeClaude {
		return claudeConfigName
	}
	return codexConfigName
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("generated JSON is malformed: %v\n%s", err, data)
	}
	return value
}

func asJSONObject(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value %#v is not an object", value)
	}
	return result
}

func asJSONArray(t *testing.T, value any) []any {
	t.Helper()
	result, ok := value.([]any)
	if !ok {
		t.Fatalf("value %#v is not an array", value)
	}
	return result
}

func snapshotTree(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel != "." {
			paths = append(paths, rel)
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	sort.Strings(paths)
	return paths
}

func TestInstallCleanConfigProducesOfficialShapeForBothRuntimes(t *testing.T) {
	for _, runtime := range []Runtime{RuntimeClaude, RuntimeCodex} {
		t.Run(string(runtime), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "config")
			manager := resolvedManager(runtime, root)
			before, err := manager.Status(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if before.Code != StatusConfigurationNotFound {
				t.Fatalf("initial status = %#v", before)
			}
			result, err := manager.Install(context.Background())
			if err != nil {
				t.Fatalf("Install() error = %v", err)
			}
			if !result.Changed || result.Status.Code != StatusInstalled {
				t.Fatalf("install result = %#v", result)
			}
			path := filepath.Join(root, configName(runtime))
			rootValue := readJSON(t, path)
			hooks := asJSONObject(t, rootValue["hooks"])
			post := asJSONArray(t, hooks["PostToolUse"])
			if len(post) != 1 {
				t.Fatalf("PostToolUse groups = %#v", post)
			}
			group := asJSONObject(t, post[0])
			handlers := asJSONArray(t, group["hooks"])
			if len(handlers) != 1 {
				t.Fatalf("PostToolUse handlers = %#v", handlers)
			}
			handler := asJSONObject(t, handlers[0])
			if handler["type"] != "command" || handler["timeout"] != float64(hookTimeout) {
				t.Fatalf("handler = %#v", handler)
			}
			if runtime == RuntimeClaude {
				if handler["async"] != true {
					t.Fatalf("Claude handler async = %#v, want true", handler["async"])
				}
				if _, found := hooks["PreToolUse"]; found {
					t.Fatal("Claude install unexpectedly added PreToolUse")
				}
				if _, found := hooks["UserPromptExpansion"]; !found {
					t.Fatal("Claude install did not add UserPromptExpansion")
				}
				if _, found := hooks["PostToolUseFailure"]; !found {
					t.Fatal("Claude install did not add PostToolUseFailure")
				}
				if handler["command"] != BinaryName {
					t.Fatalf("Claude command = %#v", handler["command"])
				}
				args := asJSONArray(t, handler["args"])
				want := []any{"ingest", "--runtime", string(RuntimeClaude), ManagedFlag, ManagedMarker}
				if !reflect.DeepEqual(args, want) {
					t.Fatalf("Claude args = %#v, want %#v", args, want)
				}
			} else {
				if _, found := handler["async"]; found {
					t.Fatalf("Codex handler unexpectedly has async: %#v", handler)
				}
				if handler["command"] != expectedCodexCommand() {
					t.Fatalf("Codex command = %#v, want %q", handler["command"], expectedCodexCommand())
				}
				if _, found := handler["args"]; found {
					t.Fatal("Codex command unexpectedly has args")
				}
				if len(hooks) != 1 {
					t.Fatalf("Codex hooks unexpectedly changed unrelated event shape: %#v", hooks)
				}
			}
			if got := snapshotTree(t, filepath.Dir(root)); len(got) == 0 {
				t.Fatal("install did not create configuration tree")
			}
		})
	}
}

func TestCodexLegacyAsyncHandlerMigratesInPlaceAndPreservesUnownedEntries(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config")
	legacy := fmt.Sprintf(`{"type":"command","command":%q,"async":true,"timeout":%d}`, expectedCodexCommand(), hookTimeout)
	canonicalHandler := fmt.Sprintf(`{"type":"command","command":%q,"timeout":%d}`, expectedCodexCommand(), hookTimeout)
	content := fmt.Sprintf(`{"hooks":{"PostToolUse":[
  {"hooks":[%s,%s,{"type":"command","command":"user-hook"}]},
  {"matcher":"Write","hooks":[{"type":"command","command":"harness-lint","args":["ingest","--runtime","codex","--managed-by","harness-lint-hooks/v0"],"async":true,"timeout":10}]},
  {"hooks":[{"type":"command","command":"echo harness-lint ingest --runtime codex --managed-by harness-lint-hooks/v1"}]}
]}}`, legacy, canonicalHandler)
	path := writeHookConfig(t, root, RuntimeCodex, content)
	manager := resolvedManager(RuntimeCodex, root)

	status, err := manager.Status(context.Background())
	if err != nil || status.Code != StatusPartiallyInstalled || status.Managed != ManagedEntryPartial {
		t.Fatalf("legacy status = %#v, err=%v", status, err)
	}
	if got := status.ManagedEntries[0]; got.ExactHandlers != 2 || got.Partial != 0 || got.Lookalikes != 1 || got.State != ManagedEntryPartial {
		t.Fatalf("legacy entry = %#v", got)
	}
	beforeDryRun, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dryRun, err := manager.DryRun(context.Background(), ActionInstall)
	if err != nil || !dryRun.WouldChange || dryRun.Changed {
		t.Fatalf("legacy dry-run = %#v, err=%v", dryRun, err)
	}
	afterDryRun, _ := os.ReadFile(path)
	if string(beforeDryRun) != string(afterDryRun) {
		t.Fatal("legacy dry-run changed configuration")
	}

	installed, err := manager.Install(context.Background())
	if err != nil || !installed.Changed || installed.Status.Code != StatusInstalled {
		t.Fatalf("legacy install = %#v, err=%v", installed, err)
	}
	value := readJSON(t, path)
	post := asJSONArray(t, asJSONObject(t, value["hooks"])["PostToolUse"])
	if len(post) != 3 {
		t.Fatalf("migration created or removed groups: %#v", post)
	}
	firstHandlers := asJSONArray(t, asJSONObject(t, post[0])["hooks"])
	if len(firstHandlers) != 2 {
		t.Fatalf("migration changed user handlers: %#v", firstHandlers)
	}
	migrated := asJSONObject(t, firstHandlers[0])
	if _, found := migrated["async"]; found {
		t.Fatalf("migrated Codex handler retained async: %#v", migrated)
	}
	if asJSONObject(t, firstHandlers[1])["command"] != "user-hook" {
		t.Fatalf("migration changed user hook: %#v", firstHandlers)
	}

	status, err = manager.Status(context.Background())
	if err != nil || status.Code != StatusInstalled || status.Managed != ManagedEntryInstalled {
		t.Fatalf("migrated status = %#v, err=%v", status, err)
	}
	canonical, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Install(context.Background())
	if err != nil || second.Changed || second.WouldChange {
		t.Fatalf("second migrated install = %#v, err=%v", second, err)
	}
	canonicalAgain, _ := os.ReadFile(path)
	if string(canonical) != string(canonicalAgain) {
		t.Fatal("second migrated install changed bytes")
	}

	uninstalled, err := manager.Uninstall(context.Background())
	if err != nil || !uninstalled.Changed {
		t.Fatalf("migrated uninstall = %#v, err=%v", uninstalled, err)
	}
	value = readJSON(t, path)
	post = asJSONArray(t, asJSONObject(t, value["hooks"])["PostToolUse"])
	if len(post) != 3 {
		t.Fatalf("uninstall changed unowned groups: %#v", post)
	}
	if len(asJSONArray(t, asJSONObject(t, post[0])["hooks"])) != 1 || asJSONObject(t, asJSONArray(t, asJSONObject(t, post[0])["hooks"])[0])["command"] != "user-hook" {
		t.Fatalf("uninstall removed user hook: %#v", post)
	}
	if _, err := manager.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	remaining := readJSON(t, path)
	if len(asJSONArray(t, asJSONObject(t, remaining["hooks"])["PostToolUse"])) != 3 {
		t.Fatal("second uninstall changed preserved entries")
	}
}

func TestClaudeInstallDoesNotNormalizeCodexShapedUserHook(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config")
	legacyCodex := fmt.Sprintf(`{"type":"command","command":%q,"async":true,"timeout":%d}`, expectedCodexCommand(), hookTimeout)
	path := writeHookConfig(t, root, RuntimeClaude, fmt.Sprintf(`{"hooks":{"PostToolUse":[{"hooks":[%s]}]}}`, legacyCodex))
	manager := resolvedManager(RuntimeClaude, root)

	if _, err := manager.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	value := readJSON(t, path)
	post := asJSONArray(t, asJSONObject(t, value["hooks"])["PostToolUse"])
	if len(post) != 2 {
		t.Fatalf("Claude install changed Codex-shaped user group count: %#v", post)
	}
	preserved := asJSONObject(t, asJSONArray(t, asJSONObject(t, post[0])["hooks"])[0])
	if preserved["async"] != true || preserved["command"] != expectedCodexCommand() {
		t.Fatalf("Claude install normalized Codex-shaped user hook: %#v", preserved)
	}
}

func TestInstallMergesUnrelatedHooksAndPreservesTopLevelOrderAndFields(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config")
	content := `{
  "first": {"nested": [1, 2, 3]},
  "hooks": {
    "PostToolUse": [{"matcher":"Bash", "hooks":[{"type":"command","command":"audit","unknown":{"keep":true}}], "groupUnknown":"keep"}],
    "OtherEvent": [{"matcher":"all", "hooks":[{"type":"command","command":"other"}], "unknown":42}],
    "UserPromptExpansion": [{"hooks":[{"type":"command","command":"prompt-audit"}]}]
  },
  "last": "preserve"
}`
	path := writeHookConfig(t, root, RuntimeClaude, content)
	manager := resolvedManager(RuntimeClaude, root)
	if _, err := manager.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	value := readJSON(t, path)
	if !reflect.DeepEqual(value["first"], map[string]any{"nested": []any{float64(1), float64(2), float64(3)}}) || value["last"] != "preserve" {
		t.Fatalf("top-level fields changed: %#v", value)
	}
	hooks := asJSONObject(t, value["hooks"])
	if len(asJSONArray(t, hooks["PostToolUse"])) != 2 || len(asJSONArray(t, hooks["UserPromptExpansion"])) != 2 {
		t.Fatalf("managed groups not merged: %#v", hooks)
	}
	other := asJSONObject(t, asJSONArray(t, hooks["OtherEvent"])[0])
	if other["unknown"] != float64(42) {
		t.Fatalf("unrelated event changed: %#v", other)
	}
	postUser := asJSONObject(t, asJSONArray(t, hooks["PostToolUse"])[0])
	if postUser["groupUnknown"] != "keep" || asJSONObject(t, asJSONArray(t, postUser["hooks"])[0])["unknown"] == nil {
		t.Fatalf("unrelated matcher group changed: %#v", postUser)
	}
	report, err := manager.Status(context.Background())
	if err != nil || report.Code != StatusInstalled {
		t.Fatalf("status after merge = %#v, err=%v", report, err)
	}
}

func TestInstallIsIdempotentAndDoesNotCreateExtraBackup(t *testing.T) {
	for _, runtime := range []Runtime{RuntimeClaude, RuntimeCodex} {
		t.Run(string(runtime), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "config")
			manager := resolvedManager(runtime, root)
			first, err := manager.Install(context.Background())
			if err != nil || !first.Changed {
				t.Fatalf("first install = %#v, err=%v", first, err)
			}
			path := filepath.Join(root, configName(runtime))
			firstBytes, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			second, err := manager.Install(context.Background())
			if err != nil || second.Changed || second.WouldChange {
				t.Fatalf("second install = %#v, err=%v", second, err)
			}
			secondBytes, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(firstBytes) != string(secondBytes) {
				t.Fatal("idempotent install rewrote byte content")
			}
			if got := snapshotTree(t, root); len(got) != 1 {
				t.Fatalf("idempotent install created unexpected files: %#v", got)
			}
		})
	}
}

func TestClaudeAllManagedEventsLifecycle(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config")
	manager := resolvedManager(RuntimeClaude, root)
	first, err := manager.Install(context.Background())
	if err != nil || !first.Changed || first.Status.Code != StatusInstalled {
		t.Fatalf("first Claude install = %#v, err=%v", first, err)
	}
	status, err := manager.Status(context.Background())
	if err != nil || status.Code != StatusInstalled || len(status.ManagedEntries) != 3 {
		t.Fatalf("Claude installed status = %#v, err=%v", status, err)
	}
	for _, entry := range status.ManagedEntries {
		if entry.ExactHandlers != 1 || entry.Partial != 0 || entry.State != ManagedEntryInstalled {
			t.Fatalf("Claude managed entry = %#v", entry)
		}
	}
	path := filepath.Join(root, configName(RuntimeClaude))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Install(context.Background())
	if err != nil || second.Changed || second.WouldChange {
		t.Fatalf("second Claude install = %#v, err=%v", second, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("second Claude install changed byte content")
	}
	uninstalled, err := manager.Uninstall(context.Background())
	if err != nil || !uninstalled.Changed {
		t.Fatalf("Claude uninstall = %#v, err=%v", uninstalled, err)
	}
	status, err = manager.Status(context.Background())
	if err != nil || status.Code != StatusNotInstalled || status.Managed != ManagedEntryNotInstalled {
		t.Fatalf("Claude uninstalled status = %#v, err=%v", status, err)
	}
	for _, entry := range status.ManagedEntries {
		if entry.ExactHandlers != 0 || entry.Partial != 0 || entry.State != ManagedEntryNotInstalled {
			t.Fatalf("Claude uninstalled entry = %#v", entry)
		}
	}
}

func TestManagedDuplicateAndPartialRegistrationsSurfacePartialStatus(t *testing.T) {
	for _, runtime := range []Runtime{RuntimeClaude, RuntimeCodex} {
		t.Run(string(runtime), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "config")
			manager := resolvedManager(runtime, root)
			if _, err := manager.Install(context.Background()); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, configName(runtime))
			value := readJSON(t, path)
			hooks := asJSONObject(t, value["hooks"])
			post := asJSONArray(t, hooks["PostToolUse"])
			group := asJSONObject(t, post[0])
			handlers := asJSONArray(t, group["hooks"])
			duplicate := make(map[string]any)
			for key, field := range asJSONObject(t, handlers[0]) {
				duplicate[key] = field
			}
			handlers = append(handlers, duplicate)
			handlers = append(handlers, map[string]any{
				"type":    "command",
				"command": BinaryName,
				"async":   true,
				"timeout": float64(hookTimeout + 1),
			})
			group["hooks"] = handlers
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o640); err != nil {
				t.Fatal(err)
			}
			status, err := manager.Status(context.Background())
			if err != nil || status.Code != StatusPartiallyInstalled || status.Managed != ManagedEntryPartial {
				t.Fatalf("partial status = %#v, err=%v", status, err)
			}
			postEntry := status.ManagedEntries[0]
			if postEntry.ExactHandlers != 2 || postEntry.Partial != 1 || postEntry.State != ManagedEntryPartial {
				t.Fatalf("partial PostToolUse entry = %#v", postEntry)
			}
		})
	}
}

func TestMalformedConfigNeverWrites(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config")
	path := writeHookConfig(t, root, RuntimeClaude, `{"hooks": {`)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := resolvedManager(RuntimeClaude, root).Install(context.Background())
	if !errors.Is(err, ErrMalformedConfiguration) {
		t.Fatalf("malformed install error = %v, want ErrMalformedConfiguration", err)
	}
	if result.Status.Code != StatusMalformed || result.Changed {
		t.Fatalf("malformed result = %#v", result)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("malformed config was changed")
	}
	if got := snapshotTree(t, root); len(got) != 1 {
		t.Fatalf("malformed config created backup/temp: %#v", got)
	}
}

func TestMalformedConfigMutationActionsReturnErrorsWithoutWriting(t *testing.T) {
	actions := []struct {
		name string
		run  func(Manager) (OperationResult, error)
	}{
		{name: "install", run: func(manager Manager) (OperationResult, error) {
			return manager.Install(context.Background())
		}},
		{name: "uninstall", run: func(manager Manager) (OperationResult, error) {
			return manager.Uninstall(context.Background())
		}},
		{name: "dry-run install", run: func(manager Manager) (OperationResult, error) {
			return manager.DryRun(context.Background(), ActionInstall)
		}},
	}
	for _, action := range actions {
		t.Run(action.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "config")
			path := writeHookConfig(t, root, RuntimeCodex, `{"hooks": {`)
			manager := resolvedManager(RuntimeCodex, root)
			status, err := manager.Status(context.Background())
			if err != nil || status.Code != StatusMalformed {
				t.Fatalf("malformed status = %#v, err=%v", status, err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			result, err := action.run(manager)
			if !errors.Is(err, ErrMalformedConfiguration) {
				t.Fatalf("%s error = %v, want ErrMalformedConfiguration", action.name, err)
			}
			if result.Changed || result.WouldChange || result.Status.Code != StatusMalformed {
				t.Fatalf("%s result = %#v", action.name, result)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatalf("%s changed malformed config", action.name)
			}
			if got := snapshotTree(t, root); len(got) != 1 {
				t.Fatalf("%s created backup/temp files: %#v", action.name, got)
			}
		})
	}
}

func TestInvalidStringAndControlJSONAreMalformedWithoutMutation(t *testing.T) {
	invalidControl := "{\"description\":\"bad" + string([]byte{0x01}) + "\"}"
	cases := []struct {
		name    string
		content string
	}{
		{name: "invalid unicode escape", content: `{"description":"\uZZZZ"}`},
		{name: "unescaped control character", content: invalidControl},
	}
	for _, runtime := range []Runtime{RuntimeClaude, RuntimeCodex} {
		for _, testCase := range cases {
			t.Run(string(runtime)+"/"+testCase.name, func(t *testing.T) {
				root := filepath.Join(t.TempDir(), "config")
				path := writeHookConfig(t, root, runtime, testCase.content)
				manager := resolvedManager(runtime, root)
				status, err := manager.Status(context.Background())
				if err != nil || status.Code != StatusMalformed {
					t.Fatalf("status = %#v, err=%v", status, err)
				}
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				result, err := manager.DryRun(context.Background(), ActionInstall)
				if !errors.Is(err, ErrMalformedConfiguration) {
					t.Fatalf("dry-run error = %v, want ErrMalformedConfiguration", err)
				}
				if result.Changed || result.WouldChange {
					t.Fatalf("dry-run result = %#v", result)
				}
				after, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if string(before) != string(after) {
					t.Fatal("invalid JSON was changed")
				}
			})
		}
	}
}

func TestDryRunNeverCreatesDirectoriesFilesTempsOrBackups(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing", "config")
	manager := resolvedManager(RuntimeCodex, root)
	result, err := manager.DryRun(context.Background(), ActionInstall)
	if err != nil {
		t.Fatal(err)
	}
	if !result.WouldChange || result.Changed || result.Status.Code != StatusConfigurationNotFound {
		t.Fatalf("dry-run result = %#v", result)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run created root: %v", err)
	}
	result, err = manager.DryRun(context.Background(), ActionUninstall)
	if err != nil || result.WouldChange || result.Changed {
		t.Fatalf("dry-run uninstall result = %#v, err=%v", result, err)
	}
}

func TestUninstallRemovesOnlyExactManagedHandlersAndPreservesUnrelatedHandlers(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config")
	manager := resolvedManager(RuntimeClaude, root)
	if _, err := manager.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, configName(RuntimeClaude))
	value := readJSON(t, path)
	hooks := asJSONObject(t, value["hooks"])
	post := asJSONArray(t, hooks["PostToolUse"])
	managed := asJSONObject(t, post[0])
	managedHandlers := asJSONArray(t, managed["hooks"])
	managedHandlers = append(managedHandlers, map[string]any{"type": "command", "command": "user-same-group", "unknown": "keep"})
	managed["hooks"] = managedHandlers
	post = append(post, map[string]any{"matcher": "Write", "hooks": []any{map[string]any{"type": "command", "command": "user-different-group"}}})
	hooks["PostToolUse"] = post
	if data, err := json.Marshal(value); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(path, data, 0o640); err != nil {
		t.Fatal(err)
	}
	result, err := manager.Uninstall(context.Background())
	if err != nil || !result.Changed {
		t.Fatalf("uninstall = %#v, err=%v", result, err)
	}
	after := readJSON(t, path)
	afterHooks := asJSONObject(t, after["hooks"])
	afterPost := asJSONArray(t, afterHooks["PostToolUse"])
	if len(afterPost) != 2 {
		t.Fatalf("uninstall removed user group: %#v", afterPost)
	}
	firstHandlers := asJSONArray(t, asJSONObject(t, afterPost[0])["hooks"])
	if len(firstHandlers) != 1 || asJSONObject(t, firstHandlers[0])["command"] != "user-same-group" {
		t.Fatalf("same-group user handler not preserved: %#v", firstHandlers)
	}
	if asJSONObject(t, asJSONArray(t, asJSONObject(t, afterPost[1])["hooks"])[0])["command"] != "user-different-group" {
		t.Fatalf("different-group user handler not preserved: %#v", afterPost)
	}
	report, err := manager.Status(context.Background())
	if err != nil || report.Code != StatusNotInstalled {
		t.Fatalf("status after uninstall = %#v, err=%v", report, err)
	}
}

func TestUninstallNotInstalledIsNoOp(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config")
	manager := resolvedManager(RuntimeCodex, root)
	result, err := manager.Uninstall(context.Background())
	if err != nil || result.Changed || result.Status.Code != StatusConfigurationNotFound {
		t.Fatalf("uninstall missing = %#v, err=%v", result, err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("uninstall created config root: %v", err)
	}
}

func TestLookalikesAndPartialStaleVersionsAreNeverDeleted(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config")
	content := `{"hooks":{"PostToolUse":[{"hooks":[
  {"type":"command","command":"echo harness-lint ingest --runtime codex --managed-by harness-lint-hooks/v1"},
  {"type":"command","command":"harness-lint","args":["ingest","--runtime","codex","--managed-by","harness-lint-hooks/v0"],"async":true,"timeout":10}
] }]}}`
	path := writeHookConfig(t, root, RuntimeCodex, content)
	manager := resolvedManager(RuntimeCodex, root)
	report, err := manager.Status(context.Background())
	if err != nil || report.Code != StatusPartiallyInstalled || report.ManagedEntries[0].Partial != 1 || report.ManagedEntries[0].Lookalikes != 1 {
		t.Fatalf("partial status = %#v, err=%v", report, err)
	}
	before, _ := os.ReadFile(path)
	result, err := manager.Uninstall(context.Background())
	if err != nil || result.Changed {
		t.Fatalf("uninstall partial = %#v, err=%v", result, err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("uninstall changed lookalike/partial-only config")
	}
	if _, err := manager.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	value := readJSON(t, path)
	post := asJSONArray(t, asJSONObject(t, value["hooks"])["PostToolUse"])
	handlerCount := 0
	for _, group := range post {
		handlerCount += len(asJSONArray(t, asJSONObject(t, group)["hooks"]))
	}
	if handlerCount != 3 {
		t.Fatalf("install did not add exact handler alongside preserved candidates: %#v", post)
	}
	if _, err := manager.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	value = readJSON(t, path)
	post = asJSONArray(t, asJSONObject(t, value["hooks"])["PostToolUse"])
	remaining := asJSONArray(t, asJSONObject(t, post[0])["hooks"])
	if len(remaining) != 2 {
		t.Fatalf("uninstall removed stale/lookalike: %#v", remaining)
	}
}

func TestBrokenSymlinkFailsWithoutMutation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, claudeConfigName)
	if err := os.Symlink(filepath.Join(root, "missing-target"), path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	manager := resolvedManager(RuntimeClaude, root)
	report, err := manager.Status(context.Background())
	if err != nil || report.Code != StatusUnsupported {
		t.Fatalf("symlink status = %#v, err=%v", report, err)
	}
	if _, err := manager.Install(context.Background()); err == nil {
		t.Fatal("install followed broken symlink")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != claudeConfigName {
		t.Fatalf("symlink install created unexpected entries: %#v", entries)
	}
}

func TestAtomicBackupPreservesModeAndLeavesNoTempFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config")
	path := writeHookConfig(t, root, RuntimeCodex, `{"description":"before"}`)
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	old, _ := os.ReadFile(path)
	result, err := resolvedManager(RuntimeCodex, root).Install(context.Background())
	if err != nil || !result.Changed || result.BackupPath == "" {
		t.Fatalf("install backup = %#v, err=%v", result, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("mode = %o, want 640", got)
	}
	backup, err := os.ReadFile(result.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != string(old) {
		t.Fatalf("backup = %q, want original %q", backup, old)
	}
	entries := snapshotTree(t, root)
	for _, entry := range entries {
		if strings.Contains(entry, ".tmp-") {
			t.Fatalf("temporary file left after atomic write: %#v", entries)
		}
	}
	_ = readJSON(t, path)
}

func TestCodexInlineConfigAndTrustReviewAreStructuredStatus(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte("[hooks.PostToolUse]\nmatcher = \"Bash\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := resolvedManager(RuntimeCodex, root).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !report.InlineHooks || !report.TrustReview.Required || report.TrustReview.Limitation == "" {
		t.Fatalf("Codex status = %#v", report)
	}
	if !hasInlineWarning(report) {
		t.Fatalf("Codex status warnings = %#v", report.Warnings)
	}
	if _, err := resolvedManager(RuntimeCodex, root).Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "config.toml")); err != nil {
		t.Fatal(err)
	}
}

func TestCodexHookStateRegistryDoesNotTriggerInlineReview(t *testing.T) {
	tests := []struct {
		name        string
		config      string
		wantInline  bool
		wantWarning bool
	}{
		{
			name: "state only",
			config: `[hooks.state."/tmp/hooks.json"]
enabled = true
trusted_hash = "hash"
`,
			wantInline:  false,
			wantWarning: false,
		},
		{
			name: "state and event",
			config: `[hooks.state."/tmp/hooks.json"]
enabled = true
trusted_hash = "hash"

[hooks.PostToolUse]
matcher = "Bash"
`,
			wantInline:  true,
			wantWarning: true,
		},
		{
			name: "unknown event",
			config: `[hooks.UnknownEvent]
matcher = "Bash"
`,
			wantInline:  true,
			wantWarning: true,
		},
		{
			name: "version is not inline metadata",
			config: `[hooks.version]
value = 1
`,
			wantInline:  true,
			wantWarning: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "config")
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte(test.config), 0o600); err != nil {
				t.Fatal(err)
			}
			report, err := resolvedManager(RuntimeCodex, root).Status(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if report.InlineHooks != test.wantInline {
				t.Fatalf("InlineHooks = %t, want %t; report = %#v", report.InlineHooks, test.wantInline, report)
			}
			if hasInlineWarning(report) != test.wantWarning {
				t.Fatalf("inline warning = %t, want %t; warnings = %#v", hasInlineWarning(report), test.wantWarning, report.Warnings)
			}
		})
	}
}

func TestUnresolvedBinaryNeverMutatesConfiguration(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config")
	manager := unresolvedManager(RuntimeClaude, root)
	result, err := manager.Install(context.Background())
	if !errors.Is(err, errBinaryUnresolved) || result.Changed {
		t.Fatalf("unresolved install = %#v, err=%v", result, err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unresolved binary install created root: %v", err)
	}
}

func TestContextCancellationPreventsReadsAndWrites(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config")
	manager := resolvedManager(RuntimeClaude, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Install(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled install error = %v", err)
	}
}
