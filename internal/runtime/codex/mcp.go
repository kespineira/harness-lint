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
	if a.options.projectRoot != "" {
		paths = append(paths, sourceRoot{path: filepath.Join(a.options.projectRoot, ".codex", "config.toml"), scope: domain.ScopeProject})
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
		values, parseErr := decodeTOML(content)
		if parseErr != nil {
			addFinding(result, "malformed-config", "Codex TOML configuration could not be parsed", domain.CapabilityMCPServer, filepath.Base(source.path))
			continue
		}
		appendInlineHooks(result, values, source, content, now)
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
			capability := capabilityBase(now, domain.CapabilityMCPServer, name, source.scope, source.path, domain.AdvertisementStateUnknown)
			capability.Hash = hashBytes(content)
			capability.MetadataTokens = unknownMeasurement()
			capability.BodyTokens = unknownMeasurement()
			capability.Enabled = domain.EnabledStateEnabled
			if enabled, present, valid := boolField(server, "enabled"); present {
				if !valid {
					capability.Enabled = domain.EnabledStateUnknown
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
					if _, lookupErr := a.options.lookPath(command); lookupErr != nil {
						addFinding(result, "unresolved-mcp-command", "configured MCP command is not resolvable", domain.CapabilityMCPServer, name)
					}
				}
			}
			if err := appendMCPToolFilters(result, server, capability.Enabled, source, name, content, now); err != nil {
				addFinding(result, "malformed-config", err.Error(), domain.CapabilityMCPServer, name)
			}
		}
	}
	return nil
}

func appendMCPToolFilters(result *domain.Discovery, server map[string]any, serverState domain.EnabledState, source sourceRoot, serverName string, content []byte, now time.Time) error {
	states := make(map[string]domain.EnabledState)
	add := func(value any, state domain.EnabledState) error {
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
				addFinding(result, "conflicting-mcp-tool-filter", "MCP tool appears in both enabled and disabled filters; disabled wins", domain.CapabilityMCPTool, mcpToolIdentity(serverName, name))
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
	if len(states) == 0 {
		return nil
	}
	toolNames := make([]string, 0, len(states))
	for name := range states {
		toolNames = append(toolNames, name)
	}
	sort.Strings(toolNames)
	for _, toolName := range toolNames {
		capability := capabilityBase(now, domain.CapabilityMCPTool, mcpToolIdentity(serverName, toolName), source.scope, source.path, domain.AdvertisementStateUnknown)
		capability.Enabled = states[toolName]
		if serverState == domain.EnabledStateDisabled {
			capability.Enabled = domain.EnabledStateDisabled
		}
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
	if a.options.projectRoot != "" {
		paths = append(paths, sourceRoot{path: filepath.Join(a.options.projectRoot, ".codex", "hooks.json"), scope: domain.ScopeProject})
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
		appendHookCapabilities(result, hookEvents(value), source, content, now)
	}
	return nil
}

func appendInlineHooks(result *domain.Discovery, values map[string]any, source sourceRoot, content []byte, now time.Time) {
	hooks, ok := values["hooks"]
	if !ok {
		return
	}
	if _, ok := hooks.(map[string]any); !ok {
		addFinding(result, "malformed-config", "hooks must be a TOML table", domain.CapabilityHook, filepath.Base(source.path))
		return
	}
	appendHookCapabilities(result, inlineHookEvents(hooks), source, content, now)
}

func appendHookCapabilities(result *domain.Discovery, hooks []hookEvent, source sourceRoot, content []byte, now time.Time) {
	for _, event := range hooks {
		capability := capabilityBase(now, domain.CapabilityHook, event.name, source.scope, source.path, domain.AdvertisementStateUnknown)
		capability.Hash = hashBytes(content)
		capability.Enabled = event.enabled
		capability.MetadataTokens = unknownMeasurement()
		capability.BodyTokens = unknownMeasurement()
		result.Capabilities = append(result.Capabilities, capability)
	}
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
	return filteredHookEvents(root, func(name string) bool {
		return name == "hooks" || name == "version"
	})
}

func inlineHookEvents(value any) []hookEvent {
	root, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return filteredHookEvents(root, func(name string) bool { return name == "state" })
}

func filteredHookEvents(root map[string]any, skip func(string) bool) []hookEvent {
	names := make([]string, 0, len(root))
	for name := range root {
		if skip(name) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]hookEvent, 0, len(names))
	for _, name := range names {
		enabled := domain.EnabledStateUnknown
		if state, present := hookEnabled(root[name]); present {
			enabled = state
		}
		result = append(result, hookEvent{name: name, enabled: enabled})
	}
	return result
}

func hookEnabled(value any) (domain.EnabledState, bool) {
	switch items := value.(type) {
	case []any:
		for _, item := range items {
			if state, present := hookEnabled(item); present {
				return state, true
			}
		}
		return domain.EnabledStateUnknown, false
	case []map[string]any:
		for _, item := range items {
			if state, present := hookEnabled(item); present {
				return state, true
			}
		}
		return domain.EnabledStateUnknown, false
	}
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
	switch items := value.(type) {
	case []string:
		return append([]string(nil), items...), true
	case []any:
		result := make([]string, 0, len(items))
		for _, item := range items {
			stringValue, ok := item.(string)
			if !ok {
				return nil, false
			}
			result = append(result, stringValue)
		}
		return result, true
	default:
		return nil, false
	}
}

func sortedMapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
