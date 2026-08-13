// Package codex implements the metadata-only adapter for OpenAI Codex.
//
// The adapter deliberately receives all environment-dependent paths and
// lookups through Options.  It never starts an MCP process, reads a live home
// directory implicitly, or returns conversation/tool payloads.
package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/runtime"
)

// Options contains the environment used by an Adapter. UserHome and
// ConfigRoot are separate because Codex configuration can be redirected while
// user skills remain rooted at the user's home. Empty paths disable that
// source; the constructor never consults the process HOME environment.
type Options struct {
	UserHome         string
	ConfigRoot       string
	ProjectRoot      string
	CurrentDirectory string
	SystemSkillRoots []string
	TranscriptRoots  []string
	HookEventPaths   []string
	Now              func() time.Time
	LookPath         func(string) (string, error)
}

type normalizedOptions struct {
	userHome       string
	configRoot     string
	projectRoot    string
	currentDir     string
	systemRoots    []string
	transcripts    []string
	hookEventPaths []string
	now            func() time.Time
	lookPath       func(string) (string, error)
}

// Adapter discovers Codex capabilities and imports metadata-only usage.
type Adapter struct{ options normalizedOptions }

var _ runtime.Adapter = (*Adapter)(nil)

// New constructs a Codex adapter.  All slices are copied so later caller
// mutation cannot change a running adapter's source set.
func New(opts Options) *Adapter {
	userHome := firstNonEmpty(opts.UserHome)
	configRoot := firstNonEmpty(opts.ConfigRoot)
	if configRoot == "" && userHome != "" {
		configRoot = filepath.Join(userHome, ".codex")
	}
	projectRoot := firstNonEmpty(opts.ProjectRoot)
	currentDir := firstNonEmpty(opts.CurrentDirectory)
	if currentDir == "" {
		currentDir = projectRoot
	}
	transcriptRoots := append([]string(nil), opts.TranscriptRoots...)
	if len(transcriptRoots) == 0 && configRoot != "" {
		transcriptRoots = []string{filepath.Join(configRoot, "sessions")}
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	lookPath := opts.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}

	return &Adapter{options: normalizedOptions{
		userHome:       cleanOptionalPath(userHome),
		configRoot:     cleanOptionalPath(configRoot),
		projectRoot:    cleanOptionalPath(projectRoot),
		currentDir:     cleanOptionalPath(currentDir),
		systemRoots:    cleanPaths(opts.SystemSkillRoots),
		transcripts:    cleanPaths(transcriptRoots),
		hookEventPaths: cleanPaths(opts.HookEventPaths),
		now:            now,
		lookPath:       lookPath,
	}}
}

// Runtime identifies the source harness.
func (a *Adapter) Runtime() domain.Runtime { return domain.RuntimeCodex }

// Discover returns a deterministic inventory snapshot.  It reads only the
// configured source paths and records diagnostics instead of failing an entire
// scan for one malformed optional file.
func (a *Adapter) Discover(ctx context.Context) (domain.Discovery, error) {
	if err := contextErr(ctx); err != nil {
		return domain.Discovery{}, err
	}
	if a == nil {
		return domain.Discovery{}, errors.New("codex adapter is nil")
	}
	now := a.options.now().UTC()
	var result domain.Discovery

	if err := a.discoverSkills(ctx, now, &result); err != nil {
		return domain.Discovery{}, err
	}
	if err := a.discoverAgents(ctx, now, &result); err != nil {
		return domain.Discovery{}, err
	}
	if err := a.discoverInstructions(ctx, now, &result); err != nil {
		return domain.Discovery{}, err
	}
	if err := a.discoverMCP(ctx, now, &result); err != nil {
		return domain.Discovery{}, err
	}
	if err := a.discoverHooks(ctx, now, &result); err != nil {
		return domain.Discovery{}, err
	}

	addDuplicateFindings(&result)
	sortDiscovery(&result)
	if err := result.Validate(); err != nil {
		return domain.Discovery{}, fmt.Errorf("validate Codex discovery: %w", err)
	}
	return result, nil
}

