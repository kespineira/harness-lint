package compatibility

import (
	"context"
	"errors"
	"testing"
	"time"
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
		if err != nil && containsAny(err.Error(), output, "SENTINEL_") {
			t.Fatalf("error leaked version output: %v", err)
		}
	}
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if needle != "" && len(value) >= len(needle) && index(value, needle) >= 0 {
			return true
		}
	}
	return false
}

func index(value, needle string) int {
	for i := 0; i+len(needle) <= len(value); i++ {
		if value[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
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
	got := detector.Detect(context.Background(), RuntimeClaudeCode)
	if got.Status != DetectionDetected || got.Version == nil || got.Version.String() != "2.1.232" {
		t.Fatalf("detection = %#v, want detected 2.1.232", got)
	}
	if gotName != "claude" || gotExecutable != "/fake/claude" || len(gotArgs) != 1 || gotArgs[0] != "--version" {
		t.Fatalf("injected calls = name %q executable %q args %#v", gotName, gotExecutable, gotArgs)
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
			got := detector.Detect(context.Background(), RuntimeCodex)
			if got.Status != test.wantStatus || !errors.Is(got.Diagnostic, test.wantErr) {
				t.Fatalf("detection = %#v, want status %q and error %v", got, test.wantStatus, test.wantErr)
			}
			if got.Version != nil || (got.Diagnostic != nil && containsAny(got.Diagnostic.Error(), "SENTINEL", "command contents")) {
				t.Fatalf("failure retained unsafe data: %#v", got)
			}
		})
	}
}

func TestEvaluateConservativeStates(t *testing.T) {
	exact := Version{Major: 2, Minor: 1, Patch: 232}
	policy := Policy{
		Runtime:    RuntimeClaudeCode,
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
	if got := Evaluate(Policy{Runtime: RuntimeCodex, Validated: []ValidatedVersion{{Version: Version{Major: 0, Minor: 147, Patch: 0}}}}, Version{Major: 0, Minor: 146, Patch: 0}); got.State != StateUnknown {
		t.Fatalf("without documented range/lower bound, state = %q, want unknown", got.State)
	}
}
