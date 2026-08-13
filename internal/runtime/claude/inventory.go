package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kespineira/harness-lint/internal/domain"
)

const (
	metadataEstimateBasis = "configured maximum estimate from advertised skill name and description at approximately 4 UTF-8 bytes/token; Claude may collapse descriptions to name-only under its skills context/character budget; not runtime cost"
	hiddenMetadataBasis   = "observed configured skill metadata is not advertised; not runtime cost"
)

type fileEntry struct {
	path  string
	root  string
	scope domain.Scope
}

type fileKind string

const (
	fileKindSkill       fileKind = "skill"
	fileKindCommand     fileKind = "command"
	fileKindAgent       fileKind = "agent"
	fileKindInstruction fileKind = "instruction"
)

type skillExposure struct {
	enabled       domain.EnabledState
	advertisement domain.AdvertisementState
	metadata      domain.Measurement
}

func metadataMeasurement(name, description string, advertisement domain.AdvertisementState) domain.Measurement {
	switch advertisement {
	case domain.AdvertisementStateFullyAdvertised:
		return domain.Measurement{
			Value:      estimateTokens([]byte(strings.TrimSpace(name) + "\n" + strings.TrimSpace(description))),
			Confidence: domain.ConfidenceEstimated,
			Basis:      metadataEstimateBasis,
		}
	case domain.AdvertisementStateNameOnly:
		return domain.Measurement{
			Value:      estimateTokens([]byte(strings.TrimSpace(name))),
			Confidence: domain.ConfidenceEstimated,
			Basis:      metadataEstimateBasis,
		}
	case domain.AdvertisementStateNotAdvertised:
		return domain.Measurement{Confidence: domain.ConfidenceObserved, Basis: hiddenMetadataBasis}
	default:
		return unknownMeasurement()
	}
}

func (s *discoveryState) effectiveSkillOverride(names ...string) (string, bool) {
	value, found := "", false
	for _, settings := range s.settings {
		overrides, ok := rawObject(settings.raw["skillOverrides"])
		if !ok {
			continue
		}
		for _, name := range names {
			candidate, ok := rawString(overrides[name])
			if !ok {
				continue
			}
			value, found = strings.ToLower(strings.TrimSpace(candidate)), true
			break
		}
	}
	return value, found
}

func (s *discoveryState) skillExposure(name string, overrideNames []string, front frontMatter) skillExposure {
	description := front.selectionDescription()
	names := append([]string{name}, overrideNames...)
	if override, ok := s.effectiveSkillOverride(names...); ok {
		switch override {
		case "on":
			return skillExposure{
				enabled:       domain.EnabledStateEnabled,
				advertisement: domain.AdvertisementStateFullyAdvertised,
				metadata:      metadataMeasurement(name, description, domain.AdvertisementStateFullyAdvertised),
			}
		case "name-only":
			return skillExposure{
				enabled:       domain.EnabledStateEnabled,
				advertisement: domain.AdvertisementStateNameOnly,
				metadata:      metadataMeasurement(name, description, domain.AdvertisementStateNameOnly),
			}
		case "user-invocable-only":
			return skillExposure{
				enabled:       domain.EnabledStateEnabled,
				advertisement: domain.AdvertisementStateNotAdvertised,
				metadata:      metadataMeasurement(name, description, domain.AdvertisementStateNotAdvertised),
			}
		case "off":
			return skillExposure{
				enabled:       domain.EnabledStateDisabled,
				advertisement: domain.AdvertisementStateNotAdvertised,
				metadata:      metadataMeasurement(name, description, domain.AdvertisementStateNotAdvertised),
			}
		default:
			return skillExposure{
				enabled:       domain.EnabledStateUnknown,
				advertisement: domain.AdvertisementStateUnknown,
				metadata:      unknownMeasurement(),
			}
		}
	}
	if disabled, known := front.boolValue("disable-model-invocation"); known && disabled {
		return skillExposure{
			enabled:       domain.EnabledStateEnabled,
			advertisement: domain.AdvertisementStateNotAdvertised,
			metadata:      metadataMeasurement(name, description, domain.AdvertisementStateNotAdvertised),
		}
	}
	return skillExposure{
		enabled:       domain.EnabledStateEnabled,
		advertisement: domain.AdvertisementStateFullyAdvertised,
		metadata:      metadataMeasurement(name, description, domain.AdvertisementStateFullyAdvertised),
	}
}

