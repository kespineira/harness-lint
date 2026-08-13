package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
)

type usageContext struct {
	timestamp time.Time
	session   string
	project   string
}

type usageCandidate struct {
	typ       domain.CapabilityType
	name      string
	eventType domain.EventType
	context   usageContext
}

func (a *Adapter) importTranscriptUsage(ctx context.Context, since time.Time) ([]domain.UsageEvent, error) {
	files, err := transcriptFiles(a.options.transcriptRoots)
	if err != nil {
		return nil, err
	}
	var result []domain.UsageEvent
	seen := make(map[string]struct{})
	for _, path := range files {
		if err := contextErr(ctx); err != nil {
			return nil, err
		}
		fallbackSession := transcriptSessionIdentifier(path)
		fallbackProject := a.options.repoRoot
		if a.options.repoRoot == "" {
			fallbackProject = filepath.Dir(path)
		}
		fileContext := usageContext{session: fallbackSession, project: fallbackProject}
		visit := func(value any) error {
			fileContext = persistentUsageContext(value, fileContext)
			base := fileContext
			candidates := make([]usageCandidate, 0, 2)
			collectUsageCandidates(value, base, &candidates)
			for _, candidate := range candidates {
				if err := contextErr(ctx); err != nil {
					return err
				}
				if candidate.context.timestamp.IsZero() {
					continue
				}
				timestamp := candidate.context.timestamp.UTC()
				if !since.IsZero() && timestamp.Before(since.UTC()) {
					continue
				}
				sessionID := candidate.context.session
				if sessionID == "" {
					sessionID = fallbackSession
				}
				projectID := candidate.context.project
				if projectID == "" {
					projectID = fallbackProject
				}
				event := domain.UsageEvent{
					Timestamp:      timestamp,
					Runtime:        domain.RuntimeCodex,
					SessionID:      hashIdentifier(sessionID),
					ProjectID:      hashIdentifier(projectID),
					CapabilityType: candidate.typ,
					CapabilityName: candidate.name,
					EventType:      candidate.eventType,
				}
				fingerprint, err := domain.FingerprintForUsageEvent(event)
				if err != nil {
					continue
				}
				if _, exists := seen[fingerprint]; exists {
					continue
				}
				seen[fingerprint] = struct{}{}
				event.Fingerprint = fingerprint
				result = append(result, event)
			}
			return nil
		}
		if err := decodeTranscriptFile(ctx, path, visit); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func transcriptSessionIdentifier(path string) string {
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if strings.HasPrefix(stem, "rollout-") {
		// Codex rollout names use rollout-<timestamp>-<thread-id>.  Keep
		// the full thread id when the conventional timestamp prefix is
		// present; it is an opaque identifier and is hashed before return.
		rest := strings.TrimPrefix(stem, "rollout-")
		if len(rest) > 20 && rest[19] == '-' {
			return rest[20:]
		}
	}
	if stem != "" {
		return stem
	}
	return path
}

func transcriptFiles(roots []string) ([]string, error) {
	seen := make(map[string]struct{})
	var result []string
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		info, err := os.Lstat(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, err := os.Stat(root)
			if err != nil {
				continue
			}
			if resolved.IsDir() {
				if evaluated, evalErr := filepath.EvalSymlinks(root); evalErr == nil {
					root = evaluated
					info = resolved
				}
			} else {
				info = resolved
			}
		}
		if !info.IsDir() {
			if supportedTranscriptExtension(root) {
				if _, exists := seen[root]; !exists {
					seen[root] = struct{}{}
					result = append(result, root)
				}
			}
			continue
		}
		err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				if _, statErr := os.Stat(path); statErr != nil {
					return nil
				}
				return nil
			}
			if entry.IsDir() || !supportedTranscriptExtension(path) {
				return nil
			}
			if _, exists := seen[path]; !exists {
				seen[path] = struct{}{}
				result = append(result, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(result)
	return result, nil
}

func supportedTranscriptExtension(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".jsonl" || ext == ".json"
}

func decodeTranscriptFile(ctx context.Context, path string, visit func(any) error) error {
	file, err := os.Open(path)
	if err != nil {
		// Transcripts are best-effort and may be rotated during a scan.
		return nil
	}
	defer file.Close()
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".jsonl" {
		scanner := bufio.NewScanner(file)
		buffer := make([]byte, 64*1024)
		scanner.Buffer(buffer, 8*1024*1024)
		for scanner.Scan() {
			if err := contextErr(ctx); err != nil {
				return err
			}
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var value any
			decoder := json.NewDecoder(strings.NewReader(line))
			decoder.UseNumber()
			if err := decoder.Decode(&value); err != nil {
				continue
			}
			if err := visit(value); err != nil {
				return err
			}
		}
		return scanner.Err()
	}
	decoder := json.NewDecoder(file)
	decoder.UseNumber()
	for {
		var value any
		err := decoder.Decode(&value)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return nil
		}
		if err := visit(value); err != nil {
			return err
		}
	}
}

func collectUsageCandidates(value any, inherited usageContext, result *[]usageCandidate) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			collectUsageCandidates(item, inherited, result)
		}
	case map[string]any:
		current := inherited
		if timestamp, ok := timestampFromMap(typed); ok {
			current.timestamp = timestamp
		}
		if session, ok := identifierField(typed, sessionKeys...); ok {
			current.session = session
		}
		if project, ok := identifierField(typed, projectKeys...); ok {
			current.project = project
		}
		if candidate, ok := toolCandidate(typed, current); ok {
			*result = append(*result, candidate)
		}
		if candidate, ok := skillCandidate(typed, current); ok {
			*result = append(*result, candidate)
		}
		for key, child := range typed {
			if isSensitiveUsageField(key) {
				continue
			}
			collectUsageCandidates(child, current, result)
		}
	}
}

