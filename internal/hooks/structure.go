package hooks

import (
	"errors"
	"fmt"
	"strings"
)

func expectedArgs(runtime Runtime) []string {
	return []string{
		"ingest",
		"--runtime",
		string(runtime),
		ManagedFlag,
		ManagedMarker,
	}
}

func expectedCodexCommand() string {
	// Every token is a fixed shell-safe word. Keep this exact string stable so
	// recognition never relies on a fragile substring search.
	return strings.Join(append([]string{BinaryName}, expectedArgs(RuntimeCodex)...), " ")
}

func isExactHandler(runtime Runtime, event string, handler *jsonNode) bool {
	if handler == nil || handler.kind != jsonObjectKind {
		return false
	}
	typeValue, typeFound, typeErr := handler.field("type")
	if typeErr != nil || !typeFound {
		return false
	}
	typeName, ok := typeValue.stringValue()
	if !ok || typeName != "command" {
		return false
	}
	commandValue, commandFound, commandErr := handler.field("command")
	if commandErr != nil || !commandFound {
		return false
	}
	command, ok := commandValue.stringValue()
	if !ok {
		return false
	}
	asyncValue, asyncFound, asyncErr := handler.field("async")
	if asyncErr != nil || !asyncFound {
		return false
	}
	async, ok := asyncValue.boolValue()
	if !ok || !async {
		return false
	}
	timeoutValue, timeoutFound, timeoutErr := handler.field("timeout")
	if timeoutErr != nil || !timeoutFound {
		return false
	}
	timeout, ok := timeoutValue.numberValue()
	if !ok || timeout != hookTimeout {
		return false
	}
	if runtime == RuntimeCodex {
		if event != "PostToolUse" || command != expectedCodexCommand() {
			return false
		}
		// Codex's documented hooks.json form is shell-command based. An args
		// field would be a different shape, so it cannot be our owned entry.
		_, argsFound, argsErr := handler.field("args")
		return argsErr == nil && !argsFound
	}
	if runtime != RuntimeClaude || command != BinaryName {
		return false
	}
	argsValue, argsFound, argsErr := handler.field("args")
	if argsErr != nil || !argsFound || argsValue.kind != jsonArrayKind {
		return false
	}
	wantArgs := expectedArgs(RuntimeClaude)
	if len(argsValue.elements) != len(wantArgs) {
		return false
	}
	for index, element := range argsValue.elements {
		value, ok := element.stringValue()
		if !ok || value != wantArgs[index] {
			return false
		}
	}
	return event == "PostToolUse" || event == "PostToolUseFailure" || event == "UserPromptExpansion"
}

func isPartialHandler(runtime Runtime, event string, handler *jsonNode) bool {
	if handler == nil || handler.kind != jsonObjectKind {
		return false
	}
	typeValue, typeFound, typeErr := handler.field("type")
	if typeErr != nil || !typeFound {
		return false
	}
	typeName, typeOK := typeValue.stringValue()
	if !typeOK || typeName != "command" {
		return false
	}
	commandValue, commandFound, commandErr := handler.field("command")
	if commandErr != nil || !commandFound {
		return false
	}
	command, commandOK := commandValue.stringValue()
	if !commandOK {
		return false
	}
	if runtime == RuntimeClaude {
		if command != BinaryName {
			return false
		}
		// A command-shaped handler with the owned executable is a stale or
		// partial candidate even if its marker, args, async, or timeout changed.
		return event == "PostToolUse" || event == "PostToolUseFailure" || event == "UserPromptExpansion"
	}
	if runtime != RuntimeCodex || event != "PostToolUse" {
		return false
	}
	if command == BinaryName {
		// A stale Codex entry can be represented with an exec-style args
		// field even though the documented Codex form is one shell string.
		// Treat it as partial when the owned marker or hook metadata is present.
		argsValue, argsFound, argsErr := handler.field("args")
		if argsErr == nil && argsFound && argsValue.kind == jsonArrayKind {
			for _, argument := range argsValue.elements {
				value, ok := argument.stringValue()
				if ok && (value == ManagedMarker || value == ManagedFlag || strings.HasPrefix(value, "harness-lint-hooks/")) {
					return true
				}
			}
		}
		_, asyncFound, asyncErr := handler.field("async")
		_, timeoutFound, timeoutErr := handler.field("timeout")
		return asyncErr == nil && timeoutErr == nil && (asyncFound || timeoutFound)
	}
	words, safe := shellWords(command)
	if !safe || len(words) == 0 || words[0] != BinaryName {
		return false
	}
	for _, word := range words[1:] {
		if word == ManagedFlag || word == ManagedMarker || strings.HasPrefix(word, "--runtime") || strings.HasPrefix(word, "harness-lint-hooks/") {
			return true
		}
	}
	// An exact executable with an incomplete command is also a candidate if it
	// clearly resembles the stable ingest entrypoint; do not classify a plain
	// user hook such as "harness-lint --help" as owned.
	return len(words) > 1 && words[1] == "ingest"
}