func commandExposure(s *discoveryState, name string, overrideNames []string, front frontMatter) skillExposure {
	// Legacy commands share the documented skill frontmatter and slash-command
	// listing. Applying the effective override by name keeps the two aliases
	// honest when they intentionally point at the same configured capability.
	return s.skillExposure(name, overrideNames, front)
}

func agentExposure() skillExposure {
	return skillExposure{
		enabled:       domain.EnabledStateUnknown,
		advertisement: domain.AdvertisementStateUnknown,
		metadata:      unknownMeasurement(),
	}
}

func (s *discoveryState) loadSettings(ctx context.Context) {
	paths := make([]struct {
		path  string
		scope domain.Scope
	}, 0, 4)
	if s.adapter.userClaudeDir != "" {
		paths = append(paths, struct {
			path  string
			scope domain.Scope
		}{filepath.Join(s.adapter.userClaudeDir, "settings.json"), domain.ScopeUser})
	}
	if s.adapter.projectRoot != "" {
		projectConfig := filepath.Join(s.adapter.projectRoot, ".claude")
		paths = append(paths,
			struct {
				path  string
				scope domain.Scope
			}{filepath.Join(projectConfig, "settings.json"), domain.ScopeProject},
			struct {
				path  string
				scope domain.Scope
			}{filepath.Join(projectConfig, "settings.local.json"), domain.ScopeProject},
		)
	}
	seen := make(map[string]struct{}, len(paths))
	for _, candidate := range paths {
		if err := contextError(ctx); err != nil {
			return
		}
		if _, ok := seen[candidate.path]; ok {
			continue
		}
		seen[candidate.path] = struct{}{}
		data, exists, err := readOptional(candidate.path)
		if err != nil {
			if isBrokenSymlink(candidate.path) {
				s.addFinding(finding("broken-symlink", "a configured settings path is a broken symlink", domain.SeverityWarning, domain.CapabilityUnknown, ""))
			} else {
				s.addFinding(finding("config-read-error", "a Claude settings file could not be read", domain.SeverityWarning, domain.CapabilityUnknown, ""))
			}
			continue
		}
		if !exists {
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil || raw == nil {
			s.addFinding(finding("malformed-config", "a Claude settings file is not valid JSON", domain.SeverityWarning, domain.CapabilityUnknown, ""))
			continue
		}
		s.settings = append(s.settings, settingsFile{raw: raw})
		s.validateSkillOverrides(raw)
		s.discoverHooks(candidate.path, candidate.scope, raw)
	}
}

func (s *discoveryState) validateSkillOverrides(settings map[string]json.RawMessage) {
	raw, present := settings["skillOverrides"]
	if !present {
		return
	}
	overrides, ok := rawObject(raw)
	if !ok {
		s.addFinding(finding("malformed-config", "the Claude skillOverrides setting is not an object", domain.SeverityWarning, domain.CapabilityUnknown, ""))
		return
	}
	for _, value := range overrides {
		candidate, ok := rawString(value)
		if !ok {
			s.addFinding(finding("malformed-config", "a Claude skillOverrides value is not a string", domain.SeverityWarning, domain.CapabilityUnknown, ""))
			continue
		}
		switch strings.ToLower(strings.TrimSpace(candidate)) {
		case "on", "name-only", "user-invocable-only", "off":
		default:
			s.addFinding(finding("malformed-config", "a Claude skillOverrides value is not a documented state", domain.SeverityWarning, domain.CapabilityUnknown, ""))
		}
	}
}

func (s *discoveryState) discoverHooks(path string, scope domain.Scope, settings map[string]json.RawMessage) {
	hooks, ok := rawObject(settings["hooks"])
	if !ok {
		if settings["hooks"] != nil {
			s.addFinding(finding("malformed-config", "the Claude hooks setting is not an object", domain.SeverityWarning, domain.CapabilityHook, ""))
		}
		return
	}
	events := make([]string, 0, len(hooks))
	for event := range hooks {
		events = append(events, event)
	}
	sort.Strings(events)
	disableAll := rawBoolValue(settings["disableAllHooks"])
	for _, event := range events {
		var groups []json.RawMessage
		if err := json.Unmarshal(hooks[event], &groups); err != nil {
			s.addFinding(finding("malformed-config", "a Claude hook event must contain an array", domain.SeverityWarning, domain.CapabilityHook, event))
			continue
		}
		for groupIndex, groupRaw := range groups {
			group, ok := rawObject(groupRaw)
			if !ok {
				s.addFinding(finding("malformed-config", "a Claude hook matcher entry is not an object", domain.SeverityWarning, domain.CapabilityHook, event))
				continue
			}
			matcher, _ := rawString(group["matcher"])
			var definitions []json.RawMessage
			if err := json.Unmarshal(group["hooks"], &definitions); err != nil {
				s.addFinding(finding("malformed-config", "a Claude hook matcher entry is missing its hooks array", domain.SeverityWarning, domain.CapabilityHook, event))
				continue
			}
			for definitionIndex, definitionRaw := range definitions {
				definition, ok := rawObject(definitionRaw)
				if !ok {
					s.addFinding(finding("malformed-config", "a Claude hook definition is not an object", domain.SeverityWarning, domain.CapabilityHook, event))
					continue
				}
				name := event
				if strings.TrimSpace(matcher) != "" {
					name += ":" + strings.TrimSpace(matcher)
				}
				if len(definitions) > 1 {
					name += fmt.Sprintf("#%d", definitionIndex+1)
				}
				if len(groups) > 1 {
					name += fmt.Sprintf("@%d", groupIndex+1)
				}
				enabled := domain.EnabledStateUnknown
				if disableAll {
					enabled = domain.EnabledStateDisabled
				} else if value, exists := rawBool(definition["enabled"]); exists {
					if value {
						enabled = domain.EnabledStateEnabled
					} else {
						enabled = domain.EnabledStateDisabled
					}
				}
				first, last := s.observation()
				s.addCapability(domain.Capability{
					Runtime:        domain.RuntimeClaudeCode,
					Type:           domain.CapabilityHook,
					Name:           name,
					Scope:          scope,
					Source:         path,
					Enabled:        enabled,
					Advertisement:  domain.AdvertisementStateUnknown,
					Hash:           hashJSON(json.RawMessage(definitionRaw)),
					MetadataTokens: unknownMeasurement(),
					BodyTokens:     unknownMeasurement(),
					FirstSeen:      first,
					LastSeen:       last,
				})
			}
		}
	}
}

func (s *discoveryState) discoverSkills(ctx context.Context) {
	entries := s.collectEntries(ctx, fileKindSkill, inventoryRoots(s.adapter, "skills"))
	for _, entry := range entries {
		if err := contextError(ctx); err != nil {
			return
		}
		data, err := os.ReadFile(entry.path)
		if err != nil {
			s.addFileReadFinding(entry.path, fileKindSkill)
			continue
		}
		front := parseFrontMatter(data)
		name := front.value("name")
		if name == "" {
			name = relativeCapabilityName(entry.root, entry.path, fileKindSkill)
		}
		if front.err != nil {
			s.addFinding(finding("malformed-frontmatter", "a skill has malformed YAML frontmatter", domain.SeverityWarning, domain.CapabilitySkill, name))
		}
		exposure := s.skillExposure(name, []string{relativeCapabilityName(entry.root, entry.path, fileKindSkill)}, front)
		first, last := s.observation()
		s.addCapability(domain.Capability{
			Runtime:        domain.RuntimeClaudeCode,
			Type:           domain.CapabilitySkill,
			Name:           name,
			Scope:          entry.scope,
			Source:         entry.path,
			Enabled:        exposure.enabled,
			Advertisement:  exposure.advertisement,
			Hash:           hashBytes(data),
			MetadataTokens: exposure.metadata,
			BodyTokens:     estimateMeasurement(front.body),
			FirstSeen:      first,
			LastSeen:       last,
		})
	}
}

func (s *discoveryState) discoverCommands(ctx context.Context) {
	entries := s.collectEntries(ctx, fileKindCommand, inventoryRoots(s.adapter, "commands"))
	for _, entry := range entries {
		if err := contextError(ctx); err != nil {
			return
		}
		data, err := os.ReadFile(entry.path)
		if err != nil {
			s.addFileReadFinding(entry.path, fileKindCommand)
			continue
		}
		front := parseFrontMatter(data)
		name := front.value("name")
		if name == "" {
			name = relativeCapabilityName(entry.root, entry.path, fileKindCommand)
		}
		if front.err != nil {
			s.addFinding(finding("malformed-frontmatter", "a command has malformed YAML frontmatter", domain.SeverityWarning, domain.CapabilityCommand, name))
		}
		exposure := commandExposure(s, name, []string{relativeCapabilityName(entry.root, entry.path, fileKindCommand)}, front)
		first, last := s.observation()
		s.addCapability(domain.Capability{
			Runtime:        domain.RuntimeClaudeCode,
			Type:           domain.CapabilityCommand,
			Name:           name,
			Scope:          entry.scope,
			Source:         entry.path,
			Enabled:        exposure.enabled,
			Advertisement:  exposure.advertisement,
			Hash:           hashBytes(data),
			MetadataTokens: exposure.metadata,
			BodyTokens:     estimateMeasurement(front.body),
			FirstSeen:      first,
			LastSeen:       last,
		})
	}
}

func (s *discoveryState) discoverAgents(ctx context.Context) {
	entries := s.collectEntries(ctx, fileKindAgent, inventoryRoots(s.adapter, "agents"))
	for _, entry := range entries {
		if err := contextError(ctx); err != nil {
			return
		}
		data, err := os.ReadFile(entry.path)
		if err != nil {
			s.addFileReadFinding(entry.path, fileKindAgent)
			continue
		}
		front := parseFrontMatter(data)
		name := front.value("name")
		if name == "" {
			name = relativeCapabilityName(entry.root, entry.path, fileKindAgent)
		}
		if front.err != nil {
			s.addFinding(finding("malformed-frontmatter", "an agent has malformed YAML frontmatter", domain.SeverityWarning, domain.CapabilityAgent, name))
		}
		exposure := agentExposure()
		first, last := s.observation()
		s.addCapability(domain.Capability{
			Runtime:        domain.RuntimeClaudeCode,
			Type:           domain.CapabilityAgent,
			Name:           name,
			Scope:          entry.scope,
			Source:         entry.path,
			Enabled:        exposure.enabled,
			Advertisement:  exposure.advertisement,
			Hash:           hashBytes(data),
			MetadataTokens: exposure.metadata,
			BodyTokens:     unknownMeasurement(),
			FirstSeen:      first,
			LastSeen:       last,
		})
	}
}

func (s *discoveryState) discoverInstructions(ctx context.Context) {
	paths := instructionPaths(s.adapter)
	for _, candidate := range paths {
		if err := contextError(ctx); err != nil {
			return
		}
		data, exists, err := readOptional(candidate.path)
		if err != nil {
			if isBrokenSymlink(candidate.path) {
				s.addFinding(finding("broken-symlink", "an instruction path is a broken symlink", domain.SeverityWarning, domain.CapabilityInstructionFile, filepath.Base(candidate.path)))
			} else {
				s.addFileReadFinding(candidate.path, fileKindInstruction)
			}
			continue
		}
		if !exists {
			continue
		}
		first, last := s.observation()
		s.addCapability(domain.Capability{
			Runtime:        domain.RuntimeClaudeCode,
			Type:           domain.CapabilityInstructionFile,
			Name:           filepath.Base(candidate.path),
			Scope:          candidate.scope,
			Source:         candidate.path,
			Enabled:        domain.EnabledStateEnabled,
			Advertisement:  domain.AdvertisementStateFullyAdvertised,
			Hash:           hashBytes(data),
			MetadataTokens: unknownMeasurement(),
			BodyTokens:     estimateMeasurement(data),
			FirstSeen:      first,
			LastSeen:       last,
		})
	}
}

type instructionSpec struct {
	path  string
	scope domain.Scope
}

func instructionPaths(adapter *Adapter) []instructionSpec {
	if adapter == nil {
		return nil
	}
	result := make([]instructionSpec, 0, 12)
	add := func(path string, scope domain.Scope) {
		path = cleanConfiguredPath(path)
		if path == "" {
			return
		}
		for _, existing := range result {
			if existing.path == path {
				return
			}
		}
		result = append(result, instructionSpec{path: path, scope: scope})
	}
	if adapter.userClaudeDir != "" {
		add(filepath.Join(adapter.userClaudeDir, "CLAUDE.md"), domain.ScopeUser)
	}
	if adapter.projectRoot != "" {
		add(filepath.Join(adapter.projectRoot, "CLAUDE.md"), domain.ScopeProject)
		add(filepath.Join(adapter.projectRoot, "CLAUDE.local.md"), domain.ScopeProject)
		add(filepath.Join(adapter.projectRoot, ".claude", "CLAUDE.md"), domain.ScopeProject)
		add(filepath.Join(adapter.projectRoot, ".claude", "CLAUDE.local.md"), domain.ScopeProject)
	}
	current := adapter.currentDirectory
	if current != "" {
		for {
			add(filepath.Join(current, "CLAUDE.md"), domain.ScopeProject)
			add(filepath.Join(current, "CLAUDE.local.md"), domain.ScopeProject)
			if current == adapter.projectRoot || current == filepath.Dir(current) {
				break
			}
			parent := filepath.Dir(current)
			if adapter.projectRoot != "" && !isWithin(parent, adapter.projectRoot) {
				break
			}
			current = parent
		}
	}
	return result
}

func isWithin(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func relativeCapabilityName(root, path string, kind fileKind) string {
	name := path
	if rel, err := filepath.Rel(root, path); err == nil {
		name = rel
	}
	name = filepath.ToSlash(name)
	switch kind {
	case fileKindSkill:
		name = filepath.ToSlash(filepath.Dir(name))
		if name == "." || name == "" {
			name = filepath.Base(root)
		}
	case fileKindCommand, fileKindAgent:
		name = strings.TrimSuffix(name, filepath.Ext(name))
	}
	return strings.TrimSpace(name)
}

func (s *discoveryState) addFileReadFinding(path string, kind fileKind) {
	if isBrokenSymlink(path) {
		capabilityType := domain.CapabilityUnknown
		switch kind {
		case fileKindSkill:
			capabilityType = domain.CapabilitySkill
		case fileKindCommand:
			capabilityType = domain.CapabilityCommand
		case fileKindAgent:
			capabilityType = domain.CapabilityAgent
		case fileKindInstruction:
			capabilityType = domain.CapabilityInstructionFile
		}
		name := ""
		if capabilityType != domain.CapabilityUnknown {
			name = filepath.Base(path)
		}
		s.addFinding(finding("broken-symlink", "a Claude inventory path is a broken symlink", domain.SeverityWarning, capabilityType, name))
		return
	}
	s.addFinding(finding("file-read-error", "a Claude inventory file could not be read", domain.SeverityWarning, domain.CapabilityUnknown, ""))
}

type rootSpec struct {
	path  string
	scope domain.Scope
}

func inventoryRoots(adapter *Adapter, name string) []rootSpec {
	if adapter == nil {
		return nil
	}
	result := make([]rootSpec, 0, 2)
	if adapter.userClaudeDir != "" {
		result = append(result, rootSpec{path: filepath.Join(adapter.userClaudeDir, name), scope: domain.ScopeUser})
	}
	if adapter.projectRoot != "" {
		result = append(result, rootSpec{path: filepath.Join(adapter.projectRoot, ".claude", name), scope: domain.ScopeProject})
	}
	return result
}

func (s *discoveryState) collectEntries(ctx context.Context, kind fileKind, roots []rootSpec) []fileEntry {
	var result []fileEntry
	seenFiles := make(map[string]struct{})
	for _, root := range roots {
		if root.path == "" {
			continue
		}
		files, findings := walkInventoryRoot(ctx, root.path, kind)
		for _, findingValue := range findings {
			s.addFinding(findingValue)
		}
		for _, path := range files {
			key := filepath.Clean(path)
			if _, ok := seenFiles[key]; ok {
				continue
			}
			seenFiles[key] = struct{}{}
			result = append(result, fileEntry{path: path, root: root.path, scope: root.scope})
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].scope != result[j].scope {
			return result[i].scope < result[j].scope
		}
		return result[i].path < result[j].path
	})
	return result
}

func walkInventoryRoot(ctx context.Context, root string, kind fileKind) ([]string, []domain.Finding) {
	var files []string
	var findings []domain.Finding
	seenDirs := make(map[string]struct{})
	var walk func(string, string) error
	walk = func(sourcePath, actualPath string) error {
		if err := contextError(ctx); err != nil {
			return err
		}
		info, err := os.Stat(actualPath)
		if err != nil {
			if os.IsNotExist(err) {
				if isBrokenSymlink(sourcePath) {
					findings = append(findings, brokenFindingForPath(sourcePath, kind))
				}
				return nil
			}
			return err
		}
		if info.IsDir() {
			realPath := actualPath
			if resolved, resolveErr := filepath.EvalSymlinks(actualPath); resolveErr == nil {
				realPath = resolved
			}
			if _, ok := seenDirs[realPath]; ok {
				return nil
			}
			seenDirs[realPath] = struct{}{}
			entries, readErr := os.ReadDir(actualPath)
			if readErr != nil {
				return readErr
			}
			sort.SliceStable(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
			for _, entry := range entries {
				if err := walk(filepath.Join(sourcePath, entry.Name()), filepath.Join(actualPath, entry.Name())); err != nil {
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						return err
					}
					findings = append(findings, finding("directory-read-error", "a Claude inventory directory could not be read", domain.SeverityWarning, domain.CapabilityUnknown, ""))
				}
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if inventoryPathMatches(sourcePath, kind) {
			files = append(files, sourcePath)
		}
		return nil
	}

	if err := walk(root, root); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return files, findings
		}
		if isBrokenSymlink(root) {
			findings = append(findings, brokenFindingForPath(root, kind))
		} else if !os.IsNotExist(err) {
			findings = append(findings, finding("directory-read-error", "a Claude inventory directory could not be read", domain.SeverityWarning, domain.CapabilityUnknown, ""))
		}
	}
	sort.Strings(files)
	return files, findings
}

func inventoryPathMatches(path string, kind fileKind) bool {
	base := filepath.Base(path)
	switch kind {
	case fileKindSkill:
		return base == "SKILL.md"
	case fileKindCommand, fileKindAgent:
		return strings.EqualFold(filepath.Ext(base), ".md")
	default:
		return false
	}
}

func brokenFindingForPath(path string, kind fileKind) domain.Finding {
	typ := domain.CapabilityUnknown
	name := ""
	switch kind {
	case fileKindSkill:
		typ, name = domain.CapabilitySkill, filepath.Base(filepath.Dir(path))
	case fileKindCommand:
		typ, name = domain.CapabilityCommand, strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	case fileKindAgent:
		typ, name = domain.CapabilityAgent, strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	case fileKindInstruction:
		typ, name = domain.CapabilityInstructionFile, filepath.Base(path)
	}
	return finding("broken-symlink", "a Claude inventory path is a broken symlink", domain.SeverityWarning, typ, name)
}

func readOptional(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return data, true, nil
	}
	if os.IsNotExist(err) {
		if info, lstatErr := os.Lstat(path); lstatErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return nil, false, err
		}
		return nil, false, nil
	}
	return nil, false, err
}

