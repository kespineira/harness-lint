package codex

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// parseTOML is a deliberately small, deterministic TOML reader for the
// metadata-shaped Codex files we inspect.  It supports the TOML constructs
// used by Codex configuration and custom-agent files (tables, arrays of
// tables, strings, multiline strings, arrays, inline tables, booleans, and
// numeric values) while rejecting duplicate keys and malformed input.  It
// returns generic values because only selected metadata is retained.
func parseTOML(content []byte) (map[string]any, error) {
	root := make(map[string]any)
	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	current := []string(nil)
	for lineNo := 0; lineNo < len(lines); lineNo++ {
		line := strings.TrimSpace(trimTOMLComment(lines[lineNo]))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[[") {
			if !strings.HasSuffix(line, "]]") {
				return nil, fmt.Errorf("line %d: malformed array table", lineNo+1)
			}
			path, err := parseTOMLKeyPath(strings.TrimSpace(line[2 : len(line)-2]))
			if err != nil || len(path) == 0 {
				return nil, fmt.Errorf("line %d: malformed array table", lineNo+1)
			}
			if err := appendTOMLTable(root, path); err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNo+1, err)
			}
			current = path
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return nil, fmt.Errorf("line %d: malformed table", lineNo+1)
			}
			path, err := parseTOMLKeyPath(strings.TrimSpace(line[1 : len(line)-1]))
			if err != nil || len(path) == 0 {
				return nil, fmt.Errorf("line %d: malformed table", lineNo+1)
			}
			if err := ensureTOMLTable(root, path); err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNo+1, err)
			}
			current = path
			continue
		}

		keyText, valueText, ok := splitTOMLAssignment(line)
		if !ok {
			return nil, fmt.Errorf("line %d: expected key = value", lineNo+1)
		}
		keys, err := parseTOMLKeyPath(keyText)
		if err != nil || len(keys) == 0 {
			return nil, fmt.Errorf("line %d: malformed key", lineNo+1)
		}
		valueText = strings.TrimSpace(valueText)
		for needsMoreTOMLValue(valueText) {
			lineNo++
			if lineNo >= len(lines) {
				return nil, fmt.Errorf("line %d: unterminated value", lineNo)
			}
			valueText += "\n" + trimTOMLComment(lines[lineNo])
		}
		value, err := parseTOMLValue(strings.TrimSpace(valueText))
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo+1, err)
		}
		if err := setTOMLValue(root, append(append([]string(nil), current...), keys...), value); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo+1, err)
		}
	}
	return root, nil
}

func trimTOMLComment(line string) string {
	var quote byte
	triple := false
	escaped := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if quote != 0 {
			if quote == '"' && !triple && escaped {
				escaped = false
				continue
			}
			if quote == '"' && !triple && c == '\\' {
				escaped = true
				continue
			}
			if triple {
				if i+2 < len(line) && line[i:i+3] == "\"\"\"" {
					quote, triple = 0, false
					i += 2
				}
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '#' {
			return line[:i]
		}
		if c == '"' || c == '\'' {
			quote = c
			if c == '"' && i+2 < len(line) && line[i:i+3] == "\"\"\"" {
				triple = true
				i += 2
			}
		}
	}
	return line
}

func splitTOMLAssignment(line string) (string, string, bool) {
	var quote byte
	triple := false
	escaped := false
	depth := 0
	for i := 0; i < len(line); i++ {
		c := line[i]
		if quote != 0 {
			if quote == '"' && !triple && escaped {
				escaped = false
				continue
			}
			if quote == '"' && !triple && c == '\\' {
				escaped = true
				continue
			}
			if triple {
				if i+2 < len(line) && line[i:i+3] == "\"\"\"" {
					quote, triple = 0, false
					i += 2
				}
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
			if c == '"' && i+2 < len(line) && line[i:i+3] == "\"\"\"" {
				triple = true
				i += 2
			}
		case '[', '{':
			depth++
		case ']', '}':
			if depth > 0 {
				depth--
			}
		case '=':
			if depth == 0 {
				return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:]), true
			}
		}
	}
	return "", "", false
}

