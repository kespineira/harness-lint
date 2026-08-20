package codex

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
	"go.yaml.in/yaml/v3"
)

type sourceRoot struct {
	path  string
	scope domain.Scope
}

type skillConfiguration struct {
	enabled bool
}

func (a *Adapter) discoverSkills(ctx context.Context, now time.Time, result *domain.Discovery) error {
	roots := make([]sourceRoot, 0, len(a.options.systemRoots)+4)
	for _, root := range projectAncestorRoots(a.options.projectRoot, a.options.currentDir, ".agents", "skills") {
		roots = append(roots, sourceRoot{path: root, scope: domain.ScopeProject})
	}
	if a.options.userHome != "" {
		roots = append(roots, sourceRoot{path: filepath.Join(a.options.userHome, ".agents", "skills"), scope: domain.ScopeUser})
	}
	for _, root := range a.options.systemRoots {
		roots = append(roots, sourceRoot{path: root, scope: domain.ScopeGlobal})
	}
	configured := a.userSkillConfigurations(result)
	for _, root := range roots {
		if err := contextErr(ctx); err != nil {
			return err
		}
		if err := a.walkRoot(ctx, root.path, result, func(path string, entry fs.DirEntry) error {
			if entry.Name() != "SKILL.md" || entry.IsDir() {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				if isBrokenSymlink(path) {
					addFinding(result, "broken-symlink", "skill file symlink target cannot be read", domain.CapabilitySkill, filepath.Base(filepath.Dir(path)))
				} else {
					addFinding(result, "skill-unreadable", "skill metadata file cannot be read", domain.CapabilitySkill, filepath.Base(filepath.Dir(path)))
				}
				return nil
			}
			doc := parseSkillDocument(content)
			name := doc.name
			if strings.TrimSpace(name) == "" {
				name = filepath.Base(filepath.Dir(path))
			}
			canonicalPath := a.canonicalSkillPath(path)
			configuredSkill, isConfigured := configured[canonicalPath]
			advertisement := domain.AdvertisementStateUnknown
			metadata := unknownMeasurement()
			validMetadata := !doc.malformed && strings.TrimSpace(doc.name) != "" && strings.TrimSpace(doc.description) != ""
			if isConfigured && !configuredSkill.enabled {
				advertisement = domain.AdvertisementStateNotAdvertised
				metadata = domain.Measurement{
					Confidence: domain.ConfidenceObserved,
					Basis:      "skill disabled by user configuration; configured-list metadata is zero/observed; not runtime cost",
				}
			} else if validMetadata {
				advertisement = domain.AdvertisementStateFullyAdvertised
				advertisedMetadata := []byte("path: " + canonicalPath + "\n" + string(doc.advertisedMetadata))
				metadata = estimatedMeasurement(advertisedMetadata, "configured-list maximum: advertised SKILL.md path, name, and description; descriptions may be shortened and skills omitted by budget")
			}
			capability := capabilityBase(now, domain.CapabilitySkill, name, root.scope, path, advertisement)
			if isConfigured && !configuredSkill.enabled {
				capability.Enabled = domain.EnabledStateDisabled
			} else if validMetadata {
				capability.Enabled = domain.EnabledStateEnabled
			}
			capability.Hash = hashBytes(content)
			capability.MetadataTokens = metadata
			capability.BodyTokens = estimatedMeasurement(doc.body, "skill body estimate")
			result.Capabilities = append(result.Capabilities, capability)
			if doc.malformed {
				addFinding(result, "malformed-skill-metadata", "skill frontmatter is missing, incomplete, or invalid", domain.CapabilitySkill, name)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (a *Adapter) userSkillConfigurations(result *domain.Discovery) map[string]skillConfiguration {
	configured := make(map[string]skillConfiguration)
	if a.options.configRoot == "" {
		return configured
	}
	configPath := filepath.Join(a.options.configRoot, "config.toml")
	content, exists, err := readOptionalFile(configPath)
	if err != nil || !exists {
		return configured
	}
	values, err := decodeTOML(content)
	if err != nil {
		// discoverMCP reports the parse finding for this same active user
		// config; avoid retaining a second parser-specific diagnostic here.
		return configured
	}
	rawSkills, exists := values["skills"]
	if !exists {
		return configured
	}
	skills, ok := rawSkills.(map[string]any)
	if !ok {
		addFinding(result, "malformed-config", "skills must be a TOML table", domain.CapabilitySkill, filepath.Base(configPath))
		return configured
	}
	rawConfig, exists := skills["config"]
	if !exists {
		return configured
	}
	entries, ok := skillConfigurationEntries(rawConfig)
	if !ok {
		addFinding(result, "malformed-config", "skills.config must be an array of TOML tables", domain.CapabilitySkill, filepath.Base(configPath))
		return configured
	}
	for _, entry := range entries {
		rawPath, hasPath := entry["path"]
		path, pathOK := rawPath.(string)
		if !hasPath || !pathOK || strings.TrimSpace(path) == "" {
			addFinding(result, "malformed-config", "skills.config path must be a non-empty string", domain.CapabilitySkill, filepath.Base(configPath))
			continue
		}
		enabled := true
		if rawEnabled, hasEnabled := entry["enabled"]; hasEnabled {
			var enabledOK bool
			enabled, enabledOK = rawEnabled.(bool)
			if !enabledOK {
				addFinding(result, "malformed-config", "skills.config enabled must be boolean", domain.CapabilitySkill, path)
				continue
			}
		}
		configured[a.canonicalSkillPath(path)] = skillConfiguration{enabled: enabled}
	}
	return configured
}

func skillConfigurationEntries(value any) ([]map[string]any, bool) {
	switch entries := value.(type) {
	case []any:
		result := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			object, ok := entry.(map[string]any)
			if !ok {
				return nil, false
			}
			result = append(result, object)
		}
		return result, true
	case []map[string]any:
		return append([]map[string]any(nil), entries...), true
	default:
		return nil, false
	}
}

func (a *Adapter) canonicalSkillPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path == "~" && a.options.userHome != "" {
		path = a.options.userHome
	} else if strings.HasPrefix(path, "~/") && a.options.userHome != "" {
		path = filepath.Join(a.options.userHome, strings.TrimPrefix(path, "~/"))
	} else if !filepath.IsAbs(path) && a.options.configRoot != "" {
		path = filepath.Join(a.options.configRoot, path)
	}
	path = filepath.Clean(path)
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		path = filepath.Join(path, "SKILL.md")
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}

// projectAncestorRoots returns roots from current directory toward the repo
// root.  The result is reversed so instruction/skill precedence can be read
// from the project root down to the current directory by callers that need
// that order.  Inventory itself is sorted before return.
func projectAncestorRoots(projectRoot, currentDir string, parts ...string) []string {
	if currentDir == "" {
		currentDir = projectRoot
	}
	if currentDir == "" {
		return nil
	}
	currentDir = filepath.Clean(currentDir)
	if strings.TrimSpace(projectRoot) == "" {
		return []string{filepath.Join(append([]string{currentDir}, parts...)...)}
	}
	projectRoot = filepath.Clean(projectRoot)
	dirs := make([]string, 0, 8)
	for dir := currentDir; ; dir = filepath.Dir(dir) {
		dirs = append(dirs, dir)
		if projectRoot != "." && projectRoot != "" && sameOrDescendant(dir, projectRoot) && dir == projectRoot {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		if projectRoot != "." && projectRoot != "" && !sameOrDescendant(parent, projectRoot) {
			break
		}
	}
	result := make([]string, 0, len(dirs))
	for i := len(dirs) - 1; i >= 0; i-- {
		result = append(result, filepath.Join(append([]string{dirs[i]}, parts...)...))
	}
	return result
}

func sameOrDescendant(path, root string) bool {
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "."
}

func (a *Adapter) discoverAgents(ctx context.Context, now time.Time, result *domain.Discovery) error {
	roots := []sourceRoot{}
	if a.options.configRoot != "" {
		roots = append(roots, sourceRoot{path: filepath.Join(a.options.configRoot, "agents"), scope: domain.ScopeUser})
	}
	if a.options.projectRoot != "" {
		roots = append(roots, sourceRoot{path: filepath.Join(a.options.projectRoot, ".codex", "agents"), scope: domain.ScopeProject})
	}
	for _, root := range roots {
		if err := contextErr(ctx); err != nil {
			return err
		}
		if err := a.walkRoot(ctx, root.path, result, func(path string, entry fs.DirEntry) error {
			if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".toml" {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				if isBrokenSymlink(path) {
					addFinding(result, "broken-symlink", "agent file symlink target cannot be read", domain.CapabilityAgent, filepath.Base(path))
				} else {
					addFinding(result, "agent-unreadable", "agent TOML file cannot be read", domain.CapabilityAgent, filepath.Base(path))
				}
				return nil
			}
			values, parseErr := decodeTOML(content)
			name, hasName := stringField(values, "name")
			_, hasDescription := stringField(values, "description")
			developerInstructions, _ := stringField(values, "developer_instructions")
			if strings.TrimSpace(name) == "" {
				name = strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			}
			// Agent name/description are identity and human-facing guidance in
			// the documented TOML format, not proof of automatic model
			// advertisement. Keep exposure and metadata unknown.
			capability := capabilityBase(now, domain.CapabilityAgent, name, root.scope, path, domain.AdvertisementStateUnknown)
			capability.Hash = hashBytes(content)
			capability.MetadataTokens = unknownMeasurement()
			capability.BodyTokens = estimatedMeasurement([]byte(developerInstructions), "agent developer instructions body estimate")
			result.Capabilities = append(result.Capabilities, capability)
			if parseErr != nil {
				addFinding(result, "malformed-agent-toml", "agent TOML configuration could not be parsed", domain.CapabilityAgent, name)
			} else if !hasName || !hasDescription {
				addFinding(result, "malformed-agent-metadata", "agent TOML must include name and description", domain.CapabilityAgent, name)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (a *Adapter) discoverInstructions(ctx context.Context, now time.Time, result *domain.Discovery) error {
	var roots []sourceRoot
	if a.options.configRoot != "" {
		// Codex's global instruction layer lives in CODEX_HOME (normally
		// ~/.codex), not directly beside the user's home directory.
		roots = append(roots, sourceRoot{path: a.options.configRoot, scope: domain.ScopeUser})
	}
	projectDirs := projectAncestorRoots(a.options.projectRoot, a.options.currentDir)
	for _, dir := range projectDirs {
		roots = append(roots, sourceRoot{path: dir, scope: domain.ScopeProject})
	}
	for _, root := range roots {
		if err := contextErr(ctx); err != nil {
			return err
		}
		// Codex gives AGENTS.override.md precedence over AGENTS.md in the
		// same directory.  Only the effective file enters the chain.
		candidates := []string{filepath.Join(root.path, "AGENTS.override.md"), filepath.Join(root.path, "AGENTS.md")}
		selected := ""
		for _, candidate := range candidates {
			info, err := os.Lstat(candidate)
			if err == nil {
				if info.Mode()&os.ModeSymlink != 0 {
					if _, statErr := os.Stat(candidate); statErr != nil {
						addFinding(result, "broken-symlink", "instruction file symlink target cannot be read", domain.CapabilityInstructionFile, filepath.Base(candidate))
						continue
					}
				}
				selected = candidate
				break
			}
			if !errors.Is(err, os.ErrNotExist) {
				addFinding(result, "instruction-unreadable", "instruction file metadata cannot be read", domain.CapabilityInstructionFile, filepath.Base(candidate))
			}
		}
		if selected == "" {
			continue
		}
		content, err := os.ReadFile(selected)
		if err != nil {
			if isBrokenSymlink(selected) {
				addFinding(result, "broken-symlink", "instruction file symlink target cannot be read", domain.CapabilityInstructionFile, filepath.Base(selected))
			} else {
				addFinding(result, "instruction-unreadable", "instruction file cannot be read", domain.CapabilityInstructionFile, filepath.Base(selected))
			}
			continue
		}
		name := filepath.Base(selected)
		capability := capabilityBase(now, domain.CapabilityInstructionFile, name, root.scope, selected, domain.AdvertisementStateFullyAdvertised)
		capability.Enabled = domain.EnabledStateEnabled
		capability.Hash = hashBytes(content)
		capability.MetadataTokens = unknownMeasurement()
		capability.BodyTokens = estimatedMeasurement(content, "configured AGENTS instruction body baseline estimate")
		result.Capabilities = append(result.Capabilities, capability)
	}
	return nil
}

func (a *Adapter) walkRoot(ctx context.Context, root string, result *domain.Discovery, visit func(string, fs.DirEntry) error) error {
	if strings.TrimSpace(root) == "" {
		return nil
	}
	info, err := os.Lstat(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		addFinding(result, "source-unreadable", "configured discovery root cannot be read", domain.CapabilityUnknown, "")
		return nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, statErr := os.Stat(root)
		if statErr != nil {
			addFinding(result, "broken-symlink", "configured discovery root symlink target cannot be read", domain.CapabilityUnknown, "")
			return nil
		}
		if !resolved.IsDir() {
			return nil
		}
		root, err = filepath.EvalSymlinks(root)
		if err != nil {
			addFinding(result, "source-unreadable", "configured discovery root symlink cannot be resolved", domain.CapabilityUnknown, "")
			return nil
		}
		info = resolved
	}
	if !info.IsDir() {
		return nil
	}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if contextErr(ctx) != nil {
			return contextErr(ctx)
		}
		if walkErr != nil {
			if isBrokenSymlink(path) {
				addFinding(result, "broken-symlink", "discovery symlink target cannot be read", domain.CapabilityUnknown, "")
			} else {
				addFinding(result, "source-unreadable", "discovery path cannot be read", domain.CapabilityUnknown, "")
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if _, statErr := os.Stat(path); statErr != nil {
				addFinding(result, "broken-symlink", "discovery symlink target cannot be read", domain.CapabilityUnknown, "")
			}
			if entry.IsDir() {
				return filepath.SkipDir
			}
		}
		return visit(path, entry)
	})
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

func isBrokenSymlink(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	_, err = os.Stat(path)
	return err != nil
}

type skillDocument struct {
	name               string
	description        string
	advertisedMetadata []byte
	body               []byte
	malformed          bool
}

func parseSkillDocument(content []byte) skillDocument {
	text := strings.TrimPrefix(string(content), "\ufeff")
	lines := strings.SplitAfter(text, "\n")
	doc := skillDocument{}
	if len(lines) == 0 || !isFrontmatterDelimiter(lines[0], "---") {
		doc.body = []byte(text)
		doc.malformed = true
		return doc
	}
	closeIndex := -1
	for i := 1; i < len(lines); i++ {
		line := strings.TrimSuffix(strings.TrimSuffix(lines[i], "\n"), "\r")
		if isFrontmatterDelimiter(line, "---") || isFrontmatterDelimiter(line, "...") {
			closeIndex = i
			break
		}
	}
	if closeIndex < 0 {
		doc.malformed = true
		return doc
	}
	frontmatter := []byte(strings.Join(lines[1:closeIndex], ""))
	doc.body = []byte(strings.Join(lines[closeIndex+1:], ""))
	values, ok := decodeSkillFrontmatter(frontmatter)
	if !ok {
		doc.malformed = true
		return doc
	}
	doc.name, _ = stringField(values, "name")
	doc.description, _ = stringField(values, "description")
	if strings.TrimSpace(doc.name) == "" || strings.TrimSpace(doc.description) == "" {
		doc.malformed = true
	}
	if !doc.malformed {
		doc.advertisedMetadata = []byte("name: " + doc.name + "\ndescription: " + doc.description + "\n")
	}
	return doc
}

func isFrontmatterDelimiter(line, delimiter string) bool {
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	return strings.TrimRight(line, " \t") == delimiter
}

func decodeSkillFrontmatter(frontmatter []byte) (map[string]any, bool) {
	decoder := yaml.NewDecoder(bytes.NewReader(frontmatter))
	var values map[string]any
	if err := decoder.Decode(&values); err != nil || values == nil {
		return nil, false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, false
	}
	return values, true
}

func stringField(values map[string]any, key string) (string, bool) {
	if values == nil {
		return "", false
	}
	value, ok := values[key]
	if !ok {
		return "", false
	}
	stringValue, ok := value.(string)
	return strings.TrimSpace(stringValue), ok && strings.TrimSpace(stringValue) != ""
}
