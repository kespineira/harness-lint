package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/kespineira/harness-lint/internal/presentation"
	"github.com/kespineira/harness-lint/internal/store"
)

func renderDatabaseStatusView(out io.Writer, renderer presentation.HumanRenderer, verbose bool, document DatabaseStatusDocument) {
	fmt.Fprintln(out, "Database")
	fmt.Fprintln(out)
	size := "Unknown"
	if document.SizeBytes != nil {
		size = renderer.Bytes(*document.SizeBytes)
	}
	schema := fmt.Sprintf("%d (current)", document.Schema.Current)
	if document.Schema.Current != document.Schema.Latest {
		schema = fmt.Sprintf("%d (latest %d)", document.Schema.Current, document.Schema.Latest)
	}
	rows := [][]string{
		{"Path", renderer.Path(document.Path)},
		{"Size", size},
		{"Schema", schema},
		{"Events", renderer.Integer(document.UsageEventCount)},
		{"History", databaseHistoryRange(renderer, document.OldestObservedAt, document.LatestObservedAt)},
		{"Integrity", "Not checked"},
	}
	fmt.Fprintln(out, indentHumanBlock(renderer.Rows(rows), 2))
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Run `harness-lint db check` to verify integrity.")
	if verbose {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Details")
		fmt.Fprintf(out, "  Schema current  %d\n", document.Schema.Current)
		fmt.Fprintf(out, "  Schema latest   %d\n", document.Schema.Latest)
		if document.OldestObservedAt != nil {
			fmt.Fprintf(out, "  Oldest event    %s\n", *document.OldestObservedAt)
		}
		if document.LatestObservedAt != nil {
			fmt.Fprintf(out, "  Latest event    %s\n", *document.LatestObservedAt)
		}
	}
}

func renderDatabaseCheckView(out io.Writer, renderer presentation.HumanRenderer, verbose bool, document DatabaseCheckDocument) {
	fmt.Fprintln(out, "Database check")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  %s\n", databaseCheckLine(renderer, document.QuickCheck, "SQLite quick check"))
	fmt.Fprintf(out, "  %s\n", databaseCheckLine(renderer, document.ForeignKeyCheck, "Foreign keys"))
	fmt.Fprintf(out, "  %s\n", databaseCheckLine(renderer, document.Schema, "Schema"))
	fmt.Fprintln(out)
	if document.Healthy {
		fmt.Fprintln(out, "Database is healthy.")
	} else {
		fmt.Fprintln(out, "Database needs attention.")
	}
	if len(document.Issues) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Issues:")
		for _, issue := range document.Issues {
			fmt.Fprintf(out, "  %s\n", boundedDatabaseIssue(issue.Check))
		}
	}
	if verbose {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Details")
		fmt.Fprintf(out, "  Quick check       %s\n", document.QuickCheck)
		fmt.Fprintf(out, "  Foreign key check %s\n", document.ForeignKeyCheck)
		fmt.Fprintf(out, "  Schema            %s\n", document.Schema)
	}
}

func renderDatabaseBackupView(out io.Writer, renderer presentation.HumanRenderer, verbose bool, destination string, size int64) {
	fmt.Fprintln(out, "Database backup")
	fmt.Fprintln(out)
	fmt.Fprintln(out, indentHumanBlock(renderer.Rows([][]string{
		{"Destination", renderer.Path(destination)},
		{"Size", renderer.Bytes(size)},
	}), 2))
	if verbose {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "SQLite online backup; existing destinations are never overwritten.")
	}
}

func databaseHistoryRange(renderer presentation.HumanRenderer, oldest, latest *string) string {
	if oldest == nil || latest == nil || strings.TrimSpace(*oldest) == "" || strings.TrimSpace(*latest) == "" {
		return "No observations"
	}
	oldestTime, oldestErr := time.Parse(time.RFC3339Nano, *oldest)
	latestTime, latestErr := time.Parse(time.RFC3339Nano, *latest)
	if oldestErr != nil || latestErr != nil {
		return "Recorded"
	}
	format := "Jan 2"
	now := renderer.Options().Now
	if oldestTime.Year() != latestTime.Year() || (!now.IsZero() && oldestTime.Year() != now.Year()) {
		format = "Jan 2, 2006"
	}
	return oldestTime.UTC().Format(format) + " → " + latestTime.UTC().Format(format)
}

func databaseCheckLine(renderer presentation.HumanRenderer, value, label string) string {
	info := presentation.StatusInfoFor(databaseIntegrityToken(value))
	return renderer.Colorize(info.Symbol+" "+label, info.Style)
}

func databaseIntegrityToken(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(store.IntegrityOK):
		return "healthy"
	case string(store.IntegrityIssues):
		return "broken"
	case string(store.IntegrityUnavailable):
		return "unavailable"
	default:
		return "unknown"
	}
}

func boundedDatabaseIssue(value string) string {
	value = cleanText(value)
	if value == "" {
		return "unknown integrity issue"
	}
	const max = 120
	if len(value) > max {
		return value[:max] + "…"
	}
	return value
}