// ImportUsage imports explicit tool usage signals from configured Codex-session
// and file-captured PostToolUse-shaped JSONL/JSON files. The since boundary is
// inclusive and all timestamps are normalized to UTC before fingerprinting.
func (a *Adapter) ImportUsage(ctx context.Context, since time.Time) ([]domain.UsageEvent, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if a == nil {
		return nil, errors.New("codex adapter is nil")
	}
	events, err := a.importTranscriptUsage(ctx, since)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(events, func(i, j int) bool {
		left, right := events[i].EffectiveActivityTime(), events[j].EffectiveActivityTime()
		if left.Equal(right) {
			return events[i].Fingerprint < events[j].Fingerprint
		}
		return left.Before(right)
	})
	return events, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func cleanOptionalPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	return filepath.Clean(path)
}

func cleanPaths(paths []string) []string {
	result := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		path = cleanOptionalPath(path)
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

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func hashBytes(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func hashIdentifier(value string) string {
	return hashBytes([]byte(strings.TrimSpace(value)))
}

func unknownMeasurement() domain.Measurement {
	return domain.Measurement{Confidence: domain.ConfidenceUnknown}
}

func estimatedMeasurement(content []byte, basis string) domain.Measurement {
	if len(content) == 0 {
		return unknownMeasurement()
	}
	// This is an intentionally explicit advertised-size estimate.  It is not
	// presented as an exact tokenizer result or runtime context cost.
	value := int64((len(content) + 3) / 4)
	return domain.Measurement{
		Value:      value,
		Confidence: domain.ConfidenceEstimated,
		Basis:      basis + "; UTF-8 bytes divided by four, rounded up; not runtime cost",
	}
}

func capabilityBase(now time.Time, typ domain.CapabilityType, name string, scope domain.Scope, source string, advertisement domain.AdvertisementState) domain.Capability {
	return domain.Capability{
		Runtime:       domain.RuntimeCodex,
		Type:          typ,
		Name:          name,
		Scope:         scope,
		Source:        source,
		Enabled:       domain.EnabledStateUnknown,
		Advertisement: advertisement,
		FirstSeen:     now,
		LastSeen:      now,
	}
}

func addFinding(result *domain.Discovery, code, message string, typ domain.CapabilityType, name string) {
	result.Findings = append(result.Findings, domain.Finding{
		Runtime:        domain.RuntimeCodex,
		Code:           code,
		Message:        message,
		Severity:       domain.SeverityWarning,
		CapabilityType: typ,
		CapabilityName: name,
		Confidence:     domain.ConfidenceObserved,
	})
}

func addDuplicateFindings(result *domain.Discovery) {
	nameCounts := make(map[string]int)
	sourceCounts := make(map[string]int)
	for _, capability := range result.Capabilities {
		nameKey := string(capability.Type) + "\x00" + capability.Name
		sourceKey := nameKey + "\x00" + string(capability.Scope) + "\x00" + capability.Source
		nameCounts[nameKey]++
		sourceCounts[sourceKey]++
	}
	nameKeys := make([]string, 0, len(nameCounts))
	for key, count := range nameCounts {
		if count > 1 {
			nameKeys = append(nameKeys, key)
		}
	}
	sort.Strings(nameKeys)
	for _, key := range nameKeys {
		parts := strings.SplitN(key, "\x00", 2)
		typ := domain.CapabilityType(parts[0])
		name := parts[1]
		addFinding(result, "duplicate-capability-name", "capability name is advertised by multiple sources", typ, name)
	}
	sourceKeys := make([]string, 0, len(sourceCounts))
	for key, count := range sourceCounts {
		if count > 1 {
			sourceKeys = append(sourceKeys, key)
		}
	}
	sort.Strings(sourceKeys)
	for _, key := range sourceKeys {
		parts := strings.SplitN(key, "\x00", 4)
		addFinding(result, "duplicate-capability-source", "capability source was discovered more than once", domain.CapabilityType(parts[0]), parts[1])
	}
}

func sortDiscovery(result *domain.Discovery) {
	sort.SliceStable(result.Capabilities, func(i, j int) bool {
		a, b := result.Capabilities[i], result.Capabilities[j]
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Scope != b.Scope {
			return a.Scope < b.Scope
		}
		return a.Source < b.Source
	})
	sort.SliceStable(result.Findings, func(i, j int) bool {
		a, b := result.Findings[i], result.Findings[j]
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		if a.CapabilityType != b.CapabilityType {
			return a.CapabilityType < b.CapabilityType
		}
		if a.CapabilityName != b.CapabilityName {
			return a.CapabilityName < b.CapabilityName
		}
		return a.Message < b.Message
	})
}
