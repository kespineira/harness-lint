package compatibility

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
)

func TestParseVersionCurrentOutputsAndSuffixes(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   Version
	}{
		{name: "claude", output: "Claude Code 2.1.232\n", want: Version{Major: 2, Minor: 1, Patch: 232}},
		{name: "codex", output: "codex-cli 0.147.0\n", want: Version{Major: 0, Minor: 147, Patch: 0}},
		{name: "prerelease", output: "Claude Code 2.1.233-beta.1", want: Version{Major: 2, Minor: 1, Patch: 233, Qualifier: "-beta.1"}},
		{name: "build suffix", output: "codex-cli 0.147.0+homebrew", want: Version{Major: 0, Minor: 147, Patch: 0, Qualifier: "+homebrew"}},
		{name: "parenthetical suffix", output: "codex-cli 0.147.0 (release build)", want: Version{Major: 0, Minor: 147, Patch: 0}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseVersion(test.output)
			if err != nil {
				t.Fatalf("ParseVersion() error = %v", err)
			}
			if !got.Equal(test.want) {
				t.Fatalf("ParseVersion() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestParseVersionRejectsMalformedOrAmbiguousOutputWithoutEchoingIt(t *testing.T) {
	for _, output := range []string{"", "Claude Code unknown", "1.2", "1.2.3 and 4.5.6", "SENTINEL_private-command"} {
		_, err := ParseVersion(output)
		if !errors.Is(err, ErrMalformedVersionOutput) {
			t.Fatalf("ParseVersion(%q) error = %v, want ErrMalformedVersionOutput", output, err)
		}
		if err != nil && ((output != "" && strings.Contains(err.Error(), output)) || strings.Contains(err.Error(), "SENTINEL_")) {
			t.Fatalf("error leaked version output: %v", err)
		}
	}
}

func TestDetectorUsesInjectedResolverAndRunner(t *testing.T) {
	var gotName, gotExecutable string
	var gotArgs []string
	detector := Detector{
		Resolver: ResolveFunc(func(name string) (string, error) {
			gotName = name
			return "/fake/claude", nil
		}),
		Runner: RunFunc(func(ctx context.Context, executable string, args ...string) (string, error) {
			gotExecutable = executable
			gotArgs = append([]string(nil), args...)
			return "Claude Code 2.1.232", nil
		}),
	}
	got := detector.Detect(context.Background(), domain.RuntimeClaudeCode)
	if got.Status != DetectionDetected || got.Version == nil || got.Version.String() != "2.1.232" {
		t.Fatalf("detection = %#v, want detected 2.1.232", got)
	}
	if got.Runtime != domain.RuntimeClaudeCode || gotName != "claude" || gotExecutable != "/fake/claude" || len(gotArgs) != 1 || gotArgs[0] != "--version" {
		t.Fatalf("injected calls = name %q executable %q args %#v", gotName, gotExecutable, gotArgs)
	}
}

func TestDetectorUsesCodexExecutableName(t *testing.T) {
	var gotName string
	detector := Detector{
		Resolver: ResolveFunc(func(name string) (string, error) {
			gotName = name
			return "/fake/codex", nil
		}),
		Runner: RunFunc(func(context.Context, string, ...string) (string, error) {
			return "codex-cli 0.147.0", nil
		}),
	}
	got := detector.Detect(context.Background(), domain.RuntimeCodex)
	if got.Runtime != domain.RuntimeCodex || got.Status != DetectionDetected || got.Version == nil {
		t.Fatalf("detection = %#v, want detected Codex runtime", got)
	}
	if gotName != "codex" {
		t.Fatalf("resolver name = %q, want codex", gotName)
	}
}

func TestDetectorFailureStatesAreStaticAndNeverRequireInstallation(t *testing.T) {
	tests := []struct {
		name       string
		resolver   ResolveFunc
		runner     RunFunc
		wantStatus DetectionStatus
		wantErr    error
	}{
		{name: "missing", resolver: func(string) (string, error) { return "", errors.New("missing /private/path") }, wantStatus: DetectionMissing, wantErr: ErrExecutableUnavailable},
		{name: "malformed", resolver: func(string) (string, error) { return "/fake/codex", nil }, runner: func(context.Context, string, ...string) (string, error) { return "codex-cli ???", nil }, wantStatus: DetectionUnparseable, wantErr: ErrMalformedVersionOutput},
		{name: "command failure", resolver: func(string) (string, error) { return "/fake/codex", nil }, runner: func(context.Context, string, ...string) (string, error) {
			return "SENTINEL_output", errors.New("command contents leaked")
		}, wantStatus: DetectionCommandFailed, wantErr: ErrVersionCommandFailed},
		{name: "timeout", resolver: func(string) (string, error) { return "/fake/codex", nil }, runner: func(ctx context.Context, _ string, _ ...string) (string, error) { <-ctx.Done(); return "", ctx.Err() }, wantStatus: DetectionTimedOut, wantErr: ErrVersionCommandTimeout},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detector := Detector{Resolver: test.resolver, Runner: test.runner, Timeout: 5 * time.Millisecond}
			got := detector.Detect(context.Background(), domain.RuntimeCodex)
			if got.Status != test.wantStatus || !errors.Is(got.Diagnostic, test.wantErr) {
				t.Fatalf("detection = %#v, want status %q and error %v", got, test.wantStatus, test.wantErr)
			}
			if got.Version != nil || (got.Diagnostic != nil && (strings.Contains(got.Diagnostic.Error(), "SENTINEL") || strings.Contains(got.Diagnostic.Error(), "command contents"))) {
				t.Fatalf("failure retained unsafe data: %#v", got)
			}
		})
	}
}

func TestEvaluateConservativeStates(t *testing.T) {
	exact := Version{Major: 2, Minor: 1, Patch: 232}
	policy := Policy{
		Runtime:    domain.RuntimeClaudeCode,
		Validated:  []ValidatedVersion{{Version: exact}},
		Comparable: &VersionRange{Min: Version{Major: 2, Minor: 1, Patch: 0}, Max: &Version{Major: 2, Minor: 2, Patch: 0}},
		LowerBound: &Version{Major: 2, Minor: 0, Patch: 0},
	}
	tests := []struct {
		name    string
		version Version
		want    CompatibilityState
	}{
		{name: "exact validated", version: exact, want: StateVerified},
		{name: "newer", version: Version{Major: 2, Minor: 1, Patch: 233}, want: StateNewerThanTested},
		{name: "older but comparable", version: Version{Major: 2, Minor: 1, Patch: 231}, want: StateCompatibleUnverified},
		{name: "older than explicit bound", version: Version{Major: 1, Minor: 9, Patch: 9}, want: StateOlderThanSupported},
		{name: "outside family", version: Version{Major: 3, Minor: 0, Patch: 0}, want: StateUnknown},
		{name: "prerelease", version: Version{Major: 2, Minor: 1, Patch: 232, Qualifier: "-rc.1"}, want: StateCompatibleUnverified},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Evaluate(policy, test.version); got.State != test.want {
				t.Fatalf("Evaluate() state = %q, want %q", got.State, test.want)
			}
		})
	}
	if got := Evaluate(Policy{Runtime: domain.RuntimeCodex, Validated: []ValidatedVersion{{Version: Version{Major: 0, Minor: 147, Patch: 0}}}}, Version{Major: 0, Minor: 146, Patch: 0}); got.State != StateUnknown {
		t.Fatalf("without documented range/lower bound, state = %q, want unknown", got.State)
	}
}

func TestUnsupportedDomainRuntimeIsConservative(t *testing.T) {
	called := false
	detector := Detector{
		Resolver: ResolveFunc(func(string) (string, error) {
			called = true
			return "/unexpected", nil
		}),
		Runner: RunFunc(func(context.Context, string, ...string) (string, error) {
			called = true
			return "3.0.0", nil
		}),
	}
	got := detector.Detect(context.Background(), domain.RuntimeCursor)
	if got.Status != DetectionMissing || !errors.Is(got.Diagnostic, ErrUnsupportedRuntime) || called {
		t.Fatalf("unsupported detection = %#v, resolver/runner called = %t", got, called)
	}
	if got := Evaluate(Policy{Runtime: domain.RuntimeCursor, Validated: []ValidatedVersion{{Version: Version{Major: 3, Minor: 0, Patch: 0}}}}, Version{Major: 3, Minor: 0, Patch: 0}); got.State != StateUnknown {
		t.Fatalf("unsupported evaluation state = %q, want unknown", got.State)
	}
}
