package hooks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type configDocument struct {
	path       string
	root       *jsonNode
	content    []byte
	mode       os.FileMode
	exists     bool
	statusCode StatusCode
}

type eventAnalysis struct {
	event          string
	exactHandlers  int
	legacyHandlers int
	partial        int
	lookalikes     int
}

type inspection struct {
	document      configDocument
	report        StatusReport
	events        []eventAnalysis
	inlineHooks   bool
	inlineWarning string
	unsafePathErr error
}

type handlerMatch uint8

const (
	handlerUnrelated handlerMatch = iota
	handlerExact
	handlerLegacy
	handlerPartial
	handlerLookalike
)

func (m *manager) Status(ctx context.Context) (StatusReport, error) {
	if err := checkContext(ctx); err != nil {
		return StatusReport{}, err
	}
	result, err := m.inspect(ctx)
	if err != nil {
		return result.report, err
	}
	return result.report, nil
}

func (m *manager) Install(ctx context.Context) (OperationResult, error) {
	return m.mutate(ActionInstall, false, ctx)
}

func (m *manager) Uninstall(ctx context.Context) (OperationResult, error) {
	return m.mutate(ActionUninstall, false, ctx)
}

func (m *manager) DryRun(ctx context.Context, action Action) (OperationResult, error) {
	return m.mutate(action, true, ctx)
}

