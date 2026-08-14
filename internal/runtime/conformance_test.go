package runtime_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	stdRuntime "runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/runtime/claude"
	"github.com/kespineira/harness-lint/internal/runtime/codex"
)

const conformanceObservedAt = "2026-08-14T12:00:00.000Z"

type conformanceFixtureSuite struct {
	runtime       domain.Runtime
	documentation string
	root          string
	parse         func([]byte, time.Time) (domain.UsageEvent, error)
}

type conformanceManifest struct {
	ManifestSchema           string                             `json:"manifest_schema"`
	Runtime                  string                             `json:"runtime"`
	FixtureSchema            string                             `json:"fixture_schema"`
	SourceDocumentationURL   string                             `json:"source_documentation_url"`
	AsOf                     string                             `json:"as_of"`
	LastValidated            string                             `json:"last_validated"`
	RuntimeVersion           *string                            `json:"runtime_version"`
	RuntimeVersionProvenance conformanceVersionProvenance       `json:"runtime_version_provenance"`
	RepresentedEventSchema   string                             `json:"represented_event_schema"`
	RepresentedConfigSchema  string                             `json:"represented_config_schema"`
	DocumentationProvenance  conformanceDocumentationProvenance `json:"documentation_provenance"`
	ReleaseBehaviorNote      string                             `json:"release_behavior_note"`
	ExpectationPolicy        map[string]string                  `json:"expectation_policy"`
	IdentityPolicy           map[string]string                  `json:"identity_policy"`
	Fixtures                 []conformanceFixture               `json:"fixtures"`
}

type conformanceVersionProvenance struct {
	Status   string `json:"status"`
	Source   string `json:"source"`
	Evidence string `json:"evidence"`
}

type conformanceDocumentationProvenance struct {
	Kind         string `json:"kind"`
	URL          string `json:"url"`
	RetrievedAt  string `json:"retrieved_at"`
	ReleaseScope string `json:"release_scope"`
}

type conformanceFixture struct {
	File           string   `json:"file"`
	Expectation    string   `json:"expectation"`
	CapabilityType string   `json:"capability_type"`
	CapabilityName string   `json:"capability_name"`
	SourceIdentity string   `json:"source_identity"`
	DuplicateGroup string   `json:"duplicate_group"`
	Identity       string   `json:"identity"`
	OptionalFields []string `json:"optional_fields"`
	StripFields    []string `json:"strip_fields"`
}

type parsedConformanceFixture struct {
	fixture conformanceFixture
	event   domain.UsageEvent
}

