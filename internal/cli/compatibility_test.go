package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kespineira/harness-lint/internal/compatibility"
	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/hooks"
)

func TestDoctorCompatibilityDiagnosticsAreConservativeAndBounded(t *testing.T) {
	tests := []struct {
		name        string
		claudeOut   string
		claudeErr   error
		missing     bool
		wantVersion string
		wantStatus  string
		wantDetect  string
	}{
		{name: "validated-shaped output without live basis", claudeOut: "Claude Code 2.1.232", wantVersion: "2.1.232", wantStatus: "unknown", wantDetect: "detected"},
		{name: "newer is not parser failure", claudeOut: "Claude Code 2.1.233", wantVersion: "2.1.233", wantStatus: "unknown", wantDetect: "detected"},
		{name: "older is not parser failure", claudeOut: "Claude Code 1.9.9", wantVersion: "1.9.9", wantStatus: "unknown", wantDetect: "detected"},
		{name: "prerelease is detected but unverified", claudeOut: "Claude Code 2.1.232-rc.1", wantVersion: "2.1.232-rc.1", wantStatus: "unknown", wantDetect: "detected"},
		{name: "malformed is unknown", claudeOut: "Claude Code unknown", wantStatus: "unknown", wantDetect: "unparsable-output"},
		{name: "missing is unknown", missing: true, wantStatus: "unknown", wantDetect: "missing-executable"},
		{name: "capture failure is unknown", claudeErr: errors.New("COMMAND_SENTINEL"), wantStatus: "unknown", wantDetect: "command-failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home")
			project := filepath.Join(root, "project")
			for _, path := range []string{filepath.Join(home, ".claude"), filepath.Join(home, ".codex"), project} {
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatalf("mkdir %s: %v", path, err)
				}
			}
			options := Options{
				Home:        home,
				CWD:         root,
				ProjectRoot: project,
				VersionResolver: compatibility.ResolveFunc(func(name string) (string, error) {
					if name == "claude" && test.missing {
						return "", os.ErrNotExist
					}
					return "/fake/" + name, nil
				}),
				VersionRunner: compatibility.RunFunc(func(_ context.Context, executable string, args ...string) (string, error) {
					if executable == "/fake/claude" {
						if test.claudeErr != nil {
							return "COMMAND_OUTPUT_SENTINEL", test.claudeErr
						}
						return test.claudeOut, nil
					}
					return "codex-cli 0.147.0", nil
				}),
			}

			var stdout, stderr bytes.Buffer
			if err := ExecuteWithOptions(options, []string{"doctor"}, nil, &stdout, &stderr); err != nil {
				t.Fatalf("doctor error = %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
			}
			output := stdout.String()
			want := "compatibility runtime=claude-code detected-version="
			if !strings.Contains(output, want) || !strings.Contains(output, "latest-validated=unknown") || !strings.Contains(output, "validation-basis=synthetic runtime-conformance fixtures; no live runtime version validated") {
				t.Fatalf("doctor output = %q, missing bounded compatibility fields", output)
			}
			line := compatibilityLine(output, "claude-code")
			if !strings.Contains(line, "status="+test.wantStatus) || !strings.Contains(line, "detection="+test.wantDetect) {
				t.Fatalf("Claude compatibility line = %q, want status=%s detection=%s", line, test.wantStatus, test.wantDetect)
			}
			if test.wantVersion != "" && !strings.Contains(line, "detected-version="+test.wantVersion) {
				t.Fatalf("Claude compatibility line = %q, want detected version=%s", line, test.wantVersion)
			}
			if test.name == "newer is not parser failure" && strings.Contains(line, "unparsable-output") {
				t.Fatalf("newer version was reported as parser failure: %q", line)
			}
			for _, sentinel := range []string{"COMMAND_SENTINEL", "COMMAND_OUTPUT_SENTINEL"} {
				if strings.Contains(output, sentinel) || strings.Contains(stderr.String(), sentinel) {
					t.Fatalf("diagnostics leaked %q: stdout=%q stderr=%q", sentinel, output, stderr.String())
				}
			}
		})
	}
}