func (m *manager) inspect(ctx context.Context) (inspection, error) {
	result := inspection{report: StatusReport{Managed: ManagedEntryNotInstalled}}
	if m == nil {
		result.report.Code = StatusUnsupported
		result.report.Warnings = append(result.report.Warnings, "hook manager is nil")
		return result, nil
	}
	result.report.Runtime = m.runtime
	result.report.ConfigPath = m.configPath
	if m.runtime != RuntimeClaude && m.runtime != RuntimeCodex {
		result.report.Code = StatusUnsupported
		result.report.Warnings = append(result.report.Warnings, errUnsupportedRuntime.Error())
		return result, nil
	}
	if err := checkContext(ctx); err != nil {
		return result, err
	}
	result.report.Binary = m.resolveBinary()
	if m.runtime == RuntimeCodex {
		result.report.TrustReview = TrustReview{
			Required:   true,
			Limitation: "Codex user-level non-managed command hooks require review/trust before execution; this manager cannot grant trust.",
		}
	}
	if m.configPath == "" || m.configRoot == "" {
		result.report.Code = StatusUnsupported
		result.report.Warnings = append(result.report.Warnings, "configuration root is empty")
		return result, nil
	}
	if err := checkNoSymlinkComponents(m.configPath); err != nil {
		result.unsafePathErr = err
		result.report.Code = StatusUnsupported
		result.report.Warnings = append(result.report.Warnings, err.Error())
		return result, nil
	}
	if m.runtime == RuntimeCodex {
		inline, warning := inspectCodexInlineHooks(m.configRoot)
		result.inlineHooks = inline
		result.report.InlineHooks = inline
		if warning != "" {
			result.inlineWarning = warning
			result.report.Warnings = append(result.report.Warnings, warning)
		}
		if inline {
			result.report.Warnings = append(result.report.Warnings, "Codex inline hooks also exist in config.toml; hooks.json and inline hooks are merged by Codex.")
		}
	}

	info, err := os.Lstat(m.configPath)
	if errors.Is(err, os.ErrNotExist) {
		result.document = configDocument{path: m.configPath, statusCode: StatusConfigurationNotFound}
		result.report.Code = StatusConfigurationNotFound
		result.report.ConfigExists = false
		result.events = m.emptyEventAnalysis()
		result.report.ManagedEntries = managedEntryReports(result.events)
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("inspect %s: %w", m.configPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		err := fmt.Errorf("configuration path %s is a symlink; refusing to follow it", m.configPath)
		result.unsafePathErr = err
		result.report.Code = StatusUnsupported
		result.report.Warnings = append(result.report.Warnings, err.Error())
		return result, nil
	}
	if !info.Mode().IsRegular() {
		err := fmt.Errorf("configuration path %s is not a regular file", m.configPath)
		result.unsafePathErr = err
		result.report.Code = StatusUnsupported
		result.report.Warnings = append(result.report.Warnings, err.Error())
		return result, nil
	}
	content, err := os.ReadFile(m.configPath)
	if err != nil {
		return result, fmt.Errorf("read %s: %w", m.configPath, err)
	}
	document := configDocument{path: m.configPath, content: content, mode: info.Mode().Perm(), exists: true}
	root, parseErr := parseOrderedJSON(content)
	if parseErr != nil {
		document.statusCode = StatusMalformed
		result.document = document
		result.report.Code = StatusMalformed
		result.report.ConfigExists = true
		result.report.Warnings = append(result.report.Warnings, "configuration JSON is malformed: "+parseErr.Error())
		result.events = m.emptyEventAnalysis()
		result.report.ManagedEntries = managedEntryReports(result.events)
		return result, nil
	}
	if root.kind != jsonObjectKind {
		err := errors.New("configuration JSON root must be an object")
		document.statusCode = StatusMalformed
		result.document = document
		result.report.Code = StatusMalformed
		result.report.ConfigExists = true
		result.report.Warnings = append(result.report.Warnings, err.Error())
		result.events = m.emptyEventAnalysis()
		result.report.ManagedEntries = managedEntryReports(result.events)
		return result, nil
	}
	document.root = root
	document.statusCode = StatusNotInstalled
	result.document = document
	result.report.ConfigExists = true
	events, analyzeErr := m.analyze(root)
	if analyzeErr != nil {
		document.statusCode = StatusMalformed
		result.report.Code = StatusMalformed
		result.report.Warnings = append(result.report.Warnings, "configuration hook structure is malformed: "+analyzeErr.Error())
		result.events = m.emptyEventAnalysis()
		result.report.ManagedEntries = managedEntryReports(result.events)
		return result, nil
	}
	result.events = events
	result.report.ManagedEntries = managedEntryReports(events)
	result.report.Managed = aggregateManagedState(events)
	result.report.Code = aggregateStatus(events)
	return result, nil
}

func (m *manager) emptyEventAnalysis() []eventAnalysis {
	events := expectedEvents(m.runtime)
	result := make([]eventAnalysis, 0, len(events))
	for _, event := range events {
		result = append(result, eventAnalysis{event: event})
	}
	return result
}

func expectedEvents(runtime Runtime) []string {
	switch runtime {
	case RuntimeClaude:
		return []string{"PostToolUse", "PostToolUseFailure", "UserPromptExpansion"}
	case RuntimeCodex:
		return []string{"PostToolUse"}
	default:
		return nil
	}
}

func managedEntryReports(events []eventAnalysis) []ManagedEntry {
	result := make([]ManagedEntry, 0, len(events))
	for _, event := range events {
		state := ManagedEntryNotInstalled
		if event.exactHandlers == 1 && event.legacyHandlers == 0 && event.partial == 0 {
			state = ManagedEntryInstalled
		} else if event.exactHandlers > 0 || event.legacyHandlers > 0 || event.partial > 0 {
			state = ManagedEntryPartial
		}
		result = append(result, ManagedEntry{
			Event:         event.event,
			State:         state,
			ExactHandlers: event.exactHandlers,
			Partial:       event.partial,
			Lookalikes:    event.lookalikes,
		})
	}
	return result
}

func aggregateManagedState(events []eventAnalysis) ManagedEntryState {
	if len(events) == 0 {
		return ManagedEntryNotInstalled
	}
	installed := 0
	partial := 0
	for _, event := range events {
		if event.exactHandlers == 1 && event.legacyHandlers == 0 && event.partial == 0 {
			installed++
		} else if event.exactHandlers > 0 || event.legacyHandlers > 0 || event.partial > 0 {
			partial++
		}
	}
	if installed == len(events) {
		return ManagedEntryInstalled
	}
	if installed > 0 || partial > 0 {
		return ManagedEntryPartial
	}
	return ManagedEntryNotInstalled
}

func aggregateStatus(events []eventAnalysis) StatusCode {
	if len(events) == 0 {
		return StatusNotInstalled
	}
	installed := 0
	partial := 0
	for _, event := range events {
		if event.exactHandlers == 1 && event.legacyHandlers == 0 && event.partial == 0 {
			installed++
		} else if event.exactHandlers > 0 || event.legacyHandlers > 0 || event.partial > 0 {
			partial++
		}
	}
	if installed == len(events) {
		return StatusInstalled
	}
	if installed > 0 || partial > 0 {
		return StatusPartiallyInstalled
	}
	return StatusNotInstalled
}

func (m *manager) analyze(root *jsonNode) ([]eventAnalysis, error) {
	hooks, found, err := root.field("hooks")
	if err != nil {
		return nil, err
	}
	if !found {
		return m.emptyEventAnalysis(), nil
	}
	if hooks.kind != jsonObjectKind {
		return nil, errors.New("hooks must be a JSON object")
	}
	result := make([]eventAnalysis, 0, len(expectedEvents(m.runtime)))
	for _, event := range expectedEvents(m.runtime) {
		analysis := eventAnalysis{event: event}
		eventValue, exists, err := hooks.field(event)
		if err != nil {
			return nil, err
		}
		if !exists {
			result = append(result, analysis)
			continue
		}
		if eventValue.kind != jsonArrayKind {
			return nil, fmt.Errorf("hooks.%s must be an array", event)
		}
		for groupIndex, group := range eventValue.elements {
			if group.kind != jsonObjectKind {
				return nil, fmt.Errorf("hooks.%s[%d] must be an object", event, groupIndex)
			}
			handlers, found, err := group.field("hooks")
			if err != nil {
				return nil, fmt.Errorf("hooks.%s[%d]: %w", event, groupIndex, err)
			}
			if !found || handlers.kind != jsonArrayKind {
				return nil, fmt.Errorf("hooks.%s[%d].hooks must be an array", event, groupIndex)
			}
			for handlerIndex, handler := range handlers.elements {
				if handler.kind != jsonObjectKind {
					return nil, fmt.Errorf("hooks.%s[%d].hooks[%d] must be an object", event, groupIndex, handlerIndex)
				}
				switch m.matchHandler(event, group, handler) {
				case handlerExact:
					analysis.exactHandlers++
				case handlerLegacy:
					analysis.exactHandlers++
					analysis.legacyHandlers++
				case handlerPartial:
					analysis.partial++
				case handlerLookalike:
					analysis.lookalikes++
				}
			}
		}
		result = append(result, analysis)
	}
	return result, nil
}

func (m *manager) matchHandler(event string, group, handler *jsonNode) handlerMatch {
	if !managedGroupShape(group) {
		return handlerUnrelated
	}
	if isExactHandler(m.runtime, event, handler) {
		return handlerExact
	}
	if isLegacyCodexHandler(m.runtime, event, handler) {
		return handlerLegacy
	}
	if isPartialHandler(m.runtime, event, handler) {
		return handlerPartial
	}
	if isLookalikeHandler(m.runtime, event, handler) {
		return handlerLookalike
	}
	return handlerUnrelated
}

func (m *manager) resolveBinary() BinaryResolution {
	result := BinaryResolution{Name: BinaryName}
	if m == nil || m.lookPath == nil {
		result.Error = errBinaryUnresolved.Error()
		return result
	}
	path, err := m.lookPath(BinaryName)
	if err != nil || strings.TrimSpace(path) == "" {
		if err != nil {
			result.Error = err.Error()
		} else {
			result.Error = errBinaryUnresolved.Error()
		}
		return result
	}
	result.Resolved = true
	result.ResolvedPath = filepath.Clean(path)
	return result
}

func (m *manager) mutate(action Action, dryRun bool, ctx context.Context) (OperationResult, error) {
	result := OperationResult{Runtime: m.runtime, Action: action}
	if action != ActionInstall && action != ActionUninstall {
		result.Status = StatusReport{Runtime: m.runtime, Code: StatusUnsupported, ConfigPath: m.configPath}
		result.Summary = "unsupported hook operation"
		return result, fmt.Errorf("unsupported hook action %q", action)
	}
	if err := checkContext(ctx); err != nil {
		return result, err
	}
	inspected, err := m.inspect(ctx)
	if err != nil {
		return result, err
	}
	result.Status = inspected.report
	if inspected.unsafePathErr != nil {
		result.Summary = "unsafe configuration path; no changes made"
		return result, inspected.unsafePathErr
	}
	if inspected.report.Code == StatusMalformed {
		result.Summary = "configuration is malformed; no changes made"
		detail := "configuration JSON is malformed"
		if len(inspected.report.Warnings) > 0 && strings.TrimSpace(inspected.report.Warnings[0]) != "" {
			detail = inspected.report.Warnings[0]
		}
		return result, fmt.Errorf("%w: %s", ErrMalformedConfiguration, detail)
	}
	if action == ActionInstall && !inspected.report.Binary.Resolved {
		result.Summary = "harness-lint is not resolvable on PATH; no changes made"
		return result, errBinaryUnresolved
	}
	if !inspected.document.exists {
		if action == ActionUninstall {
			result.Summary = "configuration not found; nothing to uninstall"
			return result, nil
		}
		result.WouldChange = true
		result.Plan = m.installPlan()
		result.Summary = "would create configuration and install managed hooks"
		if dryRun {
			return result, nil
		}
		root := newJSONObject()
		m.addManagedHooks(root)
		data := root.bytes()
		if err := m.writeNew(data); err != nil {
			return result, err
		}
		result.Changed = true
		result.WouldChange = false
		result.Summary = "created configuration and installed managed hooks"
		result.Status = m.reportAfterMutation(root, inspected.report)
		return result, nil
	}

	if inspected.document.root == nil {
		return result, errors.New("configuration document has no parsed root")
	}
	if action == ActionInstall && allEventsInstalled(inspected.events) {
		result.Summary = "managed hooks already installed; no changes made"
		return result, nil
	}
	if action == ActionUninstall && totalExactHandlers(inspected.events) == 0 {
		result.Summary = "managed hooks not installed; no changes made"
		return result, nil
	}

	updated := cloneJSON(inspected.document.root)
	var changed bool
	if action == ActionInstall {
		changed, err = m.addManagedHooks(updated)
		result.Plan = m.installPlan()
		result.Summary = "would install managed hooks"
	} else {
		changed, err = m.removeManagedHooks(updated)
		result.Plan = m.uninstallPlan()
		result.Summary = "would uninstall managed hooks"
	}
	if err != nil {
		return result, err
	}
	if !changed {
		result.Summary = "no changes needed"
		return result, nil
	}
	result.WouldChange = true
	if dryRun {
		return result, nil
	}
	backup, err := m.writeExisting(updated.bytes(), inspected.document.mode)
	if err != nil {
		return result, err
	}
	result.BackupPath = backup
	result.Changed = true
	result.WouldChange = false
	if action == ActionInstall {
		result.Summary = "installed managed hooks"
	} else {
		result.Summary = "uninstalled managed hooks"
	}
	result.Status = m.reportAfterMutation(updated, inspected.report)
	return result, nil
}

func allEventsInstalled(events []eventAnalysis) bool {
	if len(events) == 0 {
		return false
	}
	for _, event := range events {
		if event.exactHandlers != 1 || event.legacyHandlers != 0 || event.partial != 0 {
			return false
		}
	}
	return true
}

func totalExactHandlers(events []eventAnalysis) int {
	result := 0
	for _, event := range events {
		result += event.exactHandlers
	}
	return result
}

func (m *manager) installPlan() []Change {
	result := make([]Change, 0, len(expectedEvents(m.runtime))+1)
	delivery := "synchronous"
	if m.runtime == RuntimeClaude {
		delivery = "asynchronous"
	}
	for _, event := range expectedEvents(m.runtime) {
		result = append(result, Change{Kind: "add-handler", Path: m.configPath, Detail: "merge managed " + delivery + " command hook for " + event})
	}
	if m.runtime == RuntimeCodex {
		result = append(result, Change{Kind: "warning", Path: filepath.Join(m.configRoot, "config.toml"), Detail: "inline hooks, if present, are merged by Codex and require separate trust review"})
	}
	return result
}

func (m *manager) uninstallPlan() []Change {
	result := make([]Change, 0, len(expectedEvents(m.runtime)))
	for _, event := range expectedEvents(m.runtime) {
		result = append(result, Change{Kind: "remove-handler", Path: m.configPath, Detail: "remove exact managed hook for " + event})
	}
	return result
}

func (m *manager) reportAfterMutation(root *jsonNode, previous StatusReport) StatusReport {
	report := previous
	events, err := m.analyze(root)
	if err != nil {
		report.Code = StatusMalformed
		report.Managed = ManagedEntryNotInstalled
		report.ManagedEntries = nil
		report.Warnings = append(report.Warnings, "generated configuration structure is malformed: "+err.Error())
		return report
	}
	report.ConfigExists = true
	report.Code = aggregateStatus(events)
	report.Managed = aggregateManagedState(events)
	report.ManagedEntries = managedEntryReports(events)
	return report
}