func isLookalikeHandler(runtime Runtime, event string, handler *jsonNode) bool {
	if handler == nil || handler.kind != jsonObjectKind {
		return false
	}
	typeValue, typeFound, typeErr := handler.field("type")
	if typeErr != nil || !typeFound {
		return false
	}
	typeName, ok := typeValue.stringValue()
	if !ok || typeName != "command" {
		return false
	}
	commandValue, commandFound, commandErr := handler.field("command")
	if commandErr != nil || !commandFound {
		return false
	}
	command, ok := commandValue.stringValue()
	if !ok {
		return false
	}
	if runtime == RuntimeClaude {
		if event != "PostToolUse" && event != "PostToolUseFailure" && event != "UserPromptExpansion" {
			return false
		}
		if strings.Contains(command, BinaryName) && command != BinaryName {
			return true
		}
		return false
	}
	if runtime == RuntimeCodex && event == "PostToolUse" {
		words, safe := shellWords(command)
		return safe && len(words) > 0 && words[0] != BinaryName && strings.Contains(command, BinaryName)
	}
	return false
}

func newManagedHandler(runtime Runtime, event string) *jsonNode {
	handler := newJSONObject()
	_ = handler.setField("type", newJSONString("command"))
	if runtime == RuntimeClaude {
		_ = handler.setField("command", newJSONString(BinaryName))
		args := newJSONArray()
		for _, argument := range expectedArgs(RuntimeClaude) {
			args.elements = append(args.elements, newJSONString(argument))
		}
		_ = handler.setField("args", args)
	} else {
		_ = handler.setField("command", newJSONString(expectedCodexCommand()))
	}
	_ = handler.setField("async", newJSONBool(true))
	_ = handler.setField("timeout", newJSONNumber(hookTimeout))
	return handler
}

func newManagedGroup(runtime Runtime, event string) *jsonNode {
	group := newJSONObject()
	handlers := newJSONArray()
	handlers.elements = append(handlers.elements, newManagedHandler(runtime, event))
	_ = group.setField("hooks", handlers)
	return group
}

func (m *manager) addManagedHooks(root *jsonNode) (bool, error) {
	if root == nil || root.kind != jsonObjectKind {
		return false, errors.New("configuration root must be an object")
	}
	hooks, found, err := root.field("hooks")
	if err != nil {
		return false, err
	}
	if !found {
		hooks = newJSONObject()
		if err := root.setField("hooks", hooks); err != nil {
			return false, err
		}
	}
	if hooks.kind != jsonObjectKind {
		return false, errors.New("hooks must be a JSON object")
	}
	changed := false
	for _, event := range expectedEvents(m.runtime) {
		eventValue, found, err := hooks.field(event)
		if err != nil {
			return changed, err
		}
		if !found {
			eventValue = newJSONArray()
			if err := hooks.setField(event, eventValue); err != nil {
				return changed, err
			}
		}
		if eventValue.kind != jsonArrayKind {
			return changed, fmt.Errorf("hooks.%s must be an array", event)
		}
		if eventHasExactManagedHandler(m.runtime, event, eventValue) {
			continue
		}
		eventValue.elements = append(eventValue.elements, newManagedGroup(m.runtime, event))
		changed = true
	}
	return changed, nil
}

