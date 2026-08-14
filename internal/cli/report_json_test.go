package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
	reportdto "github.com/kespineira/harness-lint/internal/report"
	"github.com/kespineira/harness-lint/internal/store"
)

func TestReportAndStaleJSONExposeStableSafeUsageEvidence(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "state", "harness-lint.db")
	now := time.Date(2026, 8, 13, 15, 0, 0, 0, time.UTC)
	capability := reportTestCapability(root, "mixed", domain.ScopeProject)
	duplicate := reportTestCapability(root, "mixed", domain.ScopeUser)
	duplicate.Source = filepath.Join(root, "another", "definition.md")
	capability.MetadataTokens = domain.Measurement{Value: 2000, Confidence: domain.ConfidenceEstimated, Basis: "manifest at " + filepath.Join(root, "private", "manifest.json")}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatalf("mkdir database parent: %v", err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := db.RecordInventory(context.Background(), domain.RuntimeCodex, now, []domain.Capability{capability, duplicate}); err != nil {
		t.Fatalf("record inventory: %v", err)
	}
	sourceTime := now.Add(-4 * time.Hour)
	events := []domain.UsageEvent{
		reportTestEvent(now.Add(-3*time.Hour), nil, "mixed", domain.EventAdvertised, domain.ProvenanceImport, "advertised-session"),
		reportTestEvent(now.Add(-2*time.Hour), nil, "mixed", domain.EventLoaded, domain.ProvenanceTranscript, "loaded-session"),
		reportTestEvent(now.Add(-time.Hour), &sourceTime, "mixed", domain.EventInvoked, domain.ProvenanceHook, "secret-session-a"),
		reportTestEvent(now.Add(-30*time.Minute), nil, "mixed", domain.EventInvoked, domain.ProvenanceTranscript, "secret-session-b"),
	}
	for index := range events {
		events[index].SourceIdentity = "opaque-source-identity"
		events[index].Fingerprint = strings.Repeat("f", 64)
	}
	if err := db.InsertUsageEvents(context.Background(), events); err != nil {
		t.Fatalf("insert usage events: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	options := Options{Home: filepath.Join(root, "home"), CWD: root, ProjectRoot: root, Now: func() time.Time { return now }}
	jsonOutput := func(command string) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if err := ExecuteWithOptions(options, []string{command, "--json", "--db", dbPath, "--days", "60"}, nil, &stdout, &stderr); err != nil {
			t.Fatalf("%s JSON error = %v\nstderr=%s", command, err, stderr.String())
		}
		return stdout.String()
	}

	first := jsonOutput("report")
	second := jsonOutput("report")
	if first != second {
		t.Fatalf("deterministic report JSON differs:\nfirst=%s\nsecond=%s", first, second)
	}
	var report reportdto.ReportDocument
	if err := json.Unmarshal([]byte(first), &report); err != nil {
		t.Fatalf("decode report JSON: %v\noutput=%s", err, first)
	}
	if report.SchemaVersion != reportdto.SchemaVersion || report.GeneratedAt != now.Format(time.RFC3339Nano) {
		t.Fatalf("report envelope = %#v, want schema 1 and injected generated_at", report)
	}
	if !strings.Contains(first, `"no_activity_observed"`) || strings.Contains(first, `"never_observed"`) {
		t.Fatalf("report runtime activity naming is not stable/precise: %s", first)
	}
	if len(report.Runtimes) != 2 || len(report.Capabilities) != 2 || len(report.UsageOnly) != 0 || len(report.Findings) != 1 {
		t.Fatalf("report collection sizes = runtimes=%d capabilities=%d usage-only=%d findings=%d", len(report.Runtimes), len(report.Capabilities), len(report.UsageOnly), len(report.Findings))
	}
	capabilityJSON := report.Capabilities[0]
	if capabilityJSON.InvocationCount != 2 || capabilityJSON.DistinctSessionCount != 2 || capabilityJSON.Advertised != 1 || capabilityJSON.Loaded != 1 {
		t.Fatalf("capability usage fields = %#v", capabilityJSON)
	}
	if got, want := capabilityJSON.EvidenceSources, []string{"hook", "transcript"}; !equalStrings(got, want) {
		t.Fatalf("evidence sources = %#v, want %#v", got, want)
	}
	if capabilityJSON.FirstObservedAt == nil || capabilityJSON.LastObservedAt == nil || capabilityJSON.FirstInvocationEffectiveAt == nil || capabilityJSON.LastInvocationEffectiveAt == nil {
		t.Fatalf("report timestamps = %#v, want observed and effective activity timestamps", capabilityJSON)
	}
	if report.Findings[0].Code != "duplicate-capability" || report.Findings[0].Definitions != 2 {
		t.Fatalf("duplicate finding = %#v", report.Findings[0])
	}
	for _, forbidden := range []string{root, "secret-session-a", "secret-session-b", "opaque-source-identity", strings.Repeat("f", 64), "never used"} {
		if strings.Contains(first, forbidden) {
			t.Fatalf("report JSON contains private or misleading text %q: %s", forbidden, first)
		}
	}
	staleOutput := jsonOutput("stale")
	var stale reportdto.StaleDocument
	if err := json.Unmarshal([]byte(staleOutput), &stale); err != nil {
		t.Fatalf("decode stale JSON: %v\noutput=%s", err, staleOutput)
	}
	if stale.SchemaVersion != reportdto.SchemaVersion || stale.GeneratedAt != now.Format(time.RFC3339Nano) || len(stale.Capabilities) != 2 || len(stale.Findings) != 1 {
		t.Fatalf("stale JSON envelope/collections = %#v", stale)
	}
	if strings.Contains(staleOutput, "report as-of=") || strings.Contains(staleOutput, "capabilities:") {
		t.Fatalf("stale JSON contains terminal decoration: %s", staleOutput)
	}

	var human, stderr bytes.Buffer
	if err := ExecuteWithOptions(options, []string{"report", "--db", dbPath, "--days", "60"}, nil, &human, &stderr); err != nil {
		t.Fatalf("human report error = %v\nstderr=%s", err, stderr.String())
	}
	for _, forbidden := range []string{root, "secret-session-a", "secret-session-b", "opaque-source-identity", strings.Repeat("f", 64), "never used"} {
		if strings.Contains(human.String(), forbidden) {
			t.Fatalf("human report contains private or misleading text %q: %s", forbidden, human.String())
		}
	}
	if strings.Contains(human.String(), "never-observed=") || strings.Contains(human.String(), "first-effective-activity=") {
		t.Fatalf("human report uses imprecise activity labels: %s", human.String())
	}
	for _, want := range []string{"invocation-uses=2", "distinct-sessions=2", "first-observed=", "last-observed=", "first-invocation-effective=", "evidence-sources=hook,transcript", "definitions=2"} {
		if !strings.Contains(human.String(), want) {
			t.Fatalf("human report = %q, missing %q", human.String(), want)
		}
	}
}

func TestReportAndStaleJSONEmptyDatasetsAreValid(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "empty.db")
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	options := Options{Home: filepath.Join(root, "home"), CWD: root, ProjectRoot: root, Now: func() time.Time { return now }}
	for _, command := range []string{"report", "stale"} {
		var stdout, stderr bytes.Buffer
		if err := ExecuteWithOptions(options, []string{command, "--json", "--db", dbPath}, nil, &stdout, &stderr); err != nil {
			t.Fatalf("%s empty JSON error = %v\nstderr=%s", command, err, stderr.String())
		}
		if strings.Contains(stdout.String(), "as-of=") || strings.Contains(stdout.String(), "capabilities:") {
			t.Fatalf("%s empty JSON contains terminal decoration: %s", command, stdout.String())
		}
		var envelope struct {
			SchemaVersion int              `json:"schema_version"`
			GeneratedAt   string           `json:"generated_at"`
			Runtimes      []map[string]any `json:"runtimes"`
			Capabilities  []map[string]any `json:"capabilities"`
			UsageOnly     []map[string]any `json:"usage_only"`
			Findings      []map[string]any `json:"findings"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
			t.Fatalf("decode %s empty JSON: %v\noutput=%s", command, err, stdout.String())
		}
		if envelope.SchemaVersion != reportdto.SchemaVersion || envelope.GeneratedAt != now.Format(time.RFC3339Nano) || len(envelope.Runtimes) != 2 || len(envelope.Capabilities) != 0 || len(envelope.Findings) != 0 {
			t.Fatalf("%s empty JSON envelope = %#v", command, envelope)
		}
		if command == "report" && len(envelope.UsageOnly) != 0 {
			t.Fatalf("empty report usage-only = %#v, want []", envelope.UsageOnly)
		}
	}
}

func reportTestCapability(root, name string, scope domain.Scope) domain.Capability {
	return domain.Capability{
		Runtime:        domain.RuntimeCodex,
		Type:           domain.CapabilitySkill,
		Name:           name,
		Scope:          scope,
		Source:         filepath.Join(root, string(scope), name, "SKILL.md"),
		Enabled:        domain.EnabledStateEnabled,
		Advertisement:  domain.AdvertisementStateFullyAdvertised,
		MetadataTokens: domain.Measurement{Confidence: domain.ConfidenceUnknown},
		BodyTokens:     domain.Measurement{Confidence: domain.ConfidenceUnknown},
	}
}

func reportTestEvent(observed time.Time, sourceTimestamp *time.Time, name string, eventType domain.EventType, provenance domain.Provenance, session string) domain.UsageEvent {
	return domain.UsageEvent{
		ObservedAt:       observed,
		SourceTimestamp:  sourceTimestamp,
		Runtime:          domain.RuntimeCodex,
		SessionID:        session,
		ProjectID:        "private-project",
		CapabilityType:   domain.CapabilitySkill,
		CapabilityName:   name,
		EventType:        eventType,
		Provenance:       provenance,
		InvocationOrigin: domain.InvocationOriginUnknown,
		SchemaVersion:    domain.CurrentUsageEventSchemaVersion,
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
