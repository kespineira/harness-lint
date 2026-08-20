package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/analysis"
	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/history"
	"github.com/kespineira/harness-lint/internal/presentation"
)

func TestM7HumanViewsFixedClockGoldenShape(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	renderer := presentation.NewHumanRenderer(presentation.Options{Now: now, Width: 80})

	var contextOutput bytes.Buffer
	renderContextView(&contextOutput, renderer, analysis.ContextSummary{Groups: []analysis.ContextGroup{
		{
			Runtime:         domain.RuntimeCodex,
			CapabilityType:  domain.CapabilitySkill,
			CapabilityCount: 2,
			MetadataTokens: analysis.MeasurementSummary{
				Value: 12, Confidence: domain.ConfidenceExact, KnownCount: 1,
			},
			BodyTokens: analysis.MeasurementSummary{
				Value: 34, Confidence: domain.ConfidenceEstimated, KnownCount: 1,
			},
		},
		{
			Runtime:         domain.RuntimeCodex,
			CapabilityType:  domain.CapabilityMCPServer,
			CapabilityCount: 1,
			MetadataTokens:  analysis.MeasurementSummary{UnknownCount: 1},
			BodyTokens:      analysis.MeasurementSummary{UnknownCount: 1},
		},
	}})
	contextText := contextOutput.String()
	for _, want := range []string{
		"Context footprint",
		"Codex",
		"Skill (2)",
		"Configured baseline metadata",
		"On-load body estimate",
		"~12 tokens",
		"~34 tokens",
		"Caveats",
		"MCP schema cost is unknown",
		"2 token measurements are unknown",
	} {
		if !strings.Contains(contextText, want) {
			t.Fatalf("context output = %q, missing %q", contextText, want)
		}
	}
	if got := strings.Count(contextText, "MCP schema cost"); got != 1 {
		t.Fatalf("context caveat count = %d, want one caveat: %q", got, contextText)
	}

	lastAlpha := now.Add(-2 * time.Hour)
	lastBeta := now.Add(-30 * time.Minute)
	monthly := []history.MonthlyAggregate{
		{Month: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), Runtime: domain.RuntimeCodex, CapabilityType: domain.CapabilitySkill, CapabilityName: "alpha", Uses: 3, DistinctInvocationSessions: 1},
		{Month: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), Runtime: domain.RuntimeCodex, CapabilityType: domain.CapabilitySkill, CapabilityName: "beta", Uses: 4, DistinctInvocationSessions: 1},
	}
	var usageOutput bytes.Buffer
	renderUsageView(&usageOutput, renderer, usageView{
		now: now, days: 30, runtimeSet: true, runtimeFilter: domain.RuntimeCodex,
		aggregates: []history.Aggregate{
			{
				Runtime: domain.RuntimeCodex, CapabilityType: domain.CapabilitySkill, CapabilityName: "alpha", Uses: 12,
				DistinctInvocationSessions: 3, LastObservedAt: &lastAlpha,
				AdvertisedObservations: 4, LoadedObservations: 5,
				InvocationEvidence: map[domain.Provenance]int64{domain.ProvenanceHook: 4, domain.ProvenanceImport: 8},
			},
			{
				Runtime: domain.RuntimeClaudeCode, CapabilityType: domain.CapabilityTool, CapabilityName: "beta", Uses: 2,
				DistinctInvocationSessions: 1, LastObservedAt: &lastBeta,
				InvocationEvidence: map[domain.Provenance]int64{domain.ProvenanceTranscript: 2},
			},
		},
		monthly:        monthly,
		includeMonthly: true,
	})
	usageText := usageOutput.String()
	for _, want := range []string{"Usage", "Last 30 days", "Filters: Codex", "Capabilities ranked by uses", "Capability", "Uses", "2h ago", "Observation totals", "Invocation evidence", "Monthly usage (UTC)", "2026-08"} {
		if !strings.Contains(usageText, want) {
			t.Fatalf("usage output = %q, missing %q", usageText, want)
		}
	}
	if strings.Index(usageText, "alpha") > strings.Index(usageText, "beta") {
		t.Fatalf("usage ranking does not lead with highest use: %q", usageText)
	}
	monthlyText := usageText[strings.Index(usageText, "Monthly usage (UTC)"):]
	if !strings.Contains(monthlyText, "7") || strings.Contains(monthlyText, "Sessions") {
		t.Fatalf("monthly table is not compact uses-only output: %q", monthlyText)
	}

	staleEvidence := m7Evidence("old-skill", analysis.STALE, 1, now.Add(-61*24*time.Hour), domain.ScopeProject)
	firstObserved := now.Add(-62 * 24 * time.Hour)
	lastObserved := now.Add(-61 * 24 * time.Hour)
	staleEvidence.FirstObservedAt = &firstObserved
	staleEvidence.LastObservedAt = &lastObserved
	staleEvidence.FirstEffectiveActivityAt = &firstObserved
	staleEvidence.LastEffectiveActivityAt = &lastObserved
	reviewEvidence := m7Evidence("inspect-skill", analysis.REVIEW, 0, time.Time{}, domain.ScopeUser)
	var staleOutput bytes.Buffer
	renderStaleView(&staleOutput, renderer, false, analysis.Report{Capabilities: []analysis.CapabilityEvidence{staleEvidence, reviewEvidence}}, now, 60)
	staleText := staleOutput.String()
	if !strings.Contains(staleText, "Stale capabilities") || !strings.Contains(staleText, "old-skill") || strings.Contains(staleText, "inspect-skill") || strings.Contains(staleText, "=") {
		t.Fatalf("stale output = %q, want only STALE human rows", staleText)
	}
	var verboseStale bytes.Buffer
	renderStaleView(&verboseStale, renderer, true, analysis.Report{Capabilities: []analysis.CapabilityEvidence{staleEvidence}}, now, 60)
	if !strings.Contains(verboseStale.String(), "Exact evidence") || !strings.Contains(verboseStale.String(), "2026-") {
		t.Fatalf("verbose stale output = %q, want exact evidence", verboseStale.String())
	}
	var reviewOnlyStale bytes.Buffer
	renderStaleView(&reviewOnlyStale, renderer, false, analysis.Report{Capabilities: []analysis.CapabilityEvidence{reviewEvidence}}, now, 60)
	if !strings.Contains(reviewOnlyStale.String(), "No capabilities are currently classified STALE.") || !strings.Contains(reviewOnlyStale.String(), "REVIEW is not evidence of staleness") {
		t.Fatalf("review-only stale output = %q, want explicit REVIEW distinction without item rows", reviewOnlyStale.String())
	}
	if strings.Contains(reviewOnlyStale.String(), "inspect-skill") {
		t.Fatalf("review-only stale output listed a non-stale item: %q", reviewOnlyStale.String())
	}

	var doctorOutput bytes.Buffer
	renderDoctorView(&doctorOutput, renderer, false, []doctorRuntimeView{{runtime: domain.RuntimeCodex}})
	if !strings.Contains(doctorOutput.String(), "✓ Healthy") || !strings.Contains(doctorOutput.String(), "No configuration problems found.") {
		t.Fatalf("clean doctor output = %q", doctorOutput.String())
	}
	var emptyUsage bytes.Buffer
	renderUsageView(&emptyUsage, renderer, usageView{now: now, days: 30})
	if !strings.Contains(emptyUsage.String(), "No usage observations were found in this period.") {
		t.Fatalf("empty usage output = %q, want explicit empty state", emptyUsage.String())
	}
	var statusOutput bytes.Buffer
	renderDoctorView(&statusOutput, renderer, false, []doctorRuntimeView{
		{runtime: domain.RuntimeClaudeCode, discoveryUnavailable: true},
		{runtime: domain.RuntimeCodex, findings: []domain.Finding{{Runtime: domain.RuntimeCodex, Code: "warning", Message: "diagnostic", Severity: domain.SeverityWarning, Confidence: domain.ConfidenceObserved}}},
	})
	for _, want := range []string{"✗ Unavailable", "! Needs attention"} {
		if !strings.Contains(statusOutput.String(), want) {
			t.Fatalf("doctor status output = %q, missing %q", statusOutput.String(), want)
		}
	}

	for name, value := range map[string]string{
		"context": contextText,
		"usage":   usageText,
		"empty":   emptyUsage.String(),
		"stale":   staleText,
		"doctor":  doctorOutput.String(),
		"status":  statusOutput.String(),
	} {
		for _, line := range strings.Split(value, "\n") {
			if presentation.VisibleWidth(line) > 80 {
				t.Fatalf("%s line exceeds 80 columns (%d): %q", name, presentation.VisibleWidth(line), line)
			}
		}
	}
}