func isBrokenSymlink(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	_, err = os.Stat(path)
	return err != nil && os.IsNotExist(err)
}

func (s *discoveryState) discoverMCP(ctx context.Context) {
	if s.adapter.projectRoot != "" {
		path := filepath.Join(s.adapter.projectRoot, ".mcp.json")
		data, exists, err := readOptional(path)
		if err != nil {
			s.addMCPReadFinding(path)
		} else if exists {
			s.readMCPConfig(ctx, path, data, domain.ScopeProject, nil)
		}
	}
	for _, path := range userClaudeJSONPaths(s.adapter) {
		if err := contextError(ctx); err != nil {
			return
		}
		data, exists, err := readOptional(path)
		if err != nil {
			s.addMCPReadFinding(path)
			continue
		}
		if !exists {
			continue
		}
		s.readUserClaudeJSON(ctx, path, data)
	}
}

func userClaudeJSONPaths(adapter *Adapter) []string {
	if adapter == nil {
		return nil
	}
	paths := make([]string, 0, 3)
	add := func(path string) {
		path = cleanConfiguredPath(path)
		if path == "" {
			return
		}
		for _, existing := range paths {
			if existing == path {
				return
			}
		}
		paths = append(paths, path)
	}
	if adapter.userClaudeDir != "" {
		add(filepath.Join(adapter.userClaudeDir, ".claude.json"))
		if filepath.Base(adapter.userClaudeDir) == ".claude" {
			add(filepath.Join(filepath.Dir(adapter.userClaudeDir), ".claude.json"))
		}
	}
	if adapter.userHome != "" {
		add(filepath.Join(adapter.userHome, ".claude.json"))
	}
	return paths
}

