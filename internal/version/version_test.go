package version

import (
	"bytes"
	"testing"
)

func TestCurrentDevelopmentMetadataIsSafeWithoutLinkerValues(t *testing.T) {
	oldVersion, oldCommit, oldDate := Version, Commit, BuildDate
	t.Cleanup(func() { Version, Commit, BuildDate = oldVersion, oldCommit, oldDate })
	Version, Commit, BuildDate = "", " ", ""

	if got, want := Current(), (Metadata{Version: DefaultVersion, Commit: DefaultCommit, BuildDate: DefaultBuildDate}); got != want {
		t.Fatalf("Current() = %#v, want %#v", got, want)
	}
}

func TestGoReleaserStyleMetadataAndOutput(t *testing.T) {
	oldVersion, oldCommit, oldDate := Version, Commit, BuildDate
	t.Cleanup(func() { Version, Commit, BuildDate = oldVersion, oldCommit, oldDate })
	Version, Commit, BuildDate = "1.2.3", "abc123", "2026-08-16T12:00:00Z"

	var output bytes.Buffer
	Print(&output, "harness-lint")
	if got, want := output.String(), "harness-lint version=1.2.3 commit=abc123 build-date=2026-08-16T12:00:00Z\n"; got != want {
		t.Fatalf("Print() = %q, want %q", got, want)
	}
}