func TestRuntimeConformanceFixtureManifests(t *testing.T) {
	_, sourceFile, _, ok := stdRuntime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	tests := []conformanceFixtureSuite{
		{
			runtime:       domain.RuntimeClaudeCode,
			documentation: "https://code.claude.com/docs/en/hooks",
			root:          filepath.Join(filepath.Dir(sourceFile), "..", "..", "testdata", "claude", "hooks"),
			parse: func(payload []byte, observedAt time.Time) (domain.UsageEvent, error) {
				return claude.ParseHookPayload(payload, observedAt)
			},
		},
		{
			runtime:       domain.RuntimeCodex,
			documentation: "https://learn.chatgpt.com/docs/hooks",
			root:          filepath.Join(filepath.Dir(sourceFile), "..", "..", "testdata", "codex", "hooks", "v1"),
			parse: func(payload []byte, observedAt time.Time) (domain.UsageEvent, error) {
				return codex.ParseHookPayload(payload, observedAt)
			},
		},
	}

	observedAt, err := time.Parse(time.RFC3339Nano, conformanceObservedAt)
	if err != nil {
		t.Fatalf("parse fixed observed-at time: %v", err)
	}
	for _, suite := range tests {
		suite := suite
		t.Run(string(suite.runtime), func(t *testing.T) {
			manifest := readConformanceManifest(t, suite)
			validateConformanceManifest(t, suite, manifest)
			assertManifestEnumeratesFixtureFiles(t, suite, manifest)

			parsed := make(map[string]parsedConformanceFixture, len(manifest.Fixtures))
			for _, fixture := range manifest.Fixtures {
				fixture := fixture
				t.Run(fixture.File, func(t *testing.T) {
					payload := readConformanceFixture(t, suite, fixture.File)
					event, parseErr := suite.parse(payload, observedAt)
					switch fixture.Expectation {
					case "malformed", "unknown_event":
						if parseErr == nil {
							t.Fatalf("parse succeeded for %s fixture", fixture.Expectation)
						}
						if strings.Contains(parseErr.Error(), "SENTINEL_") {
							t.Fatalf("parser error leaked fixture payload sentinel: %v", parseErr)
						}
						return
					case "valid", "additive", "optional", "duplicate":
						if parseErr != nil {
							t.Fatalf("parse error for %s fixture: %v", fixture.Expectation, parseErr)
						}
					default:
						t.Fatalf("unsupported fixture expectation %q", fixture.Expectation)
					}

					assertConformanceEvent(t, suite, fixture, event)
					if fixture.Expectation == "additive" && len(fixture.StripFields) > 0 {
						baselinePayload := stripTopLevelJSONFields(t, payload, fixture.StripFields)
						baseline, baselineErr := suite.parse(baselinePayload, observedAt)
						if baselineErr != nil {
							t.Fatalf("baseline parse after stripping additive fields: %v", baselineErr)
						}
						if !sameConformanceEvent(event, baseline) {
							t.Fatalf("additive fields changed normalized event:\nwith fields=%#v\nwithout fields=%#v", event, baseline)
						}
					}
					parsed[fixture.File] = parsedConformanceFixture{fixture: fixture, event: event}
				})
			}
			assertDuplicateIdentityPolicy(t, manifest, parsed)
		})
	}
}

func readConformanceManifest(t *testing.T, suite conformanceFixtureSuite) conformanceManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(suite.root, "manifest.json"))
	if err != nil {
		t.Fatalf("read %s manifest: %v", suite.runtime, err)
	}
	var manifest conformanceManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode %s manifest: %v", suite.runtime, err)
	}
	return manifest
}