func TestM7VerboseParsingAndPrivacyBoundaries(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "stale verbose", args: []string{"stale", "--db", ":memory:", "--verbose"}, want: "Stale capabilities"},
		{name: "doctor verbose", args: []string{"doctor", "--verbose"}, want: "Compatibility"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := ExecuteWithOptions(Options{Home: filepath.Join(root, "home"), CWD: root, ProjectRoot: root, Now: func() time.Time { return now }}, test.args, nil, &stdout, &stderr)
			if err != nil {
				t.Fatalf("%s error = %v\nstdout=%s\nstderr=%s", test.name, err, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String(), test.want) {
				t.Fatalf("%s output = %q, missing %q", test.name, stdout.String(), test.want)
			}
		})
	}
	var defaultDoctorOut, defaultDoctorErr bytes.Buffer
	if err := ExecuteWithOptions(Options{Home: filepath.Join(root, "home"), CWD: root, ProjectRoot: root, Now: func() time.Time { return now }}, []string{"doctor"}, nil, &defaultDoctorOut, &defaultDoctorErr); err != nil {
		t.Fatalf("default doctor error = %v", err)
	}
	if strings.Contains(defaultDoctorOut.String(), "Compatibility") {
		t.Fatalf("default doctor leaked compatibility details: %q", defaultDoctorOut.String())
	}

	for _, command := range []string{"context", "usage"} {
		var stdout, stderr bytes.Buffer
		err := ExecuteWithOptions(Options{CWD: root, Now: func() time.Time { return now }}, []string{command, "--verbose"}, nil, &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), "--verbose is not supported") {
			t.Fatalf("%s verbose validation = %v, want unsupported error", command, err)
		}
	}

	var doctor bytes.Buffer
	renderDoctorView(&doctor, presentation.NewHumanRenderer(presentation.Options{Now: now, Width: 80}), false, []doctorRuntimeView{{
		runtime: domain.RuntimeCodex,
		findings: []domain.Finding{{
			Runtime: domain.RuntimeCodex, Code: "malformed-agent-toml", Message: "bounded diagnostic", Severity: domain.SeverityWarning, Confidence: domain.ConfidenceObserved,
		}},
	}})
	if strings.Contains(doctor.String(), root) || strings.Contains(doctor.String(), "session-id") || strings.Contains(doctor.String(), "payload") {
		t.Fatalf("doctor output leaked private data: %q", doctor.String())
	}
}