func isSensitiveUsageField(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "arguments", "argument", "input", "inputs", "tool_input", "toolinput", "tool_args", "toolargs", "prompt", "prompts", "response", "responses", "output", "outputs", "tool_output", "tooloutput", "result", "results":
		return true
	default:
		return false
	}
}

var sessionKeys = []string{
	"session_id", "sessionId", "session", "conversation_id", "conversationId", "thread_id", "threadId",
}

var projectKeys = []string{
	"project_id", "projectId", "project", "cwd", "current_directory", "currentDirectory", "repo_root", "repoRoot", "project_root", "projectRoot", "repository",
}

var timestampKeys = []string{
	"timestamp", "time", "created_at", "createdAt", "occurred_at", "occurredAt", "timestamp_ms", "timestampMs", "ts",
}

func persistentUsageContext(value any, inherited usageContext) usageContext {
	result := inherited
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			result = persistentUsageContext(item, result)
		}
	case map[string]any:
		if session, ok := identifierField(typed, sessionKeys...); ok {
			result.session = session
		}
		if project, ok := identifierField(typed, projectKeys...); ok {
			result.project = project
		}
		marker := strings.ToLower(strings.TrimSpace(stringFieldAny(typed, "type", "kind", "event_type", "eventType")))
		if strings.Contains(marker, "session") {
			if session, ok := identifierField(typed, "id", "session_id", "sessionId"); ok {
				result.session = session
			}
			if payload, ok := typed["payload"].(map[string]any); ok {
				if session, found := identifierField(payload, "id", "session_id", "sessionId", "thread_id", "threadId"); found {
					result.session = session
				}
				if project, found := identifierField(payload, projectKeys...); found {
					result.project = project
				}
			}
		}
		for key, child := range typed {
			if isSensitiveUsageField(key) {
				continue
			}
			result = persistentUsageContext(child, result)
		}
	}
	return result
}

func identifierField(values map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		value, ok := values[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				return strings.TrimSpace(typed), true
			}
		case json.Number:
			return typed.String(), true
		}
	}
	return "", false
}

func timestampFromMap(values map[string]any) (time.Time, bool) {
	for _, key := range timestampKeys {
		if value, ok := values[key]; ok {
			if timestamp, valid := parseJSONTime(value); valid {
				return timestamp, true
			}
		}
	}
	return time.Time{}, false
}

