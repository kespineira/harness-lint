// Package claude implements the metadata-only Claude Code runtime adapter.
//
// The adapter intentionally treats Claude Code's files as observations, not
// as executable configuration. It never starts a configured command or
// contacts an MCP endpoint; command lookup is used only to report whether a
// local executable appears resolvable.
package claude

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
	runtimepkg "github.com/kespineira/harness-lint/internal/runtime"
)

const (
	// tokenEstimateBasis is deliberately explicit: these values are advertised
	// size approximations, never a claim about Claude's actual context cost.
	tokenEstimateBasis = "estimated from UTF-8 bytes at approximately 4 bytes/token; not runtime cost"

	unknownMeasurementBasis = "not available from Claude Code configuration"
)

// Options controls every filesystem and process lookup used by Adapter.
// Supplying these roots is the supported way to run the adapter in tests. No
// option is inferred from the process environment, so a test cannot
// accidentally inspect the caller's HOME.
type Options struct {
	// UserHome is the synthetic home directory containing .claude and
	// .claude.json. ConfigRoot, when set, replaces UserHome/.claude for the
	// global Claude configuration directory.
	UserHome   string
	ConfigRoot string

	// ProjectRoot is the project containing .claude and .mcp.json. The
	// CurrentDirectory controls which project instruction hierarchy and
	// ~/.claude.json project entry are relevant.
	ProjectRoot      string
	CurrentDirectory string
	CurrentDir       string // alias accepted for callers using shorter naming

	// Now and Clock are equivalent injectable clocks. Clock takes precedence.
	Now   func() time.Time
	Clock func() time.Time

	// LookPath and CommandLookup are equivalent injectable command lookups.
	// The lookup must only resolve a command; the adapter never executes it.
	LookPath      func(string) (string, error)
	CommandLookup func(string) (string, error)

	// TranscriptRoots supplies Claude JSONL roots. If empty, the adapter uses
	// the configured global Claude projects directory when one is available.
	TranscriptRoots []string
	TranscriptRoot  string // singular alias

	// HookEventPaths supplies stable PostToolUse-shaped JSON files. Roots are
	// walked for .json and .jsonl files in deterministic order.
	HookEventPaths []string
	HookEventRoots []string
	HookEventRoot  string
	HookRoots      []string // alias accepted for callers that call them hook roots
}

// Adapter is the Claude Code implementation of runtime.Adapter.
type Adapter struct {
	userHome         string
	userClaudeDir    string
	projectRoot      string
	currentDirectory string

	now      func() time.Time
	lookPath func(string) (string, error)

	transcriptRoots []string
	hookPaths       []string
	hookRoots       []string
}

var _ runtimepkg.Adapter = (*Adapter)(nil)

// New constructs an adapter with deterministic, injected roots and services.
// Empty roots simply disable that source; in particular, an empty UserHome
// never falls back to the live process HOME.
func New(options Options) *Adapter {
	userHome := cleanConfiguredPath(options.UserHome)
	configRoot := cleanConfiguredPath(options.ConfigRoot)
	userClaudeDir := configRoot
	if userClaudeDir == "" && userHome != "" {
		userClaudeDir = filepath.Join(userHome, ".claude")
	}

	projectRoot := cleanConfiguredPath(options.ProjectRoot)
	currentDirectory := cleanConfiguredPath(options.CurrentDirectory)
	if currentDirectory == "" {
		currentDirectory = cleanConfiguredPath(options.CurrentDir)
	}
	if projectRoot == "" {
		projectRoot = currentDirectory
	}
	if currentDirectory == "" {
		currentDirectory = projectRoot
	}

	now := options.Now
	if options.Clock != nil {
		now = options.Clock
	}
	if now == nil {
		now = time.Now
	}

	lookPath := options.LookPath
	if options.CommandLookup != nil {
		lookPath = options.CommandLookup
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}

	transcriptRoots := append([]string(nil), options.TranscriptRoots...)
	if options.TranscriptRoot != "" {
		transcriptRoots = append(transcriptRoots, options.TranscriptRoot)
	}
	if len(transcriptRoots) == 0 && userClaudeDir != "" {
		transcriptRoots = append(transcriptRoots, filepath.Join(userClaudeDir, "projects"))
	}
	transcriptRoots = cleanUniquePaths(transcriptRoots)

	hookPaths := append([]string(nil), options.HookEventPaths...)
	hookPaths = cleanUniquePaths(hookPaths)
	hookRoots := append([]string(nil), options.HookEventRoots...)
	if options.HookEventRoot != "" {
		hookRoots = append(hookRoots, options.HookEventRoot)
	}
	hookRoots = append(hookRoots, options.HookRoots...)
	hookRoots = cleanUniquePaths(hookRoots)

	return &Adapter{
		userHome:         userHome,
		userClaudeDir:    userClaudeDir,
		projectRoot:      projectRoot,
		currentDirectory: currentDirectory,
		now:              now,
		lookPath:         lookPath,
		transcriptRoots:  transcriptRoots,
		hookPaths:        hookPaths,
		hookRoots:        hookRoots,
	}
}

// NewAdapter is a descriptive alias for New.
func NewAdapter(options Options) *Adapter { return New(options) }

