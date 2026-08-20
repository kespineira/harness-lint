package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/analysis"
	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/history"
	"github.com/kespineira/harness-lint/internal/presentation"
	"github.com/kespineira/harness-lint/internal/store"
)

func TestM7ReportProgressiveDisclosureAndCanonicalAttention(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	result := analysis.Report{Capabilities: []analysis.CapabilityEvidence{
		m7Evidence("keep", analysis.KEEP, 8, now.Add(-time.Hour), domain.ScopeUser),
		m7Evidence("review-z", analysis.REVIEW, 0, time.Time{}, domain.ScopeProject),
		m7Evidence("review-a", analysis.REVIEW, 0, time.Time{}, domain.ScopeUser),
		m7Evidence("stale-z", analysis.STALE, 1, now.Add(-61*24*time.Hour), domain.ScopeProject),
		m7Evidence("stale-a", analysis.STALE, 1, now.Add(-62*24*time.Hour), domain.ScopeUser),
		m7Evidence("review-b", analysis.REVIEW, 0, time.Time{}, domain.ScopeGlobal),
		m7Evidence("review-c", analysis.REVIEW, 0, time.Time{}, domain.ScopeSession),
		m7Evidence("review-d", analysis.REVIEW, 0, time.Time{}, domain.ScopeProject),
	}, Context: analysis.ContextSummary{}}
	renderer := presentation.NewHumanRenderer(presentation.Options{Now: now, Width: 80})

	var output bytes.Buffer
	renderReportView(&output, renderer, false, false, result, nil, now, 60)
	text := output.String()
	for _, want := range []string{"Harness report", "Last 60 days", "Overview"} {
		if !strings.Contains(text, want) {
			t.Fatalf("default report missing %q: %q", want, text)
		}
	}
	var overviewHeader []string
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "Installed") {
			overviewHeader = strings.Fields(line)
			break
		}
	}
	if got, want := strings.Join(overviewHeader, " "), "Runtime Installed Used Review Stale"; got != want {
		t.Fatalf("runtime overview header = %q, want %q", got, want)
	}
	if strings.Contains(text, "As of ") {
		t.Fatalf("default report exposes exact as-of timestamp: %q", text)
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "=") {
			t.Fatalf("default report contains legacy key=value line %q: %q", line, text)
		}
	}
	if strings.Contains(text, "Evidence summary") {
		t.Fatalf("default report contains removed evidence summary block: %q", text)
	}
	attention := text[strings.Index(text, "Needs attention"):strings.Index(text, "Most used")]
	var attentionHeader []string
	for _, line := range strings.Split(attention, "\n") {
		if strings.Contains(line, "Status") {
			attentionHeader = strings.Fields(line)
			break
		}
	}
	if got, want := strings.Join(attentionHeader, " "), "Status Runtime Type Name Scope Last used"; got != want {
		t.Fatalf("attention header = %q, want %q", got, want)
	}
	if strings.Contains(attention, "Review REVIEW") {
		t.Fatalf("attention repeats human Review and raw REVIEW: %q", attention)
	}
	if !strings.Contains(attention, "stale-a") || !strings.Contains(attention, "stale-z") {
		t.Fatalf("attention = %q, want stale rows", attention)
	}
	if strings.Index(attention, "stale-a") > strings.Index(attention, "stale-z") {
		t.Fatalf("attention order = %q, want canonical stale-a before stale-z", attention)
	}
	if strings.Contains(attention, "review-d") {
		t.Fatalf("attention exceeded five rows = %q", attention)
	}
	if !strings.Contains(text, "Observation") || !strings.Contains(text, "Missing observations do not prove non-use") || !strings.Contains(text, "Explore") {
		t.Fatalf("default report omitted progressive-disclosure sections: %q", text)
	}

	output.Reset()
	renderReportView(&output, renderer, true, true, result, nil, now, 60)
	if !strings.Contains(output.String(), "As of 2026-08-20T12:00:00Z") {
		t.Fatalf("verbose report missing exact as-of timestamp: %q", output.String())
	}
	for _, want := range []string{"Capabilities (8)", "review-c", "review-d", "Exact timestamps", "Provenance", "Coverage basis"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("all verbose report missing %q: %q", want, output.String())
		}
	}
	for _, line := range strings.Split(output.String(), "\n") {
		if presentation.VisibleWidth(line) > 80 {
			t.Fatalf("report line exceeds 80 columns (%d): %q", presentation.VisibleWidth(line), line)
		}
		if strings.Contains(line, "=") {
			t.Fatalf("verbose report contains legacy key=value line %q", line)
		}
	}
}

