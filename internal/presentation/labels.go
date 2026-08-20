package presentation

import (
	"strings"
)

// RuntimeLabel turns persisted/runtime-neutral identifiers into labels fit for
// a human view.  It accepts string aliases as well as named string types (for
// example domain.Runtime) without importing domain semantics into this layer.
func RuntimeLabel[T ~string](value T) string {
	key := normalizeLabel(string(value))
	switch key {
	case "claude", "claude_code":
		return "Claude Code"
	case "codex":
		return "Codex"
	case "cursor":
		return "Cursor"
	case "unknown", "":
		return "Unknown"
	default:
		return humanizeLabel(key)
	}
}

// HumanRuntime is an alias for RuntimeLabel.
func HumanRuntime[T ~string](value T) string {
	return RuntimeLabel(value)
}

// CapabilityTypeLabel turns the persisted capability type into a concise
// human label.  MCP server and tool remain distinct on purpose.
func CapabilityTypeLabel[T ~string](value T) string {
	key := normalizeLabel(string(value))
	switch key {
	case "mcp", "mcp_server":
		return "MCP server"
	case "mcp_tool":
		return "MCP tool"
	case "instruction_file":
		return "Instruction file"
	case "skill":
		return "Skill"
	case "agent":
		return "Agent"
	case "tool":
		return "Tool"
	case "hook":
		return "Hook"
	case "command":
		return "Command"
	case "plugin":
		return "Plugin"
	case "unknown", "":
		return "Unknown"
	default:
		return humanizeLabel(key)
	}
}

// HumanCapabilityType is an alias for CapabilityTypeLabel.
func HumanCapabilityType[T ~string](value T) string {
	return CapabilityTypeLabel(value)
}

func normalizeLabel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	return value
}

func humanizeLabel(value string) string {
	words := strings.Fields(strings.ReplaceAll(value, "_", " "))
	for position, word := range words {
		switch strings.ToLower(word) {
		case "mcp":
			words[position] = "MCP"
		case "api":
			words[position] = "API"
		default:
			if word != "" {
				words[position] = strings.ToUpper(word[:1]) + word[1:]
			}
		}
	}
	if len(words) == 0 {
		return "Unknown"
	}
	return strings.Join(words, " ")
}
