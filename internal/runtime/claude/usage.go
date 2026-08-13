package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
)

// ImportUsage imports only observed tool identities from Claude transcript
// JSONL and injected PostToolUse-shaped hook JSON. It intentionally ignores
// prompts, responses, source snippets, tool outputs, and model output.
func (a *Adapter) ImportUsage(ctx context.Context, since time.Time) ([]domain.UsageEvent, error) {
	if a == nil {
		return nil, errors.New("claude adapter is nil")
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	cutoff := since.UTC()
	observationTime := a.observationTime()
	if observationTime.IsZero() {
		observationTime = time.Now().UTC()
	}

	transcriptFiles, err := collectDataFiles(ctx, a.transcriptRoots, false)
	if err != nil {
		return nil, err
	}
	hookFiles, err := collectDataFiles(ctx, append(append([]string(nil), a.hookPaths...), a.hookRoots...), true)
	if err != nil {
		return nil, err
	}

	byFingerprint := make(map[string]domain.UsageEvent)
	for _, path := range transcriptFiles {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		parseTranscriptFile(ctx, path, cutoff, observationTime, a.projectFallback(), byFingerprint)
	}
	for _, path := range hookFiles {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		parseHookFile(ctx, path, cutoff, observationTime, byFingerprint)
	}

	result := make([]domain.UsageEvent, 0, len(byFingerprint))
	for _, event := range byFingerprint {
		result = append(result, event)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if !result[i].Timestamp.Equal(result[j].Timestamp) {
			return result[i].Timestamp.Before(result[j].Timestamp)
		}
		if result[i].Fingerprint != result[j].Fingerprint {
			return result[i].Fingerprint < result[j].Fingerprint
		}
		if result[i].CapabilityType != result[j].CapabilityType {
			return result[i].CapabilityType < result[j].CapabilityType
		}
		return result[i].CapabilityName < result[j].CapabilityName
	})
	return result, nil
}

func (a *Adapter) projectFallback() string {
	if a == nil {
		return ""
	}
	return firstNonEmpty(a.currentDirectory, a.projectRoot)
}

