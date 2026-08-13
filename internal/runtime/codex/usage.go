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

// usageContext contains only opaque identifiers used to build hashed usage
// records. Timestamps are always read from the record that produced an event;
// they are intentionally not inherited from neighboring records or files.
type usageContext struct {
	session string
	project string
}

type usageCandidate struct {
	typ       domain.CapabilityType
	name      string
	eventType domain.EventType
	timestamp time.Time
	context   usageContext
}

func (a *Adapter) importTranscriptUsage(ctx context.Context, since time.Time) ([]domain.UsageEvent, error) {
	roots := append([]string(nil), a.options.transcripts...)
	roots = append(roots, a.options.hookEventPaths...)
	files, err := transcriptFiles(roots)
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
		fallbackProject := a.options.projectRoot
		if fallbackProject == "" {
			fallbackProject = filepath.Dir(path)
		}
		fileContext := usageContext{session: fallbackSession, project: fallbackProject}
		visit := func(value any) error {
			for _, record := range topLevelRecords(value) {
				if err := contextErr(ctx); err != nil {
					return err
				}
				fileContext = explicitUsageContext(record, fileContext)
				candidate, ok := usageCandidateFromRecord(record, fileContext)
				if !ok || (!since.IsZero() && candidate.timestamp.Before(since.UTC())) {
					continue
				}
				event := domain.UsageEvent{
					Timestamp:      candidate.timestamp.UTC(),
					Runtime:        domain.RuntimeCodex,
					SessionID:      hashIdentifier(candidate.context.session),
					ProjectID:      hashIdentifier(candidate.context.project),
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
		// Codex rollout names use rollout-<timestamp>-<thread-id>. Keep the
		// opaque thread identifier when the conventional prefix is present.
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

// topLevelRecords expands only a JSON document's outer array. It never walks
// arbitrary nested message, prompt, response, or tool payload objects.
func topLevelRecords(value any) []any {
	if records, ok := value.([]any); ok {
		return records
	}
	return []any{value}
}

func explicitUsageContext(value any, inherited usageContext) usageContext {
	record, ok := value.(map[string]any)
	if !ok {
		return inherited
	}
	marker := strings.ToLower(strings.TrimSpace(stringFieldAny(record, "type")))
	knownRecord := marker == "session_meta" || marker == "response_item" || marker == "function_call" || marker == "custom_tool_call" || marker == "dynamic_tool_call"
	hookEvent := strings.ToLower(strings.TrimSpace(stringFieldAny(record, "hook_event_name", "hookEventName")))
	knownRecord = knownRecord || hookEvent == "posttooluse" || hookEvent == "post_tool_use"
	if !knownRecord {
		return inherited
	}
	result := inherited
	if session, found := identifierField(record, sessionKeys...); found {
		result.session = session
	}
	if project, found := identifierField(record, projectKeys...); found {
		result.project = project
	}
	if marker != "session_meta" {
		return result
	}
	payload, ok := record["payload"].(map[string]any)
	if !ok {
		return result
	}
	if session, found := identifierField(payload, "id", "session_id", "sessionId", "thread_id", "threadId"); found {
		result.session = session
	}
	if project, found := identifierField(payload, projectKeys...); found {
		result.project = project
	}
	return result
}

func usageCandidateFromRecord(value any, inherited usageContext) (usageCandidate, bool) {
	record, ok := value.(map[string]any)
	if !ok || ambiguousUsageRecord(record) {
		return usageCandidate{}, false
	}
	timestamp, ok := timestampFromMap(record)
	if !ok {
		return usageCandidate{}, false
	}
	marker := strings.ToLower(strings.TrimSpace(stringFieldAny(record, "type")))
	if marker == "response_item" {
		payload, payloadOK := record["payload"].(map[string]any)
		if !payloadOK {
			return usageCandidate{}, false
		}
		payloadType := strings.ToLower(strings.TrimSpace(stringFieldAny(payload, "type")))
		if payloadType != "function_call" && payloadType != "custom_tool_call" && payloadType != "dynamic_tool_call" {
			return usageCandidate{}, false
		}
		name, nameOK := responseItemToolName(payload)
		if !nameOK {
			return usageCandidate{}, false
		}
		return newToolCandidate(name, timestamp, inherited), true
	}
	if marker == "function_call" || marker == "custom_tool_call" || marker == "dynamic_tool_call" {
		name, nameOK := directToolName(record)
		if !nameOK {
			return usageCandidate{}, false
		}
		return newToolCandidate(name, timestamp, inherited), true
	}
	hookEvent := strings.ToLower(strings.TrimSpace(stringFieldAny(record, "hook_event_name", "hookEventName")))
	if hookEvent != "posttooluse" && hookEvent != "post_tool_use" {
		return usageCandidate{}, false
	}
	name, nameOK := postToolName(record)
	if !nameOK {
		return usageCandidate{}, false
	}
	return newToolCandidate(name, timestamp, inherited), true
}

func ambiguousUsageRecord(record map[string]any) bool {
	for key := range record {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "prompt", "prompts", "response", "responses", "message", "messages", "content", "contents":
			return true
		}
	}
	return false
}

func responseItemToolName(payload map[string]any) (string, bool) {
	return directToolName(payload)
}

func directToolName(record map[string]any) (string, bool) {
	for _, key := range []string{"name", "tool_name", "toolName"} {
		if value, ok := record[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), true
		}
	}
	return "", false
}

func postToolName(record map[string]any) (string, bool) {
	for _, key := range []string{"tool_name", "toolName", "name"} {
		if value, ok := record[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), true
		}
	}
	return "", false
}

func newToolCandidate(name string, timestamp time.Time, context usageContext) usageCandidate {
	typ := domain.CapabilityTool
	if isMCPToolIdentity(name) {
		typ = domain.CapabilityMCPTool
	}
	return usageCandidate{
		typ:       typ,
		name:      name,
		eventType: domain.EventInvoked,
		timestamp: timestamp,
		context:   context,
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

func stringFieldAny(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok {
			return value
		}
	}
	return ""
}

func isMCPToolIdentity(name string) bool {
	if !strings.HasPrefix(name, "mcp__") {
		return false
	}
	parts := strings.SplitN(strings.TrimPrefix(name, "mcp__"), "__", 2)
	return len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != ""
}