func TestM7DoctorDuplicateFindingDoesNotPrintDefinitionPaths(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "private-left", "SKILL.md")
	right := filepath.Join(root, "private-right", "SKILL.md")
	view := doctorRuntimeView{
		runtime: domain.RuntimeCodex,
		duplicates: []analysis.DuplicateName{{
			Runtime: domain.RuntimeCodex, CapabilityType: domain.CapabilitySkill, Name: "shared", Definitions: []domain.Capability{{Source: left}, {Source: right}},
		}},
	}
	renderer := presentation.NewHumanRenderer(presentation.Options{Width: 80, HomeDir: root})
	var output bytes.Buffer
	renderDoctorView(&output, renderer, false, []doctorRuntimeView{view})
	if strings.Contains(output.String(), left) || strings.Contains(output.String(), right) || !strings.Contains(output.String(), "2 definitions") {
		t.Fatalf("doctor duplicate privacy output = %q", output.String())
	}
	var verbose bytes.Buffer
	renderDoctorView(&verbose, renderer, true, []doctorRuntimeView{view})
	if !strings.Contains(verbose.String(), "~/private-left/SKILL.md") || !strings.Contains(verbose.String(), "~/private-right/SKILL.md") {
		t.Fatalf("verbose doctor output omitted actionable definition sources: %q", verbose.String())
	}
}

func TestM7VerboseRealCLIFlagsDoNotCreateStaleDatabaseForDoctor(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "missing", "doctor.db")
	options := Options{Home: filepath.Join(root, "home"), CWD: root, ProjectRoot: root, Now: func() time.Time {
		return time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	}}
	var stdout, stderr bytes.Buffer
	if err := ExecuteWithOptions(options, []string{"doctor", "--verbose", "--db", dbPath}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("doctor with ignored db path = %v", err)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("doctor created database = %v", err)
	}
}
