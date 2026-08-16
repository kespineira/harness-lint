// Package version contains the build metadata exposed by harness-lint.
//
// The defaults intentionally live in Go source so development, source, and
// go install builds do not depend on generated files. Release tooling may
// replace these variables with linker flags.
package version

import (
	"fmt"
	"io"
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

// Metadata is the normalized build information shown by the version command.
type Metadata struct {
	Version   string
	Commit    string
	BuildDate string
}

// Current returns build metadata, replacing empty linker values with safe
// development defaults.
func Current() Metadata {
	return Metadata{
		Version:   fallback(Version, DefaultVersion),
		Commit:    fallback(Commit, DefaultCommit),
		BuildDate: fallback(BuildDate, DefaultBuildDate),
	}
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