func TestHooksTestCompatibilityDoesNotTurnMissingOrMalformedVersionIntoBroken(t *testing.T) {
	tests := []struct {
		name        string
		versionOut  string
		missing     bool
		brokenBin   bool
		wantError   bool
		wantVersion string
	}{
		{name: "verified", versionOut: "Claude Code 2.1.232", wantVersion: "2.1.232"},
		{name: "missing runtime version", missing: true, wantVersion: "Not available (runtime executable not found)"},
		{name: "malformed runtime version", versionOut: "Claude Code ???", wantVersion: "Unknown (version output not recognized)"},
		{name: "separate hook executable failure", missing: true, brokenBin: true, wantError: true, wantVersion: "Not available (runtime executable not found)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			claudeRoot := filepath.Join(root, "claude")
			dbPath := filepath.Join(root, "state", "hooks.db")
			if err := os.MkdirAll(claudeRoot, 0o755); err != nil {
				t.Fatalf("mkdir Claude root: %v", err)
			}
			installLookPath := func(string) (string, error) { return "/opt/harness-lint", nil }
			if _, err := hooks.NewClaude(hooks.Options{ConfigRoot: claudeRoot, LookPath: installLookPath}).Install(context.Background()); err != nil {
				t.Fatalf("install hooks: %v", err)
			}
			lookPath := func(name string) (string, error) {
				if test.brokenBin {
					return "", os.ErrNotExist
				}
				return "/opt/harness-lint", nil
			}
			options := Options{
				CWD:      root,
				LookPath: lookPath,
				VersionResolver: compatibility.ResolveFunc(func(name string) (string, error) {
					if name == "claude" && test.missing {
						return "", os.ErrNotExist
					}
					return "/fake/" + name, nil
				}),
				VersionRunner: compatibility.RunFunc(func(_ context.Context, _ string, _ ...string) (string, error) {
					return test.versionOut, nil
				}),
			}
			var stdout, stderr bytes.Buffer
			err := ExecuteWithOptions(options, []string{"hooks", "test", "claude", "--db", dbPath, "--claude-config", claudeRoot, "--verbose"}, nil, &stdout, &stderr)
			if test.wantError {
				if err == nil || err.Error() != hookTestFailureMessage {
					t.Fatalf("hooks test error = %v, want %q", err, hookTestFailureMessage)
				}
			} else if err != nil {
				t.Fatalf("hooks test error = %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
			}
			output := stdout.String()
			if !strings.Contains(output, "Runtime version") || !strings.Contains(output, test.wantVersion) || !strings.Contains(output, "Runtime-version compatibility ranges are not yet encoded") {
				t.Fatalf("verbose compatibility output = %q, want version detail %q", output, test.wantVersion)
			}
			if test.wantError {
				if !strings.Contains(output, "✗ Broken") || !strings.Contains(output, "Executable") || !strings.Contains(output, "✗ Unavailable") {
					t.Fatalf("hooks test output = %q, want separate executable health failure", output)
				}
			} else if !strings.Contains(output, "- Idle") {
				t.Fatalf("hooks test output = %q, missing idle health state", output)
			}
		})
	}
}

func compatibilityLine(output, runtime string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "compatibility runtime="+runtime+" ") {
			return line
		}
	}
	return ""
}

func TestIngestConfigDoesNotCarryCompatibilityDependencies(t *testing.T) {
	resolverCalled := false
	runnerCalled := false
	options := Options{
		ConfigDir: t.TempDir(),
		VersionResolver: compatibility.ResolveFunc(func(string) (string, error) {
			resolverCalled = true
			return "/fake", nil
		}),
		VersionRunner: compatibility.RunFunc(func(context.Context, string, ...string) (string, error) {
			runnerCalled = true
			return "Claude Code 2.1.232", nil
		}),
	}
	config, err := resolveIngestConfig(options, parsedFlags{dbSet: true, dbPath: ":memory:"})
	if err != nil {
		t.Fatalf("resolveIngestConfig() error = %v", err)
	}
	if config.versionResolver != nil || config.versionRunner != nil || resolverCalled || runnerCalled {
		t.Fatalf("ingest config carried compatibility dependencies: %#v", config)
	}
}

func TestCompatibilityDiagnosticUsesNoCapturedCommandError(t *testing.T) {
	diagnostic := detectCompatibility(context.Background(), commandConfig{
		versionResolver: compatibility.ResolveFunc(func(string) (string, error) { return "/fake/claude", nil }),
		versionRunner: compatibility.RunFunc(func(context.Context, string, ...string) (string, error) {
			return "PAYLOAD_SENTINEL", errors.New("capture PAYLOAD_SENTINEL")
		}),
	}, domainRuntimeForTest())
	if diagnostic.detection.Status != compatibility.DetectionCommandFailed || diagnostic.hasEvaluation {
		t.Fatalf("diagnostic = %#v, want bounded command failure without evaluation", diagnostic)
	}
	if diagnostic.detection.Diagnostic == nil || strings.Contains(diagnostic.detection.Diagnostic.Error(), "PAYLOAD_SENTINEL") {
		t.Fatalf("diagnostic retained command error: %#v", diagnostic.detection.Diagnostic)
	}
}

func domainRuntimeForTest() domain.Runtime { return domain.RuntimeClaudeCode }