func (s *discoveryState) addMCPReadFinding(path string) {
	if isBrokenSymlink(path) {
		s.addFinding(finding("broken-symlink", "an MCP configuration path is a broken symlink", domain.SeverityWarning, domain.CapabilityMCPServer, filepath.Base(path)))
		return
	}
	s.addFinding(finding("config-read-error", "an MCP configuration file could not be read", domain.SeverityWarning, domain.CapabilityUnknown, ""))
}

func (s *discoveryState) readMCPConfig(ctx context.Context, path string, data []byte, scope domain.Scope, state *mcpState) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil || raw == nil {
		s.addFinding(finding("malformed-config", "an MCP configuration file is not valid JSON", domain.SeverityWarning, domain.CapabilityMCPServer, ""))
		return
	}
	servers, ok := rawObject(raw["mcpServers"])
	if !ok {
		if raw["mcpServers"] != nil {
			s.addFinding(finding("malformed-config", "the MCP servers value is not an object", domain.SeverityWarning, domain.CapabilityMCPServer, ""))
		}
		return
	}
	s.addMCPServers(ctx, path, scope, servers, state)
}

func (s *discoveryState) readUserClaudeJSON(ctx context.Context, path string, data []byte) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil || raw == nil {
		s.addFinding(finding("malformed-config", "the Claude global configuration is not valid JSON", domain.SeverityWarning, domain.CapabilityMCPServer, ""))
		return
	}
	if servers, ok := rawObject(raw["mcpServers"]); ok {
		state := mcpStateFromRaw(raw)
		s.addMCPServers(ctx, path, domain.ScopeUser, servers, &state)
	} else if raw["mcpServers"] != nil {
		s.addFinding(finding("malformed-config", "the global MCP servers value is not an object", domain.SeverityWarning, domain.CapabilityMCPServer, ""))
	}

	projects, ok := rawObject(raw["projects"])
	if !ok {
		if raw["projects"] != nil {
			s.addFinding(finding("malformed-config", "the global projects value is not an object", domain.SeverityWarning, domain.CapabilityMCPServer, ""))
		}
		return
	}
	projectKeys := make([]string, 0, len(projects))
	for key := range projects {
		projectKeys = append(projectKeys, key)
	}
	sort.Strings(projectKeys)
	for _, key := range projectKeys {
		if !s.relevantProjectKey(key) {
			continue
		}
		entry, ok := rawObject(projects[key])
		if !ok {
			s.addFinding(finding("malformed-config", "a Claude project entry is not an object", domain.SeverityWarning, domain.CapabilityMCPServer, ""))
			continue
		}
		state := mcpStateFromRaw(entry)
		servers, ok := rawObject(entry["mcpServers"])
		if !ok {
			if entry["mcpServers"] != nil {
				s.addFinding(finding("malformed-config", "a Claude project MCP servers value is not an object", domain.SeverityWarning, domain.CapabilityMCPServer, ""))
			}
			continue
		}
		s.addMCPServers(ctx, path+"#projects["+key+"]", domain.ScopeProject, servers, &state)
	}
}