func validateConformanceManifest(t *testing.T, suite conformanceFixtureSuite, manifest conformanceManifest) {
	t.Helper()
	if domain.Runtime(manifest.Runtime) != suite.runtime {
		t.Fatalf("manifest runtime = %q, want %q", manifest.Runtime, suite.runtime)
	}
	if manifest.ManifestSchema != "runtime-conformance/manifest-v2" {
		t.Fatalf("manifest schema = %q, want runtime-conformance/manifest-v2", manifest.ManifestSchema)
	}
	if manifest.FixtureSchema != "runtime-conformance/hooks-v1" {
		t.Fatalf("fixture schema = %q, want runtime-conformance/hooks-v1", manifest.FixtureSchema)
	}
	if manifest.SourceDocumentationURL != suite.documentation {
		t.Fatalf("documentation URL = %q, want %q", manifest.SourceDocumentationURL, suite.documentation)
	}
	asOf, err := time.Parse("2006-01-02", manifest.AsOf)
	if err != nil {
		t.Fatalf("manifest as_of = %q is not an ISO date: %v", manifest.AsOf, err)
	}
	lastValidated, err := time.Parse("2006-01-02", manifest.LastValidated)
	if err != nil {
		t.Fatalf("manifest last_validated = %q is not an ISO date: %v", manifest.LastValidated, err)
	}
	if lastValidated.Before(asOf) {
		t.Fatalf("manifest last_validated = %q is before as_of %q", manifest.LastValidated, manifest.AsOf)
	}
	if manifest.RuntimeVersion == nil && manifest.RuntimeVersionProvenance.Status != "unknown" {
		t.Fatalf("runtime_version is unavailable but provenance status = %q, want unknown", manifest.RuntimeVersionProvenance.Status)
	}
	if manifest.RuntimeVersion != nil && strings.TrimSpace(*manifest.RuntimeVersion) == "" {
		t.Fatal("manifest runtime_version must be non-empty when known")
	}
	if manifest.RuntimeVersion != nil && manifest.RuntimeVersionProvenance.Status != "known" {
		t.Fatalf("runtime_version is present but provenance status = %q, want known", manifest.RuntimeVersionProvenance.Status)
	}
	if manifest.RuntimeVersionProvenance.Status != "unknown" && manifest.RuntimeVersionProvenance.Status != "known" {
		t.Fatalf("unsupported runtime_version_provenance status %q", manifest.RuntimeVersionProvenance.Status)
	}
	if strings.TrimSpace(manifest.RuntimeVersionProvenance.Source) == "" || strings.TrimSpace(manifest.RuntimeVersionProvenance.Evidence) == "" {
		t.Fatal("runtime_version_provenance must include source and evidence")
	}
	if strings.TrimSpace(manifest.RepresentedEventSchema) == "" || strings.TrimSpace(manifest.RepresentedConfigSchema) == "" {
		t.Fatal("manifest represented event/config schemas must be non-empty")
	}
	if manifest.DocumentationProvenance.Kind != "official-release-reference" {
		t.Fatalf("documentation provenance kind = %q, want official-release-reference", manifest.DocumentationProvenance.Kind)
	}
	if manifest.DocumentationProvenance.URL != manifest.SourceDocumentationURL || !strings.HasPrefix(manifest.DocumentationProvenance.URL, "https://") {
		t.Fatalf("documentation provenance URL = %q, want HTTPS source URL %q", manifest.DocumentationProvenance.URL, manifest.SourceDocumentationURL)
	}
	retrievedAt, err := time.Parse("2006-01-02", manifest.DocumentationProvenance.RetrievedAt)
	if err != nil {
		t.Fatalf("documentation retrieved_at = %q is not an ISO date: %v", manifest.DocumentationProvenance.RetrievedAt, err)
	}
	if retrievedAt.After(lastValidated) {
		t.Fatalf("documentation retrieved_at = %q is after last_validated %q", manifest.DocumentationProvenance.RetrievedAt, manifest.LastValidated)
	}
	if strings.TrimSpace(manifest.DocumentationProvenance.ReleaseScope) == "" {
		t.Fatal("documentation provenance release_scope is empty")
	}
	if strings.TrimSpace(manifest.ReleaseBehaviorNote) == "" {
		t.Fatal("manifest release_behavior_note is empty")
	}
	for _, name := range []string{"valid", "malformed", "unknown_event", "additive", "optional", "duplicate"} {
		if strings.TrimSpace(manifest.ExpectationPolicy[name]) == "" {
			t.Fatalf("manifest expectation_policy[%q] is empty or missing", name)
		}
	}
	for _, name := range []string{"same", "distinct", "missing"} {
		if strings.TrimSpace(manifest.IdentityPolicy[name]) == "" {
			t.Fatalf("manifest identity_policy[%q] is empty or missing", name)
		}
	}
	if len(manifest.Fixtures) == 0 {
		t.Fatal("manifest has no fixtures")
	}

	seen := make(map[string]struct{}, len(manifest.Fixtures))
	for _, fixture := range manifest.Fixtures {
		if strings.TrimSpace(fixture.File) == "" {
			t.Fatal("manifest contains fixture with empty file name")
		}
		if filepath.IsAbs(fixture.File) || filepath.Clean(fixture.File) != fixture.File || fixture.File == "." || strings.HasPrefix(fixture.File, ".."+string(filepath.Separator)) {
			t.Fatalf("fixture path %q is not a clean relative path", fixture.File)
		}
		if _, exists := seen[fixture.File]; exists {
			t.Fatalf("manifest lists fixture %q more than once", fixture.File)
		}
		seen[fixture.File] = struct{}{}
		switch fixture.Expectation {
		case "valid", "additive", "optional":
			validateExpectedCapability(t, fixture)
			if fixture.Expectation == "additive" && len(fixture.StripFields) == 0 {
				t.Fatalf("additive fixture %q must declare strip_fields for a baseline comparison", fixture.File)
			}
			if fixture.Expectation == "optional" && len(fixture.OptionalFields) == 0 {
				t.Fatalf("optional fixture %q must declare optional_fields", fixture.File)
			}
		case "duplicate":
			validateExpectedCapability(t, fixture)
			if strings.TrimSpace(fixture.DuplicateGroup) == "" || (fixture.Identity != "same" && fixture.Identity != "distinct") {
				t.Fatalf("duplicate fixture %q must declare duplicate_group and same/distinct identity", fixture.File)
			}
		case "malformed", "unknown_event":
			if fixture.CapabilityType != "" || fixture.CapabilityName != "" || fixture.SourceIdentity != "" {
				t.Fatalf("rejected fixture %q carries a successful-event expectation: %#v", fixture.File, fixture)
			}
		default:
			t.Fatalf("fixture %q has unsupported expectation %q", fixture.File, fixture.Expectation)
		}
		if fixture.SourceIdentity != "" && fixture.SourceIdentity != "present" && fixture.SourceIdentity != "absent" {
			t.Fatalf("fixture %q has invalid source_identity expectation %q", fixture.File, fixture.SourceIdentity)
		}
	}
}

