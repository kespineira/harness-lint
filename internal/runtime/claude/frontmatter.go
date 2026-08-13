package claude

import (
	"fmt"
	"strconv"
	"strings"
)

// frontMatter is intentionally a small YAML subset. Claude's documented
// frontmatter is mostly scalar metadata; preserving arbitrary YAML would add
// a dependency and would risk accidentally retaining prompt-like content.
type frontMatter struct {
	values map[string]string
	body   []byte
	has    bool
	err    error
}

func parseFrontMatter(data []byte) frontMatter {
	result := frontMatter{values: make(map[string]string), body: data}
	text := strings.TrimPrefix(string(data), "\ufeff")
	lines := strings.SplitAfter(text, "\n")
	if len(lines) == 0 {
		return result
	}
	if strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(lines[0], "\n"), "\r")) != "---" {
		return result
	}
	result.has = true

	closing := -1
	for i := 1; i < len(lines); i++ {
		line := strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(lines[i], "\n"), "\r"))
		if line == "---" || line == "..." {
			closing = i
			break
		}
	}
	if closing < 0 {
		result.err = fmt.Errorf("frontmatter closing marker is missing")
		return result
	}

	bodyStart := 0
	for i := 0; i <= closing; i++ {
		bodyStart += len(lines[i])
	}
	result.body = []byte(text[bodyStart:])

	for i := 1; i < closing; i++ {
		line := strings.TrimSuffix(strings.TrimSuffix(lines[i], "\n"), "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// Nested YAML values and list items are not needed for inventory. Ignore
		// them rather than pretending to understand their semantics.
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") || strings.HasPrefix(trimmed, "-") {
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok || strings.TrimSpace(key) == "" {
			result.err = fmt.Errorf("frontmatter line %d is not a scalar key", i+1)
			continue
		}
		key = strings.TrimSpace(key)
		if _, exists := result.values[key]; exists {
			result.err = fmt.Errorf("frontmatter key %q is duplicated", key)
			continue
		}
		result.values[key] = parseYAMLScalar(value)
	}
	return result
}

func parseYAMLScalar(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		if (value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"') {
			if value[0] == '"' {
				if decoded, err := strconv.Unquote(value); err == nil {
					return decoded
				}
			}
			return value[1 : len(value)-1]
		}
	}
	return value
}

func (f frontMatter) value(key string) string {
	return strings.TrimSpace(f.values[key])
}

func (f frontMatter) boolValue(key string) (bool, bool) {
	value, ok := f.values[key]
	if !ok {
		return false, false
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "yes", "on", "1":
		return true, true
	case "false", "no", "off", "0":
		return false, true
	default:
		return false, false
	}
}

// selectionDescription follows Claude's documented fallback: when
// description is omitted, the first paragraph is used for skill selection.
// It deliberately returns only that small selection field, never arbitrary
// frontmatter or the full instruction body.
func (f frontMatter) selectionDescription() string {
	if description := f.value("description"); description != "" {
		return description
	}
	return firstParagraph(f.body)
}

func firstParagraph(body []byte) string {
	lines := strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n")
	paragraph := make([]string, 0, 4)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if len(paragraph) > 0 {
				break
			}
			continue
		}
		paragraph = append(paragraph, line)
	}
	return strings.TrimSpace(strings.Join(paragraph, " "))
}