func collectDataFiles(ctx context.Context, roots []string, includeJSON bool) ([]string, error) {
	var result []string
	seen := make(map[string]struct{})
	var walk func(string) error
	walk = func(path string) error {
		if err := contextError(ctx); err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			// Broken links and unreadable optional roots are not usage events.
			return nil
		}
		if info.IsDir() {
			entries, err := os.ReadDir(path)
			if err != nil {
				return nil
			}
			sort.SliceStable(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
			for _, entry := range entries {
				if err := walk(filepath.Join(path, entry.Name())); err != nil {
					return err
				}
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if includeJSON {
			ext := strings.ToLower(filepath.Ext(path))
			if ext != ".json" && ext != ".jsonl" {
				return nil
			}
		} else if strings.ToLower(filepath.Ext(path)) != ".jsonl" {
			return nil
		}
		if _, ok := seen[path]; ok {
			return nil
		}
		seen[path] = struct{}{}
		result = append(result, path)
		return nil
	}
	for _, root := range cleanUniquePaths(roots) {
		if err := walk(root); err != nil {
			return nil, err
		}
	}
	sort.Strings(result)
	return result, nil
}

type transcriptLine struct {
	Type       string          `json:"type"`
	Timestamp  string          `json:"timestamp"`
	SessionID  string          `json:"session_id"`
	SessionID2 string          `json:"sessionId"`
	CWD        string          `json:"cwd"`
	Project    string          `json:"project"`
	Name       string          `json:"name"`
	Input      json.RawMessage `json:"input"`
	ToolName   string          `json:"tool_name"`
	ToolInput  json.RawMessage `json:"tool_input"`
	Content    json.RawMessage `json:"content"`
	Message    transcriptMsg   `json:"message"`
}

type transcriptMsg struct {
	Timestamp  string          `json:"timestamp"`
	SessionID  string          `json:"session_id"`
	SessionID2 string          `json:"sessionId"`
	CWD        string          `json:"cwd"`
	Content    json.RawMessage `json:"content"`
}

func parseTranscriptFile(ctx context.Context, path string, since, fallback time.Time, projectFallback string, result map[string]domain.UsageEvent) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	// Tool results can be large; increase the line limit while retaining one
	// line at a time and discarding it immediately after metadata extraction.
	scanner.Buffer(make([]byte, 32*1024), 32*1024*1024)
	for scanner.Scan() {
		if err := contextError(ctx); err != nil {
			return
		}
		var line transcriptLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		timestamp := parseEventTime(line.Timestamp)
		if timestamp.IsZero() {
			timestamp = parseEventTime(line.Message.Timestamp)
		}
		if timestamp.IsZero() {
			timestamp = fallback
		}
		if timestamp.Before(since) {
			continue
		}
		session := firstNonEmpty(line.SessionID, line.SessionID2, line.Message.SessionID, line.Message.SessionID2)
		if session == "" {
			session = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		}
		project := firstNonEmpty(line.CWD, line.Project, line.Message.CWD)
		if project == "" {
			project = projectFallback
		}
		if project == "" {
			continue
		}
		visit := func(name string, input json.RawMessage) {
			capabilityType, capabilityName, ok := classifyTool(name, input)
			if !ok {
				return
			}
			addUsageEvent(result, timestamp, session, project, capabilityType, capabilityName)
		}
		if strings.EqualFold(line.Type, "tool_use") && line.Name != "" {
			visit(line.Name, line.Input)
		}
		if line.ToolName != "" {
			visit(line.ToolName, line.ToolInput)
		}
		scanToolUses(line.Message.Content, visit)
		scanToolUses(line.Content, visit)
	}
}

type hookLine struct {
	HookEventName string          `json:"hook_event_name"`
	Timestamp     string          `json:"timestamp"`
	EventTime     string          `json:"event_timestamp"`
	SessionID     string          `json:"session_id"`
	CWD           string          `json:"cwd"`
	Transcript    string          `json:"transcript_path"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
	ToolCalls     []hookToolCall  `json:"tool_calls"`
}

type hookToolCall struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

func parseHookFile(ctx context.Context, path string, since, fallback time.Time, result map[string]domain.UsageEvent) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()
	if strings.EqualFold(filepath.Ext(path), ".json") {
		var line hookLine
		if err := json.NewDecoder(file).Decode(&line); err == nil {
			processHookLine(ctx, line, path, since, fallback, result)
		}
		return
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 16*1024), 16*1024*1024)
	for scanner.Scan() {
		if err := contextError(ctx); err != nil {
			return
		}
		var line hookLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		processHookLine(ctx, line, path, since, fallback, result)
	}
}

func processHookLine(ctx context.Context, line hookLine, path string, since, fallback time.Time, result map[string]domain.UsageEvent) {
	if err := contextError(ctx); err != nil {
		return
	}
	if line.HookEventName != "" && line.HookEventName != "PostToolUse" && line.HookEventName != "PostToolUseFailure" && line.HookEventName != "PostToolBatch" {
		return
	}
	timestamp := parseEventTime(firstNonEmpty(line.Timestamp, line.EventTime))
	if timestamp.IsZero() {
		timestamp = fallback
	}
	if timestamp.Before(since) {
		return
	}
	session := line.SessionID
	if session == "" {
		session = strings.TrimSuffix(filepath.Base(line.Transcript), filepath.Ext(line.Transcript))
	}
	if session == "" {
		session = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	project := line.CWD
	if project == "" {
		return
	}
	if line.ToolName != "" {
		addClassifiedUsage(result, timestamp, session, project, line.ToolName, line.ToolInput)
	}
	for _, call := range line.ToolCalls {
		if call.ToolName == "" {
			continue
		}
		addClassifiedUsage(result, timestamp, session, project, call.ToolName, call.ToolInput)
	}
}

func scanToolUses(raw json.RawMessage, visit func(string, json.RawMessage)) {
	if len(raw) == 0 || string(raw) == "null" {
		return
	}
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) == nil {
		for _, block := range blocks {
			scanToolUses(block, visit)
		}
		return
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return
	}
	var typ, name string
	_ = json.Unmarshal(object["type"], &typ)
	_ = json.Unmarshal(object["name"], &name)
	if typ == "tool_use" && name != "" {
		visit(name, object["input"])
		return
	}
	for _, key := range []string{"content", "blocks", "parts"} {
		if child, ok := object[key]; ok {
			scanToolUses(child, visit)
		}
	}
}

func classifyTool(name string, input json.RawMessage) (domain.CapabilityType, string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return domain.CapabilityUnknown, "", false
	}
	if strings.HasPrefix(name, "mcp__") {
		rest := strings.TrimPrefix(name, "mcp__")
		parts := strings.SplitN(rest, "__", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != "" {
			return domain.CapabilityMCPTool, name, true
		}
		return domain.CapabilityUnknown, "", false
	}
	switch name {
	case "Skill":
		identity := inputIdentity(input, "skill", "name")
		if identity == "" {
			return domain.CapabilityUnknown, "", false
		}
		return domain.CapabilitySkill, identity, true
	case "Agent", "Task":
		identity := inputIdentity(input, "subagent_type", "agent_type", "agent", "name")
		if identity == "" {
			return domain.CapabilityUnknown, "", false
		}
		return domain.CapabilityAgent, identity, true
	default:
		return domain.CapabilityTool, name, true
	}
}

func inputIdentity(raw json.RawMessage, keys ...string) string {
	object, ok := rawObject(raw)
	if !ok {
		return ""
	}
	for _, key := range keys {
		if value, ok := rawString(object[key]); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func addClassifiedUsage(result map[string]domain.UsageEvent, timestamp time.Time, session, project, toolName string, input json.RawMessage) {
	capabilityType, capabilityName, ok := classifyTool(toolName, input)
	if !ok {
		return
	}
	addUsageEvent(result, timestamp, session, project, capabilityType, capabilityName)
}

func addUsageEvent(result map[string]domain.UsageEvent, timestamp time.Time, session, project string, capabilityType domain.CapabilityType, capabilityName string) {
	event := domain.UsageEvent{
		Timestamp:      timestamp.UTC(),
		Runtime:        domain.RuntimeClaudeCode,
		SessionID:      hashIdentifier(session),
		ProjectID:      hashIdentifier(project),
		CapabilityType: capabilityType,
		CapabilityName: capabilityName,
		EventType:      domain.EventInvoked,
	}
	fingerprint, err := domain.FingerprintForUsageEvent(event)
	if err != nil {
		return
	}
	event.Fingerprint = fingerprint
	result[fingerprint] = event
}

func parseEventTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC()
	}
	return time.Time{}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