func validateExpectedCapability(t *testing.T, fixture conformanceFixture) {
	t.Helper()
	if strings.TrimSpace(fixture.CapabilityType) == "" || strings.TrimSpace(fixture.CapabilityName) == "" {
		t.Fatalf("accepted fixture %q must declare capability_type and capability_name", fixture.File)
	}
	if fixture.SourceIdentity != "present" && fixture.SourceIdentity != "absent" {
		t.Fatalf("accepted fixture %q must declare source_identity present/absent", fixture.File)
	}
}

func assertManifestEnumeratesFixtureFiles(t *testing.T, suite conformanceFixtureSuite, manifest conformanceManifest) {
	t.Helper()
	entries, err := os.ReadDir(suite.root)
	if err != nil {
		t.Fatalf("read %s fixture directory: %v", suite.runtime, err)
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == "manifest.json" || strings.ToLower(filepath.Ext(entry.Name())) != ".json" {
			continue
		}
		files = append(files, entry.Name())
	}
	sort.Strings(files)
	listed := make([]string, 0, len(manifest.Fixtures))
	for _, fixture := range manifest.Fixtures {
		listed = append(listed, fixture.File)
	}
	sort.Strings(listed)
	if len(files) != len(listed) {
		t.Fatalf("manifest enumerates %d fixtures, directory has %d JSON fixtures: manifest=%v files=%v", len(listed), len(files), listed, files)
	}
	for i := range files {
		if files[i] != listed[i] {
			t.Fatalf("manifest fixture list differs from directory: manifest=%v files=%v", listed, files)
		}
	}
}

func readConformanceFixture(t *testing.T, suite conformanceFixtureSuite, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(suite.root, name))
	if err != nil {
		t.Fatalf("read %s fixture %q: %v", suite.runtime, name, err)
	}
	return data
}

