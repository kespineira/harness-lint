package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
)

func (a *Adapter) discoverMCP(ctx context.Context, now time.Time, result *domain.Discovery) error {
	paths := make([]sourceRoot, 0, 2)
	if a.options.configRoot != "" {
		paths = append(paths, sourceRoot{path: filepath.Join(a.options.configRoot, "config.toml"), scope: domain.ScopeUser})
	}
	if a.options.repoRoot != "" {
		paths = append(paths, sourceRoot{path: filepath.Join(a.options.repoRoot, ".codex", "config.toml"), scope: domain.ScopeProject})
	}
	for _, source := range paths {
		if err := contextErr(ctx); err != nil {
			return err
		}
		content, exists, readErr := readOptionalFile(source.path)
		if readErr != nil {
			if isBrokenSymlink(source.path) {
				addFinding(result, "broken-symlink", "MCP configuration symlink target cannot be read", domain.CapabilityMCPServer, filepath.Base(source.path))
			} else {
				addFinding(result, "config-unreadable", "MCP configuration cannot be read", domain.CapabilityMCPServer, filepath.Base(source.path))
			}
			continue
		}
		if !exists {
			continue
		}
		values, parseErr := parseTOML(content)
		if parseErr != nil {
			addFinding(result, "malformed-config", "Codex TOML configuration could not be parsed", domain.CapabilityMCPServer, filepath.Base(source.path))
			continue
		}
		mcpValue, ok := values["mcp_servers"]
		if !ok {
			// Codex uses the snake-case section.  Do not silently interpret
			// legacy or foreign configuration sections as MCP inventory.
			if _, legacy := values["mcpServers"]; legacy {
				addFinding(result, "malformed-config", "MCP configuration must use the mcp_servers section", domain.CapabilityMCPServer, filepath.Base(source.path))
			}
			continue
		}
		servers, ok := mcpValue.(map[string]any)
		if !ok {
			addFinding(result, "malformed-config", "mcp_servers must be a TOML table", domain.CapabilityMCPServer, filepath.Base(source.path))
			continue
		}
		serverNames := sortedMapKeys(servers)
		for _, name := range serverNames {
			if err := contextErr(ctx); err != nil {
				return err
			}
			server, ok := servers[name].(map[string]any)
			if !ok {
				addFinding(result, "malformed-config", "MCP server entry must be a TOML table", domain.CapabilityMCPServer, name)
				continue
			}
			capability := capabilityBase(now, domain.CapabilityMCPServer, name, source.scope, source.path)
			capability.Hash = hashBytes(content)
			capability.MetadataTokens = unknownMeasurement()
			capability.BodyTokens = unknownMeasurement()
			if enabled, present, valid := boolField(server, "enabled"); present {
				if !valid {
					addFinding(result, "malformed-config", "MCP enabled state must be boolean", domain.CapabilityMCPServer, name)
				} else if enabled {
					capability.Enabled = domain.EnabledStateEnabled
				} else {
					capability.Enabled = domain.EnabledStateDisabled
				}
			}
			result.Capabilities = append(result.Capabilities, capability)

			if rawCommand, present := server["command"]; present {
				command, valid := rawCommand.(string)
				if !valid {
					addFinding(result, "malformed-config", "MCP command must be a string", domain.CapabilityMCPServer, name)
				} else if strings.TrimSpace(command) != "" {
					if _, lookupErr := a.options.commandLookup(command); lookupErr != nil {
						addFinding(result, "unresolved-mcp-command", "configured MCP command is not resolvable", domain.CapabilityMCPServer, name)
					}
				}
			}
			if err := appendMCPToolFilters(result, server, source, name, content, now); err != nil {
				addFinding(result, "malformed-config", err.Error(), domain.CapabilityMCPServer, name)
			}
		}
	}
	return nil
}

func appendMCPToolFilters(result *domain.Discovery, server map[string]any, source sourceRoot, serverName string, content []byte, now time.Time) error {
	states := make(map[string]domain.EnabledState)
	seenFilter := false
	add := func(value any, state domain.EnabledState) error {
		seenFilter = true
		names, ok := stringSlice(value)
		if !ok {
			return fmt.Errorf("MCP tool filter must be an array of strings")
		}
		for _, name := range names {
			name = strings.TrimSpace(name)
			if name == "" || name == "*" {
				continue
			}
			if previous, exists := states[name]; exists && previous != state {
				// A deny filter is safer and deterministic when both filters
				// mention the same observable tool.
				if state == domain.EnabledStateDisabled {
					states[name] = state
				}
				addFinding(result, "conflicting-mcp-tool-filter", "MCP tool appears in both enabled and disabled filters", domain.CapabilityMCPTool, mcpToolIdentity(serverName, name))
				continue
			}
			states[name] = state
		}
		return nil
	}
	if value, exists := server["enabled_tools"]; exists {
		if err := add(value, domain.EnabledStateEnabled); err != nil {
			return err
		}
	}
	if value, exists := server["disabled_tools"]; exists {
		if err := add(value, domain.EnabledStateDisabled); err != nil {
			return err
		}
	}
	if value, exists := server["tool_filters"]; exists {
		filters, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("MCP tool_filters must be a TOML table")
		}
		for _, key := range []string{"include", "allow", "enabled", "enabled_tools"} {
			if item, found := filters[key]; found {
				if err := add(item, domain.EnabledStateEnabled); err != nil {
					return err
				}
			}
		}
		for _, key := range []string{"exclude", "deny", "disabled", "disabled_tools"} {
			if item, found := filters[key]; found {
				if err := add(item, domain.EnabledStateDisabled); err != nil {
					return err
				}
			}
		}
	}
	if tools, exists := server["tools"]; exists {
		if table, ok := tools.(map[string]any); ok {
			for _, name := range sortedMapKeys(table) {
				enabled, valid := boolValue(table[name])
				if !valid {
					if config, ok := table[name].(map[string]any); ok {
						if item, found := config["enabled"]; found {
							enabled, valid = boolValue(item)
						} else if item, found := config["disabled"]; found {
							var disabled bool
							disabled, valid = boolValue(item)
							enabled = !disabled
						}
					}
				}
				if !valid {
					continue
				}
				seenFilter = true
				if enabled {
					states[name] = domain.EnabledStateEnabled
				} else {
					states[name] = domain.EnabledStateDisabled
				}
			}
		}
	}
	if !seenFilter {
		return nil
	}
	toolNames := make([]string, 0, len(states))
	for name := range states {
		toolNames = append(toolNames, name)
	}
	sort.Strings(toolNames)
	for _, toolName := range toolNames {
		capability := capabilityBase(now, domain.CapabilityMCPTool, mcpToolIdentity(serverName, toolName), source.scope, source.path)
		capability.Enabled = states[toolName]
		capability.Hash = hashBytes([]byte(string(content) + "\x00" + serverName + "\x00" + toolName + "\x00" + string(states[toolName])))
		capability.MetadataTokens = unknownMeasurement()
		capability.BodyTokens = unknownMeasurement()
		result.Capabilities = append(result.Capabilities, capability)
	}
	return nil
}