func parseJSONTime(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return time.Time{}, false
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999Z07:00"} {
			if timestamp, err := time.Parse(layout, text); err == nil {
				return timestamp, true
			}
		}
		if seconds, err := strconv.ParseInt(text, 10, 64); err == nil {
			return unixTimestamp(seconds), true
		}
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return unixTimestamp(integer), true
		}
		if floating, err := strconv.ParseFloat(typed.String(), 64); err == nil {
			seconds := int64(floating)
			nanos := int64((floating - float64(seconds)) * 1e9)
			return time.Unix(seconds, nanos), true
		}
	case float64:
		seconds := int64(typed)
		nanos := int64((typed - float64(seconds)) * 1e9)
		return time.Unix(seconds, nanos), true
	case int64:
		return unixTimestamp(typed), true
	case int:
		return unixTimestamp(int64(typed)), true
	}
	return time.Time{}, false
}

func unixTimestamp(value int64) time.Time {
	// Codex JSONL timestamps are normally seconds; accepting millisecond and
	// microsecond epochs makes the best-effort importer tolerant of hook tools.
	absolute := value
	if absolute < 0 {
		absolute = -absolute
	}
	switch {
	case absolute >= 1_000_000_000_000_000:
		return time.Unix(0, value*1000)
	case absolute >= 1_000_000_000_000:
		return time.Unix(0, value*1_000_000)
	default:
		return time.Unix(value, 0)
	}
}

func toolCandidate(values map[string]any, current usageContext) (usageCandidate, bool) {
	marker := strings.ToLower(strings.TrimSpace(stringFieldAny(values, "type", "kind", "event_type", "eventType")))
	normalizedMarker := strings.ReplaceAll(marker, "_", "")
	if strings.Contains(marker, "output") || strings.Contains(marker, "result") || strings.Contains(marker, "response") || strings.Contains(normalizedMarker, "output") || strings.Contains(normalizedMarker, "result") {
		return usageCandidate{}, false
	}
	explicit := false
	for key := range values {
		lower := strings.ToLower(key)
		if lower == "function_call" || lower == "custom_tool_call" || lower == "dynamic_tool_call" || lower == "tool_call" || lower == "tooluse" || lower == "tool_use" {
			explicit = true
			break
		}
	}
	for _, candidateMarker := range []string{"function_call", "custom_tool_call", "dynamic_tool_call", "tool_call", "function", "custom_tool", "dynamic_tool"} {
		if marker == candidateMarker {
			explicit = true
			break
		}
	}
	if strings.Contains(normalizedMarker, "functioncall") || strings.Contains(normalizedMarker, "customtoolcall") || strings.Contains(normalizedMarker, "dynamictoolcall") {
		explicit = true
	}
	if method := strings.ToLower(strings.TrimSpace(stringFieldAny(values, "method"))); method == "item/tool/call" {
		explicit = true
	}
	hookEvent := strings.ToLower(strings.TrimSpace(stringFieldAny(values, "hook_event_name", "hookEventName", "hook_event", "hookEvent")))
	if hookEvent == "" {
		if output, ok := values["hookSpecificOutput"].(map[string]any); ok {
			hookEvent = strings.ToLower(strings.TrimSpace(stringFieldAny(output, "hook_event_name", "hookEventName", "hook_event", "hookEvent")))
		}
	}
	if hookEvent == "posttooluse" || hookEvent == "post_tool_use" {
		explicit = true
	}
	if !explicit {
		return usageCandidate{}, false
	}
	name, ok := toolNameFromMap(values)
	if !ok {
		return usageCandidate{}, false
	}
	typ := domain.CapabilityTool
	if isMCPToolIdentity(name) {
		typ = domain.CapabilityMCPTool
	}
	return usageCandidate{typ: typ, name: name, eventType: domain.EventInvoked, context: current}, true
}