func assertConformanceEvent(t *testing.T, suite conformanceFixtureSuite, fixture conformanceFixture, event domain.UsageEvent) {
	t.Helper()
	if err := event.Validate(); err != nil {
		t.Fatalf("fixture %q normalized event is invalid: %v", fixture.File, err)
	}
	if event.Runtime != suite.runtime {
		t.Fatalf("fixture %q runtime = %q, want %q", fixture.File, event.Runtime, suite.runtime)
	}
	if event.EventType != domain.EventInvoked || event.Provenance != domain.ProvenanceHook {
		t.Fatalf("fixture %q event semantics = %q/%q, want invoked/hook", fixture.File, event.EventType, event.Provenance)
	}
	if event.CapabilityType != domain.CapabilityType(fixture.CapabilityType) || event.CapabilityName != fixture.CapabilityName {
		t.Fatalf("fixture %q capability = %q/%q, want %q/%q", fixture.File, event.CapabilityType, event.CapabilityName, fixture.CapabilityType, fixture.CapabilityName)
	}
	if fixture.SourceIdentity == "present" && event.SourceIdentity == "" {
		t.Fatalf("fixture %q has no source identity", fixture.File)
	}
	if fixture.SourceIdentity == "absent" && event.SourceIdentity != "" {
		t.Fatalf("fixture %q has unexpected source identity %q", fixture.File, event.SourceIdentity)
	}
	if event.SourceTimestamp != nil {
		t.Fatalf("fixture %q has source timestamp %v; direct hook source timestamps are not documented", fixture.File, event.SourceTimestamp)
	}

	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal normalized event for fixture %q: %v", fixture.File, err)
	}
	for _, forbidden := range []string{"SENTINEL_", "fixture-session", "codex-session-", "/fixture/project"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("fixture %q normalized event leaked %q: %s", fixture.File, forbidden, encoded)
		}
	}
}

func stripTopLevelJSONFields(t *testing.T, payload []byte, fields []string) []byte {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatalf("decode additive fixture object: %v", err)
	}
	for _, field := range fields {
		delete(object, field)
	}
	stripped, err := json.Marshal(object)
	if err != nil {
		t.Fatalf("encode additive fixture baseline: %v", err)
	}
	return stripped
}

func sameConformanceEvent(left, right domain.UsageEvent) bool {
	return left.ObservedAt.Equal(right.ObservedAt) &&
		(left.SourceTimestamp == nil) == (right.SourceTimestamp == nil) &&
		left.Runtime == right.Runtime &&
		left.SessionID == right.SessionID &&
		left.ProjectID == right.ProjectID &&
		left.CapabilityType == right.CapabilityType &&
		left.CapabilityName == right.CapabilityName &&
		left.EventType == right.EventType &&
		left.Provenance == right.Provenance &&
		left.InvocationOrigin == right.InvocationOrigin &&
		left.SchemaVersion == right.SchemaVersion &&
		left.SourceIdentity == right.SourceIdentity &&
		left.Fingerprint == right.Fingerprint
}

func assertDuplicateIdentityPolicy(t *testing.T, manifest conformanceManifest, parsed map[string]parsedConformanceFixture) {
	t.Helper()
	groups := make(map[string][]parsedConformanceFixture)
	for _, fixture := range parsed {
		if fixture.fixture.Expectation == "duplicate" {
			groups[fixture.fixture.DuplicateGroup] = append(groups[fixture.fixture.DuplicateGroup], fixture)
		}
	}
	if len(groups) == 0 {
		t.Fatal("manifest has no duplicate identity group")
	}
	for group, fixtures := range groups {
		if len(fixtures) < 2 {
			t.Fatalf("duplicate group %q has %d fixtures, want at least two", group, len(fixtures))
		}
		var same, distinct []parsedConformanceFixture
		for _, fixture := range fixtures {
			switch fixture.fixture.Identity {
			case "same":
				same = append(same, fixture)
			case "distinct":
				distinct = append(distinct, fixture)
			}
		}
		if len(same) < 2 || len(distinct) < 1 {
			t.Fatalf("duplicate group %q must contain >=2 same and >=1 distinct identities: %#v", group, fixtures)
		}
		for _, fixture := range same[1:] {
			if fixture.event.Fingerprint != same[0].event.Fingerprint || fixture.event.SourceIdentity != same[0].event.SourceIdentity {
				t.Fatalf("same identity group %q did not deduplicate: %#v vs %#v", group, same[0].event, fixture.event)
			}
		}
		for _, fixture := range distinct {
			if fixture.event.Fingerprint == same[0].event.Fingerprint {
				t.Fatalf("distinct identity in group %q collapsed into same fingerprint: %#v", group, fixture.event)
			}
		}
	}
	if strings.TrimSpace(manifest.IdentityPolicy["missing"]) == "" {
		t.Fatal("manifest missing identity fallback policy")
	}
}