func needsMoreTOMLValue(value string) bool {
	var quote byte
	triple := false
	escaped := false
	depth := 0
	for i := 0; i < len(value); i++ {
		c := value[i]
		if quote != 0 {
			if quote == '"' && !triple && escaped {
				escaped = false
				continue
			}
			if quote == '"' && !triple && c == '\\' {
				escaped = true
				continue
			}
			if triple {
				if i+2 < len(value) && value[i:i+3] == "\"\"\"" {
					quote, triple = 0, false
					i += 2
				}
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
			if c == '"' && i+2 < len(value) && value[i:i+3] == "\"\"\"" {
				triple = true
				i += 2
			}
		case '[', '{':
			depth++
		case ']', '}':
			if depth > 0 {
				depth--
			}
		}
	}
	return quote != 0 || triple || depth != 0
}

func parseTOMLKeyPath(text string) ([]string, error) {
	parts := splitTOMLTopLevel(text, '.')
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("empty key segment")
		}
		if len(part) >= 2 && ((part[0] == '"' && part[len(part)-1] == '"') || (part[0] == '\'' && part[len(part)-1] == '\'')) {
			if part[0] == '"' {
				value, err := strconv.Unquote(part)
				if err != nil {
					return nil, fmt.Errorf("invalid quoted key")
				}
				part = value
			} else {
				part = part[1 : len(part)-1]
			}
		}
		if strings.TrimSpace(part) == "" {
			return nil, fmt.Errorf("empty key segment")
		}
		result = append(result, part)
	}
	return result, nil
}