func (s *discoveryState) relevantProjectKey(key string) bool {
	if s.adapter.currentDirectory == "" && s.adapter.projectRoot == "" {
		return false
	}
	key = cleanConfiguredPath(key)
	return key == s.adapter.currentDirectory || key == s.adapter.projectRoot
}

type mcpState struct {
	disabledMCP      map[string]struct{}
	enabledMCP       map[string]struct{}
	disabledMCPJSON  map[string]struct{}
	enabledMCPJSON   map[string]struct{}
	enableAllMCPJSON bool
}

func mcpStateFromRaw(raw map[string]json.RawMessage) mcpState {
	return mcpState{
		disabledMCP:      stringSet(rawStringArray(raw["disabledMcpServers"])),
		enabledMCP:       stringSet(rawStringArray(raw["enabledMcpServers"])),
		disabledMCPJSON:  stringSet(rawStringArray(raw["disabledMcpjsonServers"])),
		enabledMCPJSON:   stringSet(rawStringArray(raw["enabledMcpjsonServers"])),
		enableAllMCPJSON: rawBoolValue(raw["enableAllProjectMcpServers"]),
	}
}

func (s *discoveryState) addMCPServers(ctx context.Context, path string, scope domain.Scope, servers map[string]json.RawMessage, state *mcpState) {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := contextError(ctx); err != nil {
			return
		}
		raw := servers[name]
		config, ok := rawObject(raw)
		if !ok {
			s.addFinding(finding("malformed-config", "an MCP server definition is not an object", domain.SeverityWarning, domain.CapabilityMCPServer, name))
			continue
		}
		serverState := mcpEnabledState(scope, name, config, state)
		if command, ok := rawString(config["command"]); ok && strings.TrimSpace(command) != "" {
			if _, err := s.adapter.lookPath(command); err != nil {
				s.addFinding(finding("mcp-command-unresolved", "a configured local MCP command is not resolvable", domain.SeverityWarning, domain.CapabilityMCPServer, name))
			}
		} else if serverType, ok := rawString(config["type"]); ok && strings.EqualFold(strings.TrimSpace(serverType), "stdio") {
			s.addFinding(finding("malformed-config", "a stdio MCP server is missing its command", domain.SeverityWarning, domain.CapabilityMCPServer, name))
		}
		first, last := s.observation()
		s.addCapability(domain.Capability{
			Runtime:        domain.RuntimeClaudeCode,
			Type:           domain.CapabilityMCPServer,
			Name:           name,
			Scope:          scope,
			Source:         path,
			Enabled:        serverState,
			Advertisement:  domain.AdvertisementStateUnknown,
			Hash:           hashJSON(json.RawMessage(raw)),
			MetadataTokens: unknownMeasurement(),
			BodyTokens:     unknownMeasurement(),
			FirstSeen:      first,
			LastSeen:       last,
		})
	}
}

