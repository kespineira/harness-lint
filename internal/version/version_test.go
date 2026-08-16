package version

import (
	"bytes"
	"runtime/debug"
	"testing"
)

func TestCurrentDevelopmentMetadataIsSafeWithoutLinkerValues(t *testing.T) {
	oldVersion, oldCommit, oldDate := Version, Commit, BuildDate
	t.Cleanup(func() { Version, Commit, BuildDate = oldVersion, oldCommit, oldDate })
	Version, Commit, BuildDate = "", " ", ""
	oldReader := buildInfoReader
	t.Cleanup(func() { buildInfoReader = oldReader })
	buildInfoReader = func() (*debug.BuildInfo, bool) { return nil, false }

	if got, want := Current(), (Metadata{Version: DefaultVersion, Commit: DefaultCommit, BuildDate: DefaultBuildDate}); got != want {
		t.Fatalf("Current() = %#v, want %#v", got, want)
	}
}

func TestGoReleaserStyleMetadataAndOutput(t *testing.T) {
	oldVersion, oldCommit, oldDate := Version, Commit, BuildDate
	t.Cleanup(func() { Version, Commit, BuildDate = oldVersion, oldCommit, oldDate })
	Version, Commit, BuildDate = "1.2.3", "abc123", "2026-08-16T12:00:00Z"
	oldReader := buildInfoReader
	t.Cleanup(func() { buildInfoReader = oldReader })
	buildInfoReader = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "v9.9.9"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "build-info-revision"}, {Key: "vcs.time", Value: "2099-01-01T00:00:00Z"}}}, true
	}

	var output bytes.Buffer
	Print(&output, "harness-lint")
	if got, want := output.String(), "harness-lint version=1.2.3 commit=abc123 build-date=2026-08-16T12:00:00Z\n"; got != want {
		t.Fatalf("Print() = %q, want %q", got, want)
	}
}

func TestCurrentFallsBackToGoInstallBuildInfo(t *testing.T) {
	oldVersion, oldCommit, oldDate := Version, Commit, BuildDate
	t.Cleanup(func() { Version, Commit, BuildDate = oldVersion, oldCommit, oldDate })
	Version, Commit, BuildDate = DefaultVersion, DefaultCommit, DefaultBuildDate
	oldReader := buildInfoReader
	t.Cleanup(func() { buildInfoReader = oldReader })
	buildInfoReader = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Main: debug.Module{Path: "github.com/example/harness-lint", Version: "v1.2.3"},
			Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "go-install-revision"},
				{Key: "vcs.time", Value: "2026-08-16T12:00:00Z"},
				{Key: "vcs.modified", Value: "false"},
			},
		}, true
	}

	if got, want := Current(), (Metadata{Version: "1.2.3", Commit: "go-install-revision", BuildDate: "2026-08-16T12:00:00Z"}); got != want {
		t.Fatalf("Current() = %#v, want %#v", got, want)
	}
}

func TestCurrentKeepsExplicitLinkerValuesAuthoritative(t *testing.T) {
	oldVersion, oldCommit, oldDate := Version, Commit, BuildDate
	t.Cleanup(func() { Version, Commit, BuildDate = oldVersion, oldCommit, oldDate })
	Version, Commit, BuildDate = "1.2.3", "ldflags-revision", "2026-08-15T12:00:00Z"
	oldReader := buildInfoReader
	t.Cleanup(func() { buildInfoReader = oldReader })
	buildInfoReader = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "v9.9.9"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "build-info-revision"}, {Key: "vcs.time", Value: "2099-01-01T00:00:00Z"}}}, true
	}

	if got, want := Current(), (Metadata{Version: "1.2.3", Commit: "ldflags-revision", BuildDate: "2026-08-15T12:00:00Z"}); got != want {
		t.Fatalf("Current() = %#v, want %#v", got, want)
	}
}

func TestCurrentIgnoresDevelAndUnavailableBuildInfo(t *testing.T) {
	tests := []struct {
		name string
		read func() (*debug.BuildInfo, bool)
	}{
		{name: "absent", read: func() (*debug.BuildInfo, bool) { return nil, false }},
		{name: "devel module", read: func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "ignored-revision"}, {Key: "vcs.time", Value: "2099-01-01T00:00:00Z"}}}, true
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			oldVersion, oldCommit, oldDate := Version, Commit, BuildDate
			t.Cleanup(func() { Version, Commit, BuildDate = oldVersion, oldCommit, oldDate })
			Version, Commit, BuildDate = DefaultVersion, DefaultCommit, DefaultBuildDate
			oldReader := buildInfoReader
			t.Cleanup(func() { buildInfoReader = oldReader })
			buildInfoReader = test.read

			if got, want := Current(), (Metadata{Version: DefaultVersion, Commit: DefaultCommit, BuildDate: DefaultBuildDate}); got != want {
				t.Fatalf("Current() = %#v, want %#v", got, want)
			}
		})
	}
}
