package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// orderedJSON is a small JSON AST. encoding/json's map representation loses
// object order, which is needlessly noisy for a configuration manager. We
// retain object field order and scalar spelling while changing only the
// structural nodes touched by a manager operation.
type jsonKind uint8

const (
	jsonScalar jsonKind = iota
	jsonObjectKind
	jsonArrayKind
)

type jsonNode struct {
	kind     jsonKind
	raw      []byte
	fields   []jsonField
	elements []*jsonNode
}

type jsonField struct {
	key   string
	value *jsonNode
}

func newJSONObject() *jsonNode { return &jsonNode{kind: jsonObjectKind} }

func newJSONArray() *jsonNode { return &jsonNode{kind: jsonArrayKind} }

func newJSONString(value string) *jsonNode {
	return &jsonNode{kind: jsonScalar, raw: []byte(strconv.Quote(value))}
}

func newJSONBool(value bool) *jsonNode {
	if value {
		return &jsonNode{kind: jsonScalar, raw: []byte("true")}
	}
	return &jsonNode{kind: jsonScalar, raw: []byte("false")}
}

func newJSONNumber(value int) *jsonNode {
	return &jsonNode{kind: jsonScalar, raw: []byte(strconv.Itoa(value))}
}

func (n *jsonNode) field(name string) (*jsonNode, bool, error) {
	if n == nil || n.kind != jsonObjectKind {
		return nil, false, errors.New("JSON value is not an object")
	}
	var found *jsonNode
	for _, field := range n.fields {
		if field.key != name {
			continue
		}
		if found != nil {
			return nil, false, fmt.Errorf("JSON object contains duplicate %q fields", name)
		}
		found = field.value
	}
	return found, found != nil, nil
}

func (n *jsonNode) setField(name string, value *jsonNode) error {
	if n == nil || n.kind != jsonObjectKind {
		return errors.New("JSON value is not an object")
	}
	for index := range n.fields {
		if n.fields[index].key == name {
			for next := index + 1; next < len(n.fields); next++ {
				if n.fields[next].key == name {
					return fmt.Errorf("JSON object contains duplicate %q fields", name)
				}
			}
			n.fields[index].value = value
			return nil
		}
	}
	n.fields = append(n.fields, jsonField{key: name, value: value})
	return nil
}

func (n *jsonNode) removeField(name string) error {
	if n == nil || n.kind != jsonObjectKind {
		return errors.New("JSON value is not an object")
	}
	for index, field := range n.fields {
		if field.key != name {
			continue
		}
		for next := index + 1; next < len(n.fields); next++ {
			if n.fields[next].key == name {
				return fmt.Errorf("JSON object contains duplicate %q fields", name)
			}
		}
		n.fields = append(n.fields[:index], n.fields[index+1:]...)
		return nil
	}
	return nil
}

