package codex

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
)

type sourceRoot struct {
	path  string
	scope domain.Scope
}

func (a *Adapter) discoverSkills(ctx context.Context, now time.Time, result *domain.Discovery) error {
	roots := make([]sourceRoot, 0, len(a.options.systemSkillRoots)+4)
	for _, root := range projectAncestorRoots(a.options.repoRoot, a.options.currentDir, ".agents", "skills") {
		roots = append(roots, sourceRoot{path: root, scope: domain.ScopeProject})
	}
	if a.options.userHome != "" {
		roots = append(roots, sourceRoot{path: filepath.Join(a.options.userHome, ".agents", "skills"), scope: domain.ScopeUser})
	}
	for _, root := range a.options.systemSkillRoots {
		roots = append(roots, sourceRoot{path: root, scope: domain.ScopeGlobal})
	}
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
			capability := capabilityBase(now, domain.CapabilitySkill, name, root.scope, path)
			capability.Hash = hashBytes(content)
			capability.MetadataTokens = estimatedMeasurement(doc.metadata, "skill frontmatter metadata estimate")
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

// projectAncestorRoots returns roots from current directory toward the repo
// root.  The result is reversed so instruction/skill precedence can be read
// from the project root down to the current directory by callers that need
// that order.  Inventory itself is sorted before return.
func projectAncestorRoots(repoRoot, currentDir string, parts ...string) []string {
	if currentDir == "" {
		currentDir = repoRoot
	}
	if currentDir == "" {
		return nil
	}
	currentDir = filepath.Clean(currentDir)
	if strings.TrimSpace(repoRoot) == "" {
		return []string{filepath.Join(append([]string{currentDir}, parts...)...)}
	}
	repoRoot = filepath.Clean(repoRoot)
	dirs := make([]string, 0, 8)
	for dir := currentDir; ; dir = filepath.Dir(dir) {
		dirs = append(dirs, dir)
		if repoRoot != "." && repoRoot != "" && sameOrDescendant(dir, repoRoot) && dir == repoRoot {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		if repoRoot != "." && repoRoot != "" && !sameOrDescendant(parent, repoRoot) {
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
	if a.options.repoRoot != "" {
		roots = append(roots, sourceRoot{path: filepath.Join(a.options.repoRoot, ".codex", "agents"), scope: domain.ScopeProject})
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
			values, parseErr := parseTOML(content)
			name, hasName := stringField(values, "name")
			if strings.TrimSpace(name) == "" {
				name = strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			}
			capability := capabilityBase(now, domain.CapabilityAgent, name, root.scope, path)
			capability.Hash = hashBytes(content)
			capability.MetadataTokens = unknownMeasurement()
			capability.BodyTokens = unknownMeasurement()
			result.Capabilities = append(result.Capabilities, capability)
			if parseErr != nil {
				addFinding(result, "malformed-agent-toml", "agent TOML configuration could not be parsed", domain.CapabilityAgent, name)
			} else if !hasName {
				addFinding(result, "malformed-agent-metadata", "agent TOML has no name; filename fallback was used", domain.CapabilityAgent, name)
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
	projectDirs := projectAncestorRoots(a.options.repoRoot, a.options.currentDir)
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
		capability := capabilityBase(now, domain.CapabilityInstructionFile, name, root.scope, selected)
		capability.Enabled = domain.EnabledStateEnabled
		capability.Hash = hashBytes(content)
		capability.MetadataTokens = unknownMeasurement()
		capability.BodyTokens = unknownMeasurement()
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
	name        string
	description string
	metadata    []byte
	body        []byte
	malformed   bool
}

func parseSkillDocument(content []byte) skillDocument {
	text := strings.TrimPrefix(string(content), "\ufeff")
	lines := strings.SplitAfter(text, "\n")
	doc := skillDocument{}
	if len(lines) == 0 || strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(lines[0], "\n"), "\r")) != "---" {
		doc.body = []byte(text)
		doc.malformed = true
		return doc
	}
	closeIndex := -1
	for i := 1; i < len(lines); i++ {
		line := strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(lines[i], "\n"), "\r"))
		if line == "---" || line == "..." {
			closeIndex = i
			break
		}
	}
	if closeIndex < 0 {
		doc.metadata = []byte(strings.Join(lines[1:], ""))
		doc.malformed = true
		return doc
	}
	doc.metadata = []byte(strings.Join(lines[1:closeIndex], ""))
	doc.body = []byte(strings.Join(lines[closeIndex+1:], ""))
	seenKeys := make(map[string]struct{})
	for _, line := range strings.Split(string(doc.metadata), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) == "" {
			doc.malformed = true
			continue
		}
		key = strings.TrimSpace(key)
		if _, exists := seenKeys[key]; exists {
			doc.malformed = true
		}
		seenKeys[key] = struct{}{}
		var scalarOK bool
		value, scalarOK = parseFrontmatterScalar(strings.TrimSpace(value))
		if !scalarOK {
			doc.malformed = true
		}
		switch key {
		case "name":
			doc.name = value
		case "description":
			doc.description = value
		}
	}
	if strings.TrimSpace(doc.name) == "" || strings.TrimSpace(doc.description) == "" {
		doc.malformed = true
	}
	return doc
}

func parseFrontmatterScalar(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	if value[0] == '"' || value[0] == '\'' {
		if len(value) < 2 || value[len(value)-1] != value[0] {
			return "", false
		}
		return value[1 : len(value)-1], true
	}
	if (value[0] == '[' && !strings.HasSuffix(value, "]")) || (value[0] == '{' && !strings.HasSuffix(value, "}")) {
		return "", false
	}
	return value, true
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
