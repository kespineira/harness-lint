// Package presentation contains the small, deterministic formatting boundary
// shared by human-facing command views.  It intentionally knows nothing about
// commands, persistence, or analysis policy.
package presentation

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// DefaultWidth is deliberately fixed.  Human output should remain readable
// when it is redirected, captured in a test, or displayed in a narrow shell;
// this package does not try to be a terminal-width engine.
const DefaultWidth = 80

// Options controls presentation without coupling a view to process-global
// state.  Now follows the callback-injection convention used by the CLI.  A
// fixed NowTime is also available for callers that already own a timestamp;
// Now takes precedence over NowTime.
type Options struct {
	Color   bool
	Width   int
	HomeDir string
	Now     time.Time
}

// HumanRenderer is the narrow boundary intended for future command views.
// It is safe to copy; all options are value-like and formatting has no hidden
// mutable state.
type HumanRenderer struct {
	options Options
}

// NewHumanRenderer creates a renderer with the supplied options.
func NewHumanRenderer(options Options) HumanRenderer {
	return HumanRenderer{options: options}
}

// New is a concise alias for NewHumanRenderer.
func New(options Options) HumanRenderer {
	return NewHumanRenderer(options)
}

// DefaultRenderer creates a renderer with deterministic layout defaults and
// process-local time/home resolution only when those values are requested.
func DefaultRenderer() HumanRenderer {
	return NewHumanRenderer(Options{})
}

func (r HumanRenderer) width() int {
	if r.options.Width > 0 {
		return r.options.Width
	}
	return DefaultWidth
}

func (r HumanRenderer) now() time.Time {
	if !r.options.Now.IsZero() {
		return r.options.Now
	}
	return time.Now()
}

func (r HumanRenderer) homeDir() string {
	if r.options.HomeDir != "" {
		return r.options.HomeDir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// Integer formats an integer with comma group separators.
func (r HumanRenderer) Integer(value int64) string {
	return FormatInteger(value)
}

// Int is an alias for Integer.
func (r HumanRenderer) Int(value int64) string {
	return r.Integer(value)
}

// Bytes formats a byte count using IEC units.
func (r HumanRenderer) Bytes(value int64) string {
	return FormatBytes(value)
}

// Tokens formats an estimated token count with the required leading tilde.
func (r HumanRenderer) Tokens(value int64) string {
	return FormatTokens(value)
}

// Duration formats a duration without sub-second noise.
func (r HumanRenderer) Duration(value time.Duration) string {
	return FormatDuration(value)
}

// RelativeTime formats a timestamp relative to the renderer's injected clock.
func (r HumanRenderer) RelativeTime(value time.Time) string {
	return FormatRelativeTime(value, r.now())
}

// RelativeTimestamp is an alias for RelativeTime.
func (r HumanRenderer) RelativeTimestamp(value time.Time) string {
	return r.RelativeTime(value)
}

// Timestamp formats an exact UTC RFC3339Nano timestamp.
func (r HumanRenderer) Timestamp(value time.Time) string {
	return FormatTimestamp(value)
}

// ExactTimestamp is an explicit companion alias for Timestamp.
func (r HumanRenderer) ExactTimestamp(value time.Time) string {
	return r.Timestamp(value)
}

// Path contracts the actual configured home directory to ~ when appropriate.
func (r HumanRenderer) Path(value string) string {
	return ContractHomePath(value, r.homeDir())
}

// Runtime returns a human runtime label.
func (r HumanRenderer) Runtime(value string) string {
	return RuntimeLabel(value)
}

// Type returns a human capability-type label.
func (r HumanRenderer) Type(value string) string {
	return CapabilityTypeLabel(value)
}

// Status returns a semantic status representation with optional ANSI color.
// The symbol and word are always present, including when color is disabled.
func (r HumanRenderer) Status(value string) string {
	return RenderStatus(value, r.options.Color)
}

// Colorize applies an ANSI style only when the caller explicitly enables
// color.  The text passed to Colorize should remain meaningful on its own.
func (r HumanRenderer) Colorize(value string, style Style) string {
	return Colorize(value, style, r.options.Color)
}

// Wrap wraps text at the renderer's conservative width.
func (r HumanRenderer) Wrap(value string) string {
	return Wrap(value, r.width())
}

// KeyValues renders whitespace-aligned key/value lines at the renderer's
// conservative width.
func (r HumanRenderer) KeyValues(values []KeyValue) string {
	return RenderKeyValues(values, r.width())
}

// Rows renders whitespace-aligned rows at the renderer's conservative width.
func (r HumanRenderer) Rows(rows [][]string) string {
	return RenderRows(rows, r.width())
}

// Table renders an optional header followed by whitespace-aligned rows.
func (r HumanRenderer) Table(headers []string, rows [][]string) string {
	return RenderTable(headers, rows, r.width())
}

// String allows a renderer to be logged without exposing implementation
// details.  It is intentionally terse because the renderer itself is not
// human output.
func (r HumanRenderer) String() string {
	return fmt.Sprintf("HumanRenderer(width=%d,color=%t)", r.width(), r.options.Color)
}

// Options returns a copy of the renderer options for callers that need to
// derive a closely related view.
func (r HumanRenderer) Options() Options {
	return r.options
}

// NormalizeStatusKey makes status matching deterministic across the common
// snake_case, kebab-case, and uppercase forms used by domain packages.
func NormalizeStatusKey(value string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "-", "_"))
}
