// Package compatibility contains the deliberately small compatibility model
// used by local runtime checks. It has no dependency on an installed runtime:
// callers provide both executable resolution and command execution.
package compatibility

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Runtime identifies a locally detectable runtime. Keep this type local to
// the package so version checks do not become part of the ingestion contract.
type Runtime string

const (
	RuntimeClaudeCode Runtime = "claude-code"
	RuntimeCodex      Runtime = "codex"
)

func (r Runtime) executable() string {
	switch r {
	case RuntimeClaudeCode:
		return "claude"
	case RuntimeCodex:
		return "codex-cli"
	default:
		return ""
	}
}

// Version is a semantic runtime version. Qualifier preserves prerelease or
// build information and is intentionally part of exact equality.
type Version struct {
	Major     int
	Minor     int
	Patch     int
	Qualifier string
}

func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d%s", v.Major, v.Minor, v.Patch, v.Qualifier)
}

func (v Version) valid() bool {
	return v.Major >= 0 && v.Minor >= 0 && v.Patch >= 0
}

// Equal compares all version components, including a prerelease/build
// qualifier. This prevents a prerelease from being called an exact validated
// release merely because its numeric components match.
func (v Version) Equal(other Version) bool {
	return v.Major == other.Major && v.Minor == other.Minor && v.Patch == other.Patch && v.Qualifier == other.Qualifier
}

// Compare orders numeric components first and then uses the conservative
// semver rule that an unqualified release is newer than a qualified release.
func (v Version) Compare(other Version) int {
	if v.Major != other.Major {
		return compareInt(v.Major, other.Major)
	}
	if v.Minor != other.Minor {
		return compareInt(v.Minor, other.Minor)
	}
	if v.Patch != other.Patch {
		return compareInt(v.Patch, other.Patch)
	}
	if v.Qualifier == other.Qualifier {
		return 0
	}
	if v.Qualifier == "" {
		return 1
	}
	if other.Qualifier == "" {
		return -1
	}
	return compareString(v.Qualifier, other.Qualifier)
}

func compareInt(left, right int) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func compareString(left, right string) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

var versionPattern = regexp.MustCompile(`(?:^|[^0-9A-Za-z])v?([0-9]+)\.([0-9]+)\.([0-9]+)((?:[-+][0-9A-Za-z.-]+)?)(?:$|[^0-9A-Za-z.-])`)

// ParseVersion extracts one version from a runtime's --version output. It
// accepts product prefixes and harmless suffixes, including output such as
// "Claude Code 2.1.232" and "codex-cli 0.147.0 (build ...)". Output is not
// retained by this package and parse errors never echo it.
func ParseVersion(output string) (Version, error) {
	if strings.TrimSpace(output) == "" {
		return Version{}, ErrMalformedVersionOutput
	}
	matches := versionPattern.FindAllStringSubmatch(output, -1)
	if len(matches) != 1 {
		return Version{}, ErrMalformedVersionOutput
	}
	major, err := parseVersionPart(matches[0][1])
	if err != nil {
		return Version{}, ErrMalformedVersionOutput
	}
	minor, err := parseVersionPart(matches[0][2])
	if err != nil {
		return Version{}, ErrMalformedVersionOutput
	}
	patch, err := parseVersionPart(matches[0][3])
	if err != nil {
		return Version{}, ErrMalformedVersionOutput
	}
	return Version{Major: major, Minor: minor, Patch: patch, Qualifier: matches[0][4]}, nil
}

func parseVersionPart(value string) (int, error) {
	parsed, err := strconv.ParseUint(value, 10, 31)
	if err != nil {
		return 0, err
	}
	return int(parsed), nil
}

var (
	ErrMalformedVersionOutput = errors.New("malformed runtime version output")
	ErrExecutableUnavailable  = errors.New("runtime executable unavailable")
	ErrVersionCommandFailed   = errors.New("runtime version command failed")
	ErrVersionCommandTimeout  = errors.New("runtime version command timed out")
	ErrUnsupportedRuntime     = errors.New("unsupported runtime")
)

// ExecutableResolver resolves a static executable name to a runnable path.
// Implementations should not inspect runtime data or execute commands.
type ExecutableResolver interface {
	Resolve(name string) (string, error)
}

// CommandRunner runs only the fixed version command supplied by Detector.
// Implementations should avoid retaining stdout/stderr; the detector parses
// the output in memory and never includes it in diagnostics.
type CommandRunner interface {
	Run(ctx context.Context, executable string, args ...string) (string, error)
}

// ResolveFunc and RunFunc make deterministic tests and future low-frequency
// doctor/hooks-test integrations easy to fake without a live installation.
type ResolveFunc func(name string) (string, error)

func (f ResolveFunc) Resolve(name string) (string, error) { return f(name) }

type RunFunc func(ctx context.Context, executable string, args ...string) (string, error)

func (f RunFunc) Run(ctx context.Context, executable string, args ...string) (string, error) {
	return f(ctx, executable, args...)
}

// DetectionStatus describes the non-version outcome without exposing command
// output or arbitrary resolver/runner errors.
type DetectionStatus string