func TestM7ExplainUnknownAndAmbiguousReturnErrors(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "state", "harness-lint.db")
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatalf("mkdir database parent: %v", err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := db.RecordInventory(context.Background(), domain.RuntimeCodex, now, []domain.Capability{
		reportTestCapability(root, "same", domain.ScopeProject),
		reportTestCapability(root, "same", domain.ScopeUser),
	}); err != nil {
		db.Close()
		t.Fatalf("record inventory: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	options := Options{Home: filepath.Join(root, "home"), CWD: root, ProjectRoot: root, Now: func() time.Time { return now }}

	for _, test := range []struct {
		name     string
		args     []string
		want     string
		guidance string
	}{
		{name: "unknown", args: []string{"explain", "missing", "--db", dbPath}, want: "was found", guidance: "harness-lint report --all"},
		{name: "ambiguous", args: []string{"explain", "same", "--db", dbPath}, want: "matches multiple", guidance: "--scope"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := ExecuteWithOptions(options, test.args, nil, &stdout, &stderr)
			if err == nil {
				t.Fatalf("explain unexpectedly succeeded: output=%q", stdout.String())
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("explain error = %v, want %q", err, test.want)
			}
			if stdout.Len() != 0 {
				t.Fatalf("explain error wrote success output to stdout: %q", stdout.String())
			}
			if !strings.Contains(err.Error(), test.guidance) {
				t.Fatalf("explain error lacks actionable guidance: %v", err)
			}
		})
	}

	var stdout, stderr bytes.Buffer
	if err := ExecuteWithOptions(options, []string{"explain", "same", "--scope", "user", "--db", dbPath}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("filtered explain error = %v\noutput=%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "Why REVIEW?") || !strings.Contains(stdout.String(), "Usage") || !strings.Contains(stdout.String(), "same") {
		t.Fatalf("filtered explain output = %q", stdout.String())
	}
}

func TestM7ReportAllIsWiredThroughTheCLI(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	var stdout, stderr bytes.Buffer
	err := ExecuteWithOptions(
		Options{Now: func() time.Time { return now }},
		[]string{"report", "--all", "--db", ":memory:", "--color", "never"},
		nil,
		&stdout,
		&stderr,
	)
	if err != nil {
		t.Fatalf("report --all error = %v\nstderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Capabilities (0)") {
		t.Fatalf("report --all output = %q", stdout.String())
	}
}

func TestM7ExplainUsesProgressiveSectionsAndEstimatedTokens(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	evidence := m7Evidence("code-reviewer", analysis.REVIEW, 0, time.Time{}, domain.ScopeUser)
	evidence.MetadataTokens = domain.Measurement{Value: 38, Confidence: domain.ConfidenceEstimated}
	evidence.BodyTokens = domain.Measurement{Value: 1600, Confidence: domain.ConfidenceEstimated}
	evidence.EffectiveCoverage = &history.EffectiveCoverage{
		Status: history.CoveragePartial,
		Intervals: []history.Interval{{
			Start: now.Add(-(39*time.Hour + 15*time.Minute)),
			End:   now,
		}},
	}
	evidence.CoverageConfidence = domain.ConfidenceUnknown
	renderer := presentation.NewHumanRenderer(presentation.Options{Now: now, Width: 80})

	var output bytes.Buffer
	renderExplainView(&output, renderer, reportCapability{evidence: evidence, scope: domain.ScopeUser}, 60, now, false)
	text := output.String()
	for _, want := range []string{
		"code-reviewer",
		"Usage",
		"Exposure",
		"Coverage",
		"Why REVIEW?",
		"Interpretation",
		"~38 tokens",
		"~1.6k tokens when loaded",
		"39h 15m",
		"Partial",
		"Confidence:      Unknown",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("explain output missing %q: %q", want, text)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if presentation.VisibleWidth(line) > 80 {
			t.Fatalf("explain line exceeds 80 columns: %q", line)
		}
		if strings.Contains(line, "=") {
			t.Fatalf("explain contains legacy key=value output: %q", line)
		}
	}
}

func TestM7ExplainParsingSelectionAndNoJSON(t *testing.T) {
	nested, args, err := parseCommandArgs("explain", []string{"lint", "--runtime", "codex", "--type", "skill", "--scope", "project"})
	if err != nil {
		t.Fatalf("parse explain args: %v", err)
	}
	if nested.explainName != "lint" || len(args) != 6 {
		t.Fatalf("nested=%#v args=%#v", nested, args)
	}
	parsed, err := parseFlags("explain", args, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parse explain flags: %v", err)
	}
	parsed.explainName = nested.explainName
	if err := validateCommandFlags("explain", parsed); err != nil {
		t.Fatalf("validate explain flags: %v", err)
	}
	if _, err := explainType("not-a-type"); err == nil {
		t.Fatal("unknown explain type unexpectedly accepted")
	}
	badJSON := parsedFlags{explainName: "lint", jsonSet: true, json: true}
	if err := validateCommandFlags("explain", badJSON); err == nil || !strings.Contains(err.Error(), "does not support --json") {
		t.Fatalf("explain JSON validation = %v", err)
	}
}

func m7Evidence(name string, classification analysis.Classification, uses int, last time.Time, scope domain.Scope) analysis.CapabilityEvidence {
	evidence := analysis.CapabilityEvidence{
		Capability: domain.Capability{
			Runtime:        domain.RuntimeCodex,
			Type:           domain.CapabilitySkill,
			Name:           name,
			Scope:          scope,
			Enabled:        domain.EnabledStateEnabled,
			Advertisement:  domain.AdvertisementStateUnknown,
			MetadataTokens: domain.Measurement{Confidence: domain.ConfidenceUnknown},
			BodyTokens:     domain.Measurement{Confidence: domain.ConfidenceUnknown},
		},
		Classification:     classification,
		InvocationCount:    uses,
		ActivityCount:      uses,
		EventCounts:        map[domain.EventType]int{domain.EventInvoked: uses},
		EvidenceCoverage:   "lifetime activity coverage is unknown",
		CoverageConfidence: domain.ConfidenceUnknown,
	}
	if !last.IsZero() {
		evidence.HasLastUsed = true
		evidence.LastUsedAt = last
	}
	return evidence
}