func toolNameFromMap(values map[string]any) (string, bool) {
	for _, key := range []string{"tool_name", "toolName", "name", "function_name", "functionName"} {
		if value, ok := values[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), true
		}
	}
	if value, ok := values["function"]; ok {
		if function, ok := value.(map[string]any); ok {
			if name, found := function["name"].(string); found && strings.TrimSpace(name) != "" {
				return strings.TrimSpace(name), true
			}
		}
	}
	if value, ok := values["tool"]; ok {
		if name, ok := value.(string); ok && strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name), true
		}
		if tool, ok := value.(map[string]any); ok {
			if name, found := tool["name"].(string); found && strings.TrimSpace(name) != "" {
				return strings.TrimSpace(name), true
			}
		}
	}
	if namespace, ok := values["namespace"].(string); ok {
		if name, found := values["name"].(string); found && strings.HasPrefix(namespace, "mcp__") && strings.TrimSpace(name) != "" {
			return strings.TrimSuffix(namespace, "__") + "__" + strings.TrimSpace(name), true
		}
	}
	if params, ok := values["params"].(map[string]any); ok {
		if name, found := toolNameFromMap(params); found {
			return name, true
		}
	}
	if recipient, ok := values["recipient"].(string); ok {
		recipient = strings.TrimSpace(recipient)
		recipient = strings.TrimPrefix(recipient, "functions.")
		if recipient != "" {
			return recipient, true
		}
	}
	return "", false
}

func skillCandidate(values map[string]any, current usageContext) (usageCandidate, bool) {
	name := ""
	eventType := domain.EventType("")
	for _, key := range []string{"skill_name", "skillName", "skill", "loaded_skill", "loadedSkill", "invoked_skill", "invokedSkill"} {
		if value, ok := values[key].(string); ok && strings.TrimSpace(value) != "" {
			name = strings.TrimSpace(value)
			break
		}
	}
	marker := strings.ToLower(strings.TrimSpace(stringFieldAny(values, "type", "kind", "event_type", "eventType", "event")))
	if strings.Contains(marker, "skill_loaded") || strings.Contains(marker, "skill_load") || strings.Contains(marker, "skill_loaded") {
		eventType = domain.EventLoaded
	} else if strings.Contains(marker, "skill_invoked") || strings.Contains(marker, "skill_use") || strings.Contains(marker, "skill_used") {
		eventType = domain.EventInvoked
	}
	if eventType == "" {
		for _, key := range []string{"skill_event", "skillEvent", "event", "action"} {
			value := strings.ToLower(strings.TrimSpace(stringFieldAny(values, key)))
			switch value {
			case string(domain.EventLoaded), "load", "loaded_skill":
				eventType = domain.EventLoaded
			case string(domain.EventInvoked), "invoke", "used", "use":
				eventType = domain.EventInvoked
			}
			if eventType != "" {
				break
			}
		}
	}
	if eventType == "" {
		for _, key := range []string{"skill_loaded", "skillLoaded", "skill_invoked", "skillInvoked"} {
			if enabled, ok := values[key].(bool); ok && enabled {
				if strings.Contains(strings.ToLower(key), "loaded") {
					eventType = domain.EventLoaded
				} else {
					eventType = domain.EventInvoked
				}
				break
			}
		}
	}
	if eventType == "" {
		capabilityType := strings.ToLower(strings.TrimSpace(stringFieldAny(values, "capability_type", "capabilityType")))
		if capabilityType == "skill" && (marker == string(domain.EventLoaded) || marker == string(domain.EventInvoked)) {
			if marker == string(domain.EventLoaded) {
				eventType = domain.EventLoaded
			} else {
				eventType = domain.EventInvoked
			}
		}
	}
	if name == "" || eventType == "" {
		return usageCandidate{}, false
	}
	return usageCandidate{typ: domain.CapabilitySkill, name: name, eventType: eventType, context: current}, true
}

func stringFieldAny(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok {
			return value
		}
	}
	return ""
}

func isMCPToolIdentity(name string) bool {
	parts := strings.Split(name, "__")
	return len(parts) >= 3 && parts[0] == "mcp" && strings.TrimSpace(parts[1]) != "" && strings.TrimSpace(strings.Join(parts[2:], "__")) != ""
}