// Runtime identifies the source runtime in all normalized values.
func (a *Adapter) Runtime() domain.Runtime { return domain.RuntimeClaudeCode }

// Discover scans only configured Claude Code inventory files. Missing
// optional files are normal and do not produce findings; malformed files and
// broken links are retained as deterministic findings while the rest of the
// inventory is still returned.
func (a *Adapter) Discover(ctx context.Context) (domain.Discovery, error) {
	if a == nil {
		return domain.Discovery{}, errors.New("claude adapter is nil")
	}
	if err := contextError(ctx); err != nil {
		return domain.Discovery{}, err
	}

	state := discoveryState{
		adapter: a,
		seenAt:  a.observationTime(),
	}
	state.loadSettings(ctx)
	state.discoverSkills(ctx)
	state.discoverCommands(ctx)
	state.discoverAgents(ctx)
	state.discoverInstructions(ctx)
	state.discoverMCP(ctx)
	state.addDuplicateFindings()
	state.sort()
	if err := contextError(ctx); err != nil {
		return domain.Discovery{}, err
	}

	result := domain.Discovery{Capabilities: state.capabilities, Findings: state.findings}
	if err := result.Validate(); err != nil {
		return domain.Discovery{}, fmt.Errorf("validate Claude discovery: %w", err)
	}
	return result, nil
}

type discoveryState struct {
	adapter *Adapter
	seenAt  time.Time

	capabilities []domain.Capability
	findings     []domain.Finding

	settings []settingsFile
}

type settingsFile struct {
	path  string
	scope domain.Scope
	raw   map[string]json.RawMessage
}

func (s *discoveryState) addCapability(capability domain.Capability) {
	s.capabilities = append(s.capabilities, capability)
}

func (s *discoveryState) addFinding(finding domain.Finding) {
	s.findings = append(s.findings, finding)
}

func (s *discoveryState) observation() (time.Time, time.Time) {
	if s.seenAt.IsZero() {
		return time.Time{}, time.Time{}
	}
	seen := s.seenAt.UTC()
	return seen, seen
}

func (a *Adapter) observationTime() time.Time {
	if a == nil || a.now == nil {
		return time.Time{}
	}
	return a.now().UTC()
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func cleanConfiguredPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	return filepath.Clean(path)
}

func cleanUniquePaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		path = cleanConfiguredPath(path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		result = append(result, path)
	}
	return result
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hashIdentifier(value string) string {
	return hashBytes([]byte(strings.TrimSpace(value)))
}

func canonicalJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return data
}

func hashJSON(value any) string { return hashBytes(canonicalJSON(value)) }

func unknownMeasurement() domain.Measurement {
	return domain.Measurement{Confidence: domain.ConfidenceUnknown}
}

func estimateMeasurement(data []byte) domain.Measurement {
	return domain.Measurement{
		Value:      estimateTokens(data),
		Confidence: domain.ConfidenceEstimated,
		Basis:      tokenEstimateBasis,
	}
}

func estimateTokens(data []byte) int64 {
	if len(data) == 0 {
		return 0
	}
	// Four UTF-8 bytes per token is a deliberately coarse, stable advertised
	// size estimate. Round up so non-empty metadata/body does not disappear.
	return int64((len(data) + 3) / 4)
}

func finding(code, message string, severity domain.Severity, capabilityType domain.CapabilityType, capabilityName string) domain.Finding {
	if capabilityType == domain.CapabilityUnknown || strings.TrimSpace(capabilityName) == "" {
		capabilityType = domain.CapabilityUnknown
		capabilityName = ""
	}
	return domain.Finding{
		Runtime:        domain.RuntimeClaudeCode,
		Code:           code,
		Message:        message,
		Severity:       severity,
		CapabilityType: capabilityType,
		CapabilityName: strings.TrimSpace(capabilityName),
		Confidence:     domain.ConfidenceObserved,
	}
}

func (s *discoveryState) sort() {
	sort.SliceStable(s.capabilities, func(i, j int) bool {
		left, right := s.capabilities[i], s.capabilities[j]
		return capabilityLess(left, right)
	})
	sort.SliceStable(s.findings, func(i, j int) bool {
		left, right := s.findings[i], s.findings[j]
		if left.Code != right.Code {
			return left.Code < right.Code
		}
		if left.CapabilityType != right.CapabilityType {
			return left.CapabilityType < right.CapabilityType
		}
		if left.CapabilityName != right.CapabilityName {
			return left.CapabilityName < right.CapabilityName
		}
		if left.Message != right.Message {
			return left.Message < right.Message
		}
		if left.Severity != right.Severity {
			return left.Severity < right.Severity
		}
		return left.Confidence < right.Confidence
	})
}

func capabilityLess(left, right domain.Capability) bool {
	if left.Runtime != right.Runtime {
		return left.Runtime < right.Runtime
	}
	if left.Type != right.Type {
		return left.Type < right.Type
	}
	if left.Name != right.Name {
		return left.Name < right.Name
	}
	if left.Scope != right.Scope {
		return left.Scope < right.Scope
	}
	return left.Source < right.Source
}