const (
	DetectionDetected      DetectionStatus = "detected"
	DetectionMissing       DetectionStatus = "missing-executable"
	DetectionUnparseable   DetectionStatus = "unparsable-output"
	DetectionCommandFailed DetectionStatus = "command-failed"
	DetectionTimedOut      DetectionStatus = "timeout"
)

// Detection contains only static diagnostics and, when trustworthy, a parsed
// version. Version is nil for unavailable, failed, timed out, or malformed
// command results.
type Detection struct {
	Runtime    Runtime
	Version    *Version
	Status     DetectionStatus
	Diagnostic error
}

// Detector performs one explicitly requested version check. It is not used by
// ingestion and has no implicit process execution.
type Detector struct {
	Resolver ExecutableResolver
	Runner   CommandRunner
	Timeout  time.Duration
}

const DefaultVersionCommandTimeout = 2 * time.Second

func (d Detector) Detect(ctx context.Context, runtime Runtime) Detection {
	result := Detection{Runtime: runtime}
	name := runtime.executable()
	if name == "" {
		result.Status = DetectionMissing
		result.Diagnostic = ErrUnsupportedRuntime
		return result
	}
	if d.Resolver == nil || d.Runner == nil {
		result.Status = DetectionMissing
		result.Diagnostic = ErrExecutableUnavailable
		return result
	}
	executable, err := d.Resolver.Resolve(name)
	if err != nil || strings.TrimSpace(executable) == "" {
		result.Status = DetectionMissing
		result.Diagnostic = ErrExecutableUnavailable
		return result
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx := ctx
	cancel := func() {}
	if timeout := d.TimeoutOrDefault(); timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	output, err := d.Runner.Run(runCtx, executable, "--version")
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			result.Status = DetectionTimedOut
			result.Diagnostic = ErrVersionCommandTimeout
			return result
		}
		result.Status = DetectionCommandFailed
		result.Diagnostic = ErrVersionCommandFailed
		return result
	}
	version, err := ParseVersion(output)
	if err != nil {
		result.Status = DetectionUnparseable
		result.Diagnostic = ErrMalformedVersionOutput
		return result
	}
	result.Status = DetectionDetected
	result.Version = &version
	return result
}

func (d Detector) TimeoutOrDefault() time.Duration {
	if d.Timeout == 0 {
		return DefaultVersionCommandTimeout
	}
	return d.Timeout
}

// CompatibilityState is intentionally small. Unknown is the result whenever
// the model lacks an explicit fact or documented comparable range.
type CompatibilityState string

const (
	StateVerified             CompatibilityState = "verified"
	StateNewerThanTested      CompatibilityState = "newer-than-tested"
	StateOlderThanSupported   CompatibilityState = "older-than-supported"
	StateCompatibleUnverified CompatibilityState = "compatible-unverified"
	StateUnknown              CompatibilityState = "unknown"
)

// ValidatedVersion is an explicit fixture/version fact. The fact says which
// version was validated; it does not claim that an existing synthetic fixture
// was produced by that version.
type ValidatedVersion struct {
	Version Version
}

// VersionRange describes a documented comparable family. Min and Max are
// inclusive. A nil Max means the family has no documented upper bound.
type VersionRange struct {
	Min Version
	Max *Version
}

func (r VersionRange) contains(version Version) bool {
	if !r.Min.valid() || version.Compare(r.Min) < 0 {
		return false
	}
	return r.Max == nil || version.Compare(*r.Max) <= 0
}

// Policy is supplied by a caller that owns release/conformance evidence.
// Exact versions must be explicit validated facts; ranges must be backed by
// documentation; lower bounds are optional and are the only basis for the
// older-than-supported state.
type Policy struct {
	Runtime    Runtime
	Validated  []ValidatedVersion
	Comparable *VersionRange
	LowerBound *Version
}

// Compatibility is the result of evaluating a parsed local version against a
// policy. Version is retained for diagnostics but never contains command text.
type Compatibility struct {
	Runtime Runtime
	Version Version
	State   CompatibilityState
}

// Evaluate returns a conservative compatibility state. A newer release is
// never treated as parser failure; it is newer-than-tested only when it is in a
// documented comparable family/range and above the newest exact fact.
func Evaluate(policy Policy, version Version) Compatibility {
	result := Compatibility{Runtime: policy.Runtime, Version: version, State: StateUnknown}
	if !version.valid() {
		return result
	}
	for _, fact := range policy.Validated {
		if version.Equal(fact.Version) {
			result.State = StateVerified
			return result
		}
	}
	if policy.LowerBound != nil && version.Compare(*policy.LowerBound) < 0 {
		result.State = StateOlderThanSupported
		return result
	}
	if policy.Comparable == nil || !policy.Comparable.contains(version) {
		return result
	}
	newerThanFact := false
	for _, fact := range policy.Validated {
		if version.Compare(fact.Version) > 0 {
			newerThanFact = true
			break
		}
	}
	if newerThanFact {
		result.State = StateNewerThanTested
		return result
	}
	result.State = StateCompatibleUnverified
	return result
}