func mcpToolIdentity(server, tool string) string {
	return "mcp__" + server + "__" + tool
}

func (a *Adapter) discoverHooks(ctx context.Context, now time.Time, result *domain.Discovery) error {
	paths := make([]sourceRoot, 0, 2)
	if a.options.configRoot != "" {
		paths = append(paths, sourceRoot{path: filepath.Join(a.options.configRoot, "hooks.json"), scope: domain.ScopeUser})
	}
	if a.options.repoRoot != "" {
		paths = append(paths, sourceRoot{path: filepath.Join(a.options.repoRoot, ".codex", "hooks.json"), scope: domain.ScopeProject})
	}
	for _, source := range paths {
		if err := contextErr(ctx); err != nil {
			return err
		}
		content, exists, readErr := readOptionalFile(source.path)
		if readErr != nil {
			if isBrokenSymlink(source.path) {
				addFinding(result, "broken-symlink", "hooks configuration symlink target cannot be read", domain.CapabilityHook, filepath.Base(source.path))
			} else {
				addFinding(result, "hooks-unreadable", "hooks configuration cannot be read", domain.CapabilityHook, filepath.Base(source.path))
			}
			continue
		}
		if !exists {
			continue
		}
		var value any
		decoder := json.NewDecoder(strings.NewReader(string(content)))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			addFinding(result, "malformed-hooks", "hooks JSON configuration could not be parsed", domain.CapabilityHook, filepath.Base(source.path))
			continue
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			addFinding(result, "malformed-hooks", "hooks JSON configuration has trailing data", domain.CapabilityHook, filepath.Base(source.path))
			continue
		}
		if _, ok := value.(map[string]any); !ok {
			addFinding(result, "malformed-hooks", "hooks JSON configuration must be an object", domain.CapabilityHook, filepath.Base(source.path))
			continue
		}
		hooks := hookEvents(value)
		for _, event := range hooks {
			capability := capabilityBase(now, domain.CapabilityHook, event.name, source.scope, source.path)
			capability.Hash = hashBytes(content)
			capability.Enabled = event.enabled
			capability.MetadataTokens = unknownMeasurement()
			capability.BodyTokens = unknownMeasurement()
			result.Capabilities = append(result.Capabilities, capability)
		}
	}
	return nil
}

type hookEvent struct {
	name    string
	enabled domain.EnabledState
}

func hookEvents(value any) []hookEvent {
	root, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if nested, exists := root["hooks"]; exists {
		if nestedMap, ok := nested.(map[string]any); ok {
			root = nestedMap
		}
	}
	names := make([]string, 0, len(root))
	for name := range root {
		if name == "hooks" || name == "version" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]hookEvent, 0, len(names))
	for _, name := range names {
		enabled := domain.EnabledStateUnknown
		if items, ok := root[name].([]any); ok {
			for _, item := range items {
				if state, present := hookEnabled(item); present {
					enabled = state
					break
				}
			}
		} else if state, present := hookEnabled(root[name]); present {
			enabled = state
		}
		result = append(result, hookEvent{name: name, enabled: enabled})
	}
	return result
}

func hookEnabled(value any) (domain.EnabledState, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		return domain.EnabledStateUnknown, false
	}
	value, exists := object["enabled"]
	if !exists {
		return domain.EnabledStateUnknown, false
	}
	enabled, ok := boolValue(value)
	if !ok {
		return domain.EnabledStateUnknown, false
	}
	if enabled {
		return domain.EnabledStateEnabled, true
	}
	return domain.EnabledStateDisabled, true
}

func readOptionalFile(path string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if info.IsDir() {
		return nil, true, fmt.Errorf("path is a directory")
	}
	content, err := os.ReadFile(path)
	return content, true, err
}

func boolField(values map[string]any, key string) (value bool, present bool, valid bool) {
	item, present := values[key]
	if !present {
		return false, false, true
	}
	value, valid = boolValue(item)
	return value, true, valid
}

func boolValue(value any) (bool, bool) {
	item, ok := value.(bool)
	return item, ok
}

func stringSlice(value any) ([]string, bool) {
	items, ok := value.([]any)
	if !ok {
		return nil, false
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		stringValue, ok := item.(string)
		if !ok {
			return nil, false
		}
		result = append(result, stringValue)
	}
	return result, true
}

func sortedMapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