func splitTOMLTopLevel(text string, separator byte) []string {
	var quote byte
	triple := false
	escaped := false
	depth := 0
	start := 0
	result := []string{}
	for i := 0; i < len(text); i++ {
		c := text[i]
		if quote != 0 {
			if quote == '"' && !triple && escaped {
				escaped = false
				continue
			}
			if quote == '"' && !triple && c == '\\' {
				escaped = true
				continue
			}
			if triple {
				if i+2 < len(text) && text[i:i+3] == "\"\"\"" {
					quote, triple = 0, false
					i += 2
				}
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
			if c == '"' && i+2 < len(text) && text[i:i+3] == "\"\"\"" {
				triple = true
				i += 2
			}
		case '[', '{':
			depth++
		case ']', '}':
			if depth > 0 {
				depth--
			}
		default:
			if c == separator && depth == 0 {
				result = append(result, text[start:i])
				start = i + 1
			}
		}
	}
	return append(result, text[start:])
}

func parseTOMLValue(text string) (any, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("empty value")
	}
	if strings.HasPrefix(text, "\"\"\"") {
		if !strings.HasSuffix(text, "\"\"\"") || len(text) < 6 {
			return nil, fmt.Errorf("unterminated multiline string")
		}
		return text[3 : len(text)-3], nil
	}
	if strings.HasPrefix(text, "'''") {
		if !strings.HasSuffix(text, "'''") || len(text) < 6 {
			return nil, fmt.Errorf("unterminated multiline string")
		}
		return text[3 : len(text)-3], nil
	}
	if text[0] == '"' {
		if len(text) < 2 || text[len(text)-1] != '"' {
			return nil, fmt.Errorf("unterminated string")
		}
		value, err := strconv.Unquote(text)
		if err != nil {
			return nil, fmt.Errorf("invalid string")
		}
		return value, nil
	}
	if text[0] == '\'' {
		if len(text) < 2 || text[len(text)-1] != '\'' {
			return nil, fmt.Errorf("unterminated string")
		}
		return text[1 : len(text)-1], nil
	}
	if strings.HasPrefix(text, "[") {
		if !strings.HasSuffix(text, "]") {
			return nil, fmt.Errorf("unterminated array")
		}
		inner := strings.TrimSpace(text[1 : len(text)-1])
		if inner == "" {
			return []any{}, nil
		}
		parts := splitTOMLTopLevel(inner, ',')
		values := make([]any, 0, len(parts))
		for _, part := range parts {
			value, err := parseTOMLValue(part)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		return values, nil
	}
	if strings.HasPrefix(text, "{") {
		if !strings.HasSuffix(text, "}") {
			return nil, fmt.Errorf("unterminated inline table")
		}
		inner := strings.TrimSpace(text[1 : len(text)-1])
		values := make(map[string]any)
		if inner == "" {
			return values, nil
		}
		for _, part := range splitTOMLTopLevel(inner, ',') {
			key, valueText, ok := splitTOMLAssignment(strings.TrimSpace(part))
			if !ok {
				return nil, fmt.Errorf("malformed inline table")
			}
			keys, err := parseTOMLKeyPath(key)
			if err != nil || len(keys) != 1 {
				return nil, fmt.Errorf("malformed inline table key")
			}
			value, err := parseTOMLValue(valueText)
			if err != nil {
				return nil, err
			}
			if _, exists := values[keys[0]]; exists {
				return nil, fmt.Errorf("duplicate inline table key %q", keys[0])
			}
			values[keys[0]] = value
		}
		return values, nil
	}
	if text == "true" || text == "false" {
		return text == "true", nil
	}
	numeric := strings.ReplaceAll(text, "_", "")
	if integer, err := strconv.ParseInt(numeric, 0, 64); err == nil {
		return integer, nil
	}
	if floating, err := strconv.ParseFloat(numeric, 64); err == nil {
		return floating, nil
	}
	// Date/time values are not used for capability identity.  Preserve them
	// as opaque strings so a valid config remains parseable.
	if looksLikeTOMLDate(text) {
		return text, nil
	}
	return nil, fmt.Errorf("unsupported value")
}

func looksLikeTOMLDate(text string) bool {
	if len(text) < 10 {
		return false
	}
	return text[4] == '-' && text[7] == '-' && unicode.IsDigit(rune(text[0])) && unicode.IsDigit(rune(text[1]))
}

func ensureTOMLTable(root map[string]any, path []string) error {
	current := root
	for _, key := range path {
		value, exists := current[key]
		if !exists {
			nested := make(map[string]any)
			current[key] = nested
			current = nested
			continue
		}
		switch typed := value.(type) {
		case map[string]any:
			current = typed
		case []any:
			if len(typed) == 0 {
				return fmt.Errorf("table %q has no array entries", key)
			}
			last, ok := typed[len(typed)-1].(map[string]any)
			if !ok {
				return fmt.Errorf("table %q is not an object array", key)
			}
			current = last
		default:
			return fmt.Errorf("table %q conflicts with a value", key)
		}
	}
	return nil
}

func appendTOMLTable(root map[string]any, path []string) error {
	if len(path) == 0 {
		return fmt.Errorf("empty array table")
	}
	parent := root
	for _, key := range path[:len(path)-1] {
		value, exists := parent[key]
		if !exists {
			nested := make(map[string]any)
			parent[key] = nested
			parent = nested
			continue
		}
		switch typed := value.(type) {
		case map[string]any:
			parent = typed
		case []any:
			if len(typed) == 0 {
				return fmt.Errorf("array table parent %q is empty", key)
			}
			last, ok := typed[len(typed)-1].(map[string]any)
			if !ok {
				return fmt.Errorf("array table parent %q is not an object array", key)
			}
			parent = last
		default:
			return fmt.Errorf("array table parent %q conflicts with a value", key)
		}
	}
	key := path[len(path)-1]
	value, exists := parent[key]
	if !exists {
		parent[key] = []any{map[string]any{}}
		return nil
	}
	array, ok := value.([]any)
	if !ok {
		return fmt.Errorf("array table %q conflicts with a value", key)
	}
	parent[key] = append(array, map[string]any{})
	return nil
}

func setTOMLValue(root map[string]any, path []string, value any) error {
	if len(path) == 0 {
		return fmt.Errorf("empty key")
	}
	parentPath, key := path[:len(path)-1], path[len(path)-1]
	if err := ensureTOMLTable(root, parentPath); err != nil {
		return err
	}
	parent, err := tomlMapAt(root, parentPath)
	if err != nil {
		return err
	}
	if _, exists := parent[key]; exists {
		return fmt.Errorf("duplicate key %q", strings.Join(path, "."))
	}
	parent[key] = value
	return nil
}

func tomlMapAt(root map[string]any, path []string) (map[string]any, error) {
	current := root
	for _, key := range path {
		value, exists := current[key]
		if !exists {
			nested := make(map[string]any)
			current[key] = nested
			current = nested
			continue
		}
		switch typed := value.(type) {
		case map[string]any:
			current = typed
		case []any:
			if len(typed) == 0 {
				return nil, fmt.Errorf("array table %q is empty", key)
			}
			last, ok := typed[len(typed)-1].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("array table %q is not an object array", key)
			}
			current = last
		default:
			return nil, fmt.Errorf("key %q is not a table", key)
		}
	}
	return current, nil
}