func eventHasExactManagedHandler(runtime Runtime, event string, eventValue *jsonNode) bool {
	if eventValue == nil || eventValue.kind != jsonArrayKind {
		return false
	}
	for _, group := range eventValue.elements {
		if !managedGroupShape(group) {
			continue
		}
		handlers, found, err := group.field("hooks")
		if err != nil || !found || handlers.kind != jsonArrayKind {
			continue
		}
		for _, handler := range handlers.elements {
			if isExactHandler(runtime, event, handler) {
				return true
			}
		}
	}
	return false
}

func (m *manager) removeManagedHooks(root *jsonNode) (bool, error) {
	if root == nil || root.kind != jsonObjectKind {
		return false, errors.New("configuration root must be an object")
	}
	hooks, found, err := root.field("hooks")
	if err != nil || !found {
		return false, err
	}
	if hooks.kind != jsonObjectKind {
		return false, errors.New("hooks must be a JSON object")
	}
	changed := false
	for _, event := range expectedEvents(m.runtime) {
		eventValue, found, err := hooks.field(event)
		if err != nil {
			return changed, err
		}
		if !found {
			continue
		}
		if eventValue.kind != jsonArrayKind {
			return changed, fmt.Errorf("hooks.%s must be an array", event)
		}
		groups := make([]*jsonNode, 0, len(eventValue.elements))
		for _, group := range eventValue.elements {
			if group.kind != jsonObjectKind {
				return changed, fmt.Errorf("hooks.%s contains a non-object group", event)
			}
			handlers, found, err := group.field("hooks")
			if err != nil {
				return changed, err
			}
			if !found || handlers.kind != jsonArrayKind {
				return changed, fmt.Errorf("hooks.%s group has no handler array", event)
			}
			remaining := make([]*jsonNode, 0, len(handlers.elements))
			removed := false
			for _, handler := range handlers.elements {
				if managedGroupShape(group) && isExactHandler(m.runtime, event, handler) {
					removed = true
					changed = true
					continue
				}
				remaining = append(remaining, handler)
			}
			if !removed {
				groups = append(groups, group)
				continue
			}
			if len(remaining) == 0 && groupHasNoPreservableFields(group) {
				// The group contained only managed handlers. Remove that empty
				// matcher group, but retain any group-level unknown fields.
				continue
			}
			handlers.elements = remaining
			groups = append(groups, group)
		}
		eventValue.elements = groups
		if len(groups) == 0 {
			if err := hooks.removeField(event); err != nil {
				return changed, err
			}
		}
	}
	if hooks.isEmptyObject() {
		if err := root.removeField("hooks"); err != nil {
			return changed, err
		}
	}
	return changed, nil
}

func managedGroupShape(group *jsonNode) bool {
	if group == nil || group.kind != jsonObjectKind {
		return false
	}
	// Managed groups intentionally omit matcher. A copied command under a
	// user matcher is not owned by this manager and must remain untouched.
	_, matcherFound, matcherErr := group.field("matcher")
	return matcherErr == nil && !matcherFound
}

func groupHasNoPreservableFields(group *jsonNode) bool {
	if group == nil || group.kind != jsonObjectKind {
		return false
	}
	// A group with any field besides hooks carries user data that should not be
	// silently discarded when its managed handler is removed.
	return len(group.fields) == 1 && group.fields[0].key == "hooks"
}

func cloneJSON(node *jsonNode) *jsonNode {
	if node == nil {
		return nil
	}
	result := &jsonNode{kind: node.kind, raw: append([]byte(nil), node.raw...)}
	if node.kind == jsonObjectKind {
		result.fields = make([]jsonField, 0, len(node.fields))
		for _, field := range node.fields {
			result.fields = append(result.fields, jsonField{key: field.key, value: cloneJSON(field.value)})
		}
	} else if node.kind == jsonArrayKind {
		result.elements = make([]*jsonNode, 0, len(node.elements))
		for _, element := range node.elements {
			result.elements = append(result.elements, cloneJSON(element))
		}
	}
	return result
}
