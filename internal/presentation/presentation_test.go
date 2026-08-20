package presentation

import (
	"strings"
	"testing"
	"time"
)

func TestFormatPrimitives(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{name: "integer", got: FormatInteger(1234567890), want: "1,234,567,890"},
		{name: "negative integer", got: FormatInteger(-1234567), want: "-1,234,567"},
		{name: "bytes", got: FormatBytes(28823552), want: "27.5 MiB"},
		{name: "exact bytes", got: FormatBytes(1023), want: "1023 B"},
		{name: "small tokens", got: FormatTokens(38), want: "~38"},
		{name: "thousands tokens", got: FormatTokens(1600), want: "~1.6k"},
		{name: "larger tokens", got: FormatTokens(88400), want: "~88.4k"},
		{name: "concise duration", got: FormatDuration(39*time.Hour + 15*time.Minute + 700*time.Millisecond), want: "39h 15m"},
		{name: "zero duration", got: FormatDuration(700 * time.Millisecond), want: "0s"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("got %q, want %q", test.got, test.want)
			}
		})
	}
}

func TestRelativeAndExactTimestamps(t *testing.T) {
	now := time.Date(2026, time.August, 20, 12, 0, 0, 123456789, time.UTC)
	if got := FormatRelativeTime(now.Add(-5*time.Minute-15*time.Second), now); got != "5m ago" {
		t.Fatalf("recent timestamp = %q, want 5m ago", got)
	}
	if got := FormatRelativeTime(now.Add(-48*time.Hour), now); got != "2d ago" {
		t.Fatalf("older recent timestamp = %q, want 2d ago", got)
	}
	if got := FormatRelativeTime(now.Add(-8*24*time.Hour), now); got != "Aug 12, 12:00" {
		t.Fatalf("older timestamp = %q, want Aug 12, 12:00", got)
	}
	if got := FormatRelativeTime(now.Add(2*time.Hour), now); got != "in 2h" {
		t.Fatalf("future timestamp = %q, want in 2h", got)
	}

	value := time.Date(2026, time.August, 20, 14, 30, 0, 123456789, time.FixedZone("CEST", 2*60*60))
	if got := FormatTimestamp(value); got != "2026-08-20T12:30:00.123456789Z" {
		t.Fatalf("exact timestamp = %q, want UTC RFC3339Nano", got)
	}
}

func TestRendererUsesInjectedClockAndHome(t *testing.T) {
	now := time.Date(2026, time.August, 20, 12, 0, 0, 0, time.UTC)
	renderer := NewHumanRenderer(Options{
		Now:     now,
		HomeDir: "/Users/alice",
		Width:   80,
	})
	if got := renderer.RelativeTime(now.Add(-time.Hour)); got != "1h ago" {
		t.Fatalf("injected relative time = %q, want 1h ago", got)
	}
	if got := renderer.Path("/Users/alice/project/file"); got != "~/project/file" {
		t.Fatalf("contracted path = %q, want ~/project/file", got)
	}
}

func TestContractHomePathOnlyForDescendants(t *testing.T) {
	const home = "/Users/alice"
	tests := map[string]string{
		"/Users/alice":          "~",
		"/Users/alice/project":  "~/project",
		"/Users/alice-old/file": "/Users/alice-old/file",
		"/Users/bob/file":       "/Users/bob/file",
		"relative/file":         "relative/file",
	}
	for input, want := range tests {
		if got := ContractHomePath(input, home); got != want {
			t.Fatalf("ContractHomePath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestLabelsAndStatuses(t *testing.T) {
	if got := RuntimeLabel("claude-code"); got != "Claude Code" {
		t.Fatalf("runtime label = %q, want Claude Code", got)
	}
	if got := CapabilityTypeLabel("mcp_tool"); got != "MCP tool" {
		t.Fatalf("capability type label = %q, want MCP tool", got)
	}
	if got := RenderStatus("healthy", false); got != "✓ Healthy" {
		t.Fatalf("status = %q, want ✓ Healthy", got)
	}
	if got := RenderStatus("STALE", false); got != "! Stale" {
		t.Fatalf("stale status = %q, want ! Stale", got)
	}
	if got := RenderStatus("idle", false); got != "- Idle" {
		t.Fatalf("idle status = %q, want - Idle", got)
	}
	if got := StripANSI(RenderStatus("healthy", true)); got != "✓ Healthy" {
		t.Fatalf("colored status meaning = %q, want ✓ Healthy", got)
	}
}

func TestANSIOnAndOff(t *testing.T) {
	plain := Colorize("OK", StyleSuccess, false)
	if plain != "OK" {
		t.Fatalf("disabled color = %q, want exact plain text", plain)
	}
	colored := Colorize("OK", StyleSuccess, true)
	if colored != "\x1b[32mOK\x1b[0m" {
		t.Fatalf("enabled color = %q, want green SGR", colored)
	}
	if StripANSI(colored) != plain {
		t.Fatalf("stripped colored text = %q, want %q", StripANSI(colored), plain)
	}
}

func TestKeyValuesGolden(t *testing.T) {
	got := RenderKeyValues([]KeyValue{
		{Key: "runtime", Value: "Codex"},
		{Key: "type", Value: "MCP server"},
		{Key: "name", Value: "filesystem"},
	}, 80)
	want := "runtime: Codex\ntype:    MCP server\nname:    filesystem"
	if got != want {
		t.Fatalf("key/value output =\n%s\nwant =\n%s", got, want)
	}
}

func TestTableGoldenAndWrapping(t *testing.T) {
	got := RenderTable(
		[]string{"Runtime", "Type"},
		[][]string{{"Codex", "Skill"}, {"Claude Code", "MCP tool"}},
		80,
	)
	want := "Runtime      Type\nCodex        Skill\nClaude Code  MCP tool"
	if got != want {
		t.Fatalf("table output =\n%s\nwant =\n%s", got, want)
	}

	identity := strings.Repeat("identity", 14)
	wrapped := WrapText(identity, 80)
	if len(wrapped) < 2 {
		t.Fatalf("long identity was not wrapped: %q", wrapped)
	}
	if strings.Join(wrapped, "") != identity {
		t.Fatalf("wrapped identity changed: got %q, want original identity", strings.Join(wrapped, ""))
	}
	for _, line := range wrapped {
		if VisibleWidth(line) > 80 {
			t.Fatalf("wrapped line width = %d, want <= 80: %q", VisibleWidth(line), line)
		}
	}
}

func TestTableKeeps80ColumnsAndIdentity(t *testing.T) {
	identity := strings.Repeat("capability-name-", 10)
	output := RenderTable([]string{"Name"}, [][]string{{identity}}, 80)
	if !strings.Contains(strings.ReplaceAll(output, "\n", ""), identity) {
		t.Fatal("80-column table silently truncated the identity")
	}
	for _, line := range strings.Split(output, "\n") {
		if VisibleWidth(line) > 80 {
			t.Fatalf("table line width = %d, want <= 80: %q", VisibleWidth(line), line)
		}
	}
}

func TestWrapDoesNotStrandAFittingFinalWord(t *testing.T) {
	input := "Lifetime coverage remains unknown unless a positive capture/presence intersection is shown."
	got := Wrap(input, 78)
	if strings.HasSuffix(got, "intersection is\nshown.") {
		t.Fatalf("Wrap stranded a fitting final word: %q", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if VisibleWidth(line) > 78 {
			t.Fatalf("wrapped line exceeds width: %q", line)
		}
	}
}
