package cli

import (
	"bytes"
	"strings"
	"testing"

	buildversion "github.com/kespineira/harness-lint/internal/version"
)

func TestExecuteVersionUsesBuildMetadata(t *testing.T) {
	oldVersion, oldCommit, oldDate := buildversion.Version, buildversion.Commit, buildversion.BuildDate
	t.Cleanup(func() {
		buildversion.Version, buildversion.Commit, buildversion.BuildDate = oldVersion, oldCommit, oldDate
	})
	buildversion.Version, buildversion.Commit, buildversion.BuildDate = "1.2.3", "abc123", "2026-08-16T12:00:00Z"

	var stdout, stderr bytes.Buffer
	if err := ExecuteWithOptions(Options{}, []string{"version"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("version error = %v", err)
	}
	if got, want := stdout.String(), "harness-lint version=1.2.3 commit=abc123 build-date=2026-08-16T12:00:00Z\n"; got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
}

func TestExecuteVersionDevelopmentOutputAndHelpAreDeterministic(t *testing.T) {
	oldVersion, oldCommit, oldDate := buildversion.Version, buildversion.Commit, buildversion.BuildDate
	t.Cleanup(func() {
		buildversion.Version, buildversion.Commit, buildversion.BuildDate = oldVersion, oldCommit, oldDate
	})
	buildversion.Version, buildversion.Commit, buildversion.BuildDate = "", "", ""

	var stdout, stderr bytes.Buffer
	if err := ExecuteWithOptions(Options{}, []string{"--version"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("global version error = %v", err)
	}
	if got, want := stdout.String(), "harness-lint version=0.0.0-dev commit=unknown build-date=unknown\n"; got != want {
		t.Fatalf("development version output = %q, want %q", got, want)
	}
	stdout.Reset()
	if err := ExecuteWithOptions(Options{}, []string{"version", "--help"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("version help error = %v", err)
	}
	for _, want := range []string{"usage: harness-lint version", "semantic version", "commit", "build date"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("version help = %q, missing %q", stdout.String(), want)
		}
	}
}

func TestExecuteVersionRejectsFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := ExecuteWithOptions(Options{}, []string{"version", "--db", ":memory:"}, nil, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "version does not accept options") {
		t.Fatalf("version with flag error = %v, want option rejection", err)
	}
	if err := ExecuteWithOptions(Options{}, []string{"--version", "version"}, nil, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "--version does not accept arguments") {
		t.Fatalf("global version with arguments error = %v, want argument rejection", err)
	}
}