func mcpEnabledState(scope domain.Scope, name string, config map[string]json.RawMessage, state *mcpState) domain.EnabledState {
	if enabled, ok := rawBool(config["enabled"]); ok {
		if enabled {
			return domain.EnabledStateEnabled
		}
		return domain.EnabledStateDisabled
	}
	if disabled, ok := rawBool(config["disabled"]); ok && disabled {
		return domain.EnabledStateDisabled
	}
	if state == nil {
		return domain.EnabledStateUnknown
	}
	if _, ok := state.disabledMCPJSON[name]; ok {
		return domain.EnabledStateDisabled
	}
	if _, ok := state.enabledMCPJSON[name]; ok || state.enableAllMCPJSON {
		return domain.EnabledStateEnabled
	}
	if scope == domain.ScopeProject {
		if _, ok := state.disabledMCP[name]; ok {
			return domain.EnabledStateDisabled
		}
		if _, ok := state.enabledMCP[name]; ok {
			return domain.EnabledStateEnabled
		}
	}
	return domain.EnabledStateUnknown
}

func rawObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil, false
	}
	return value, true
}

func rawString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func rawBool(raw json.RawMessage) (bool, bool) {
	if len(raw) == 0 {
		return false, false
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, false
	}
	return value, true
}

func rawBoolValue(raw json.RawMessage) bool {
	value, _ := rawBool(raw)
	return value
}

func rawStringArray(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil
	}
	return values
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func (s *discoveryState) addDuplicateFindings() {
	groups := make(map[string][]domain.Capability)
	for _, capability := range s.capabilities {
		key := string(capability.Type) + "\x00" + capability.Name
		groups[key] = append(groups[key], capability)
	}
	keys := make([]string, 0, len(groups))
	for key, capabilities := range groups {
		if len(capabilities) < 2 {
			continue
		}
		locations := make(map[string]struct{})
		for _, capability := range capabilities {
			locations[string(capability.Scope)+"\x00"+capability.Source] = struct{}{}
		}
		if len(locations) < 2 {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		parts := strings.SplitN(key, "\x00", 2)
		capabilityType := domain.CapabilityType(parts[0])
		name := ""
		if len(parts) == 2 {
			name = parts[1]
		}
		s.addFinding(finding("duplicate-capability", "the same capability name is defined across Claude sources or scopes", domain.SeverityWarning, capabilityType, name))
	}
}
