// Package version contains the build metadata exposed by harness-lint.
//
// The defaults intentionally live in Go source so development, source, and
// go install builds do not depend on generated files. Release tooling may
// replace these variables with linker flags.
package version

import (
	"fmt"
	"io"
	"runtime/debug"
	"strings"
)

const (
	DefaultVersion   = "0.0.0-dev"
	DefaultCommit    = "unknown"
	DefaultBuildDate = "unknown"
)

// These variables are the linker targets used by release tooling. GoReleaser
// can set them with -X github.com/kespineira/harness-lint/internal/version.<name>=... .
var (
	Version   = DefaultVersion
	Commit    = DefaultCommit
	BuildDate = DefaultBuildDate
)

// buildInfoReader is a seam around runtime/debug.ReadBuildInfo so callers and
// tests can deterministically model binaries built by go install or locally.
var buildInfoReader = debug.ReadBuildInfo

// Metadata is the normalized build information shown by the version command.
type Metadata struct {
	Version   string
	Commit    string
	BuildDate string
}

// Current returns build metadata. Release linker values take precedence over
// runtime build information; when the version is still a development default,
// go install metadata is used where it provides non-development values.
func Current() Metadata {
	metadata := Metadata{
		Version:   fallback(Version, DefaultVersion),
		Commit:    fallback(Commit, DefaultCommit),
		BuildDate: fallback(BuildDate, DefaultBuildDate),
	}
	if !isDevelopmentVersion(Version) {
		return metadata
	}
	info, ok := buildInfoReader()
	if !ok || info == nil {
		return metadata
	}
	moduleVersion := displayVersion(info.Main.Version)
	if moduleVersion == "" {
		return metadata
	}
	metadata.Version = moduleVersion
	settings := buildSettings(info.Settings)
	if isDevelopmentValue(Commit) {
		metadata.Commit = buildInfoFallback(settings["vcs.revision"], DefaultCommit)
	}
	if isDevelopmentValue(BuildDate) {
		metadata.BuildDate = buildInfoFallback(settings["vcs.time"], DefaultBuildDate)
	}
	return metadata
}

func isDevelopmentVersion(value string) bool {
	trimmed := strings.TrimSpace(value)
	return isDevelopmentValue(trimmed) || trimmed == DefaultVersion
}

func isDevelopmentValue(value string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed == "" || trimmed == DefaultCommit || trimmed == DefaultBuildDate || isDevel(trimmed)
}

func isDevel(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "devel", "(devel)":
		return true
	default:
		return false
	}
}

func displayVersion(value string) string {
	trimmed := strings.TrimSpace(value)
	trimmed = strings.TrimPrefix(trimmed, "v")
	if trimmed == "" || isDevel(trimmed) {
		return ""
	}
	return trimmed
}

func buildInfoFallback(value, defaultValue string) string {
	if strings.TrimSpace(value) == "" || isDevel(value) {
		return defaultValue
	}
	return value
}

func buildSettings(settings []debug.BuildSetting) map[string]string {
	values := make(map[string]string, len(settings))
	for _, setting := range settings {
		if strings.TrimSpace(setting.Key) == "" || setting.Value == "" {
			continue
		}
		values[setting.Key] = setting.Value
	}
	return values
}

func fallback(value, defaultValue string) string {
	if strings.TrimSpace(value) == "" {
		return defaultValue
	}
	return value
}

// Format returns the stable, single-line version representation used by the
// CLI and suitable for logs and support output.
func (m Metadata) Format(binaryName string) string {
	return fmt.Sprintf("%s version=%s commit=%s build-date=%s", binaryName, m.Version, m.Commit, m.BuildDate)
}

// Print writes the stable version representation followed by a newline.
func Print(w io.Writer, binaryName string) {
	fmt.Fprintln(w, Current().Format(binaryName))
}