func (n *jsonNode) stringValue() (string, bool) {
	if n == nil || n.kind != jsonScalar {
		return "", false
	}
	var value string
	if err := json.Unmarshal(n.raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func (n *jsonNode) boolValue() (bool, bool) {
	if n == nil || n.kind != jsonScalar {
		return false, false
	}
	switch string(n.raw) {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}

func (n *jsonNode) numberValue() (int, bool) {
	if n == nil || n.kind != jsonScalar {
		return 0, false
	}
	value, err := strconv.Atoi(string(n.raw))
	return value, err == nil
}

func (n *jsonNode) encode(buffer *bytes.Buffer, depth int) {
	if n == nil {
		buffer.WriteString("null")
		return
	}
	switch n.kind {
	case jsonScalar:
		buffer.Write(n.raw)
	case jsonObjectKind:
		if len(n.fields) == 0 {
			buffer.WriteString("{}")
			return
		}
		buffer.WriteString("{\n")
		for index, field := range n.fields {
			writeIndent(buffer, depth+1)
			key, _ := json.Marshal(field.key)
			buffer.Write(key)
			buffer.WriteString(": ")
			field.value.encode(buffer, depth+1)
			if index+1 < len(n.fields) {
				buffer.WriteByte(',')
			}
			buffer.WriteByte('\n')
		}
		writeIndent(buffer, depth)
		buffer.WriteByte('}')
	case jsonArrayKind:
		if len(n.elements) == 0 {
			buffer.WriteString("[]")
			return
		}
		buffer.WriteString("[\n")
		for index, element := range n.elements {
			writeIndent(buffer, depth+1)
			element.encode(buffer, depth+1)
			if index+1 < len(n.elements) {
				buffer.WriteByte(',')
			}
			buffer.WriteByte('\n')
		}
		writeIndent(buffer, depth)
		buffer.WriteByte(']')
	}
}

func writeIndent(buffer *bytes.Buffer, depth int) {
	for index := 0; index < depth; index++ {
		buffer.WriteString("  ")
	}
}

func (n *jsonNode) bytes() []byte {
	var buffer bytes.Buffer
	n.encode(&buffer, 0)
	buffer.WriteByte('\n')
	return buffer.Bytes()
}

func parseOrderedJSON(data []byte) (*jsonNode, error) {
	position := skipJSONSpace(data, 0)
	node, next, err := parseJSONValue(data, position)
	if err != nil {
		return nil, err
	}
	if next = skipJSONSpace(data, next); next != len(data) {
		return nil, fmt.Errorf("unexpected JSON data at byte %d", next)
	}
	return node, nil
}

func skipJSONSpace(data []byte, position int) int {
	for position < len(data) {
		switch data[position] {
		case ' ', '\t', '\r', '\n':
			position++
		default:
			return position
		}
	}
	return position
}

func parseJSONValue(data []byte, position int) (*jsonNode, int, error) {
	position = skipJSONSpace(data, position)
	if position >= len(data) {
		return nil, position, errors.New("unexpected end of JSON")
	}
	switch data[position] {
	case '{':
		return parseJSONObject(data, position)
	case '[':
		return parseJSONArray(data, position)
	case '"':
		end, err := scanJSONString(data, position)
		if err != nil {
			return nil, position, err
		}
		raw := data[position:end]
		if !json.Valid(raw) {
			return nil, position, fmt.Errorf("invalid JSON string at byte %d", position)
		}
		return &jsonNode{kind: jsonScalar, raw: append([]byte(nil), raw...)}, end, nil
	default:
		end := position
		for end < len(data) {
			switch data[end] {
			case ' ', '\t', '\r', '\n', ',', ']', '}':
				goto scalarDone
			default:
				end++
			}
		}
	scalarDone:
		if end == position {
			return nil, position, fmt.Errorf("unexpected JSON delimiter at byte %d", position)
		}
		raw := data[position:end]
		if !json.Valid(raw) {
			return nil, position, fmt.Errorf("invalid JSON value at byte %d", position)
		}
		return &jsonNode{kind: jsonScalar, raw: append([]byte(nil), raw...)}, end, nil
	}
}

func parseJSONObject(data []byte, position int) (*jsonNode, int, error) {
	node := newJSONObject()
	position++ // {
	position = skipJSONSpace(data, position)
	if position < len(data) && data[position] == '}' {
		return node, position + 1, nil
	}
	for {
		position = skipJSONSpace(data, position)
		if position >= len(data) || data[position] != '"' {
			return nil, position, errors.New("JSON object key must be a string")
		}
		end, err := scanJSONString(data, position)
		if err != nil {
			return nil, position, err
		}
		var key string
		if err := json.Unmarshal(data[position:end], &key); err != nil {
			return nil, position, fmt.Errorf("decode JSON object key: %w", err)
		}
		position = skipJSONSpace(data, end)
		if position >= len(data) || data[position] != ':' {
			return nil, position, errors.New("JSON object key must be followed by a colon")
		}
		value, next, err := parseJSONValue(data, position+1)
		if err != nil {
			return nil, position, err
		}
		node.fields = append(node.fields, jsonField{key: key, value: value})
		position = skipJSONSpace(data, next)
		if position >= len(data) {
			return nil, position, errors.New("unterminated JSON object")
		}
		switch data[position] {
		case ',':
			position++
			continue
		case '}':
			return node, position + 1, nil
		default:
			return nil, position, errors.New("JSON object requires comma or closing brace")
		}
	}
}

func parseJSONArray(data []byte, position int) (*jsonNode, int, error) {
	node := newJSONArray()
	position++ // [
	position = skipJSONSpace(data, position)
	if position < len(data) && data[position] == ']' {
		return node, position + 1, nil
	}
	for {
		value, next, err := parseJSONValue(data, position)
		if err != nil {
			return nil, position, err
		}
		node.elements = append(node.elements, value)
		position = skipJSONSpace(data, next)
		if position >= len(data) {
			return nil, position, errors.New("unterminated JSON array")
		}
		switch data[position] {
		case ',':
			position++
			continue
		case ']':
			return node, position + 1, nil
		default:
			return nil, position, errors.New("JSON array requires comma or closing bracket")
		}
	}
}

func scanJSONString(data []byte, position int) (int, error) {
	if position >= len(data) || data[position] != '"' {
		return position, errors.New("JSON string must start with a quote")
	}
	position++
	for position < len(data) {
		switch data[position] {
		case '\\':
			position += 2
		case '"':
			return position + 1, nil
		default:
			position++
		}
	}
	return position, errors.New("unterminated JSON string")
}

func (n *jsonNode) hasOnlyField(name string) bool {
	return n != nil && n.kind == jsonObjectKind && len(n.fields) == 1 && n.fields[0].key == name
}

func (n *jsonNode) isEmptyObject() bool {
	return n != nil && n.kind == jsonObjectKind && len(n.fields) == 0
}

func (n *jsonNode) isEmptyArray() bool {
	return n != nil && n.kind == jsonArrayKind && len(n.elements) == 0
}

func shellWords(command string) ([]string, bool) {
	// Codex stores shell-form commands. This parser intentionally accepts only
	// fixed, safely quoted words; operators, substitutions, redirects, and
	// comments make a command a non-match rather than an owned command.
	var words []string
	var current strings.Builder
	inSingle, inDouble, escaped, wordStarted := false, false, false, false
	flush := func() {
		if wordStarted {
			words = append(words, current.String())
			current.Reset()
			wordStarted = false
		}
	}
	for index := 0; index < len(command); index++ {
		character := command[index]
		if escaped {
			if inSingle {
				return nil, false
			}
			current.WriteByte(character)
			wordStarted = true
			escaped = false
			continue
		}
		if inSingle {
			if character == '\'' {
				inSingle = false
			} else {
				current.WriteByte(character)
			}
			wordStarted = true
			continue
		}
		if inDouble {
			switch character {
			case '"':
				inDouble = false
			case '\\':
				escaped = true
			default:
				current.WriteByte(character)
			}
			wordStarted = true
			continue
		}
		switch character {
		case '\\':
			escaped = true
			wordStarted = true
		case '\'':
			inSingle = true
			wordStarted = true
		case '"':
			inDouble = true
			wordStarted = true
		case ' ', '\t', '\r', '\n':
			flush()
		case '|', '&', ';', '>', '<', '`', '$', '(', ')', '*', '?', '#':
			return nil, false
		default:
			current.WriteByte(character)
			wordStarted = true
		}
	}
	if escaped || inSingle || inDouble {
		return nil, false
	}
	flush()
	return words, true
}
