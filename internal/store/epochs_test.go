package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/capture"
	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/history"
)

func TestHookIngestAndCaptureFailuresDriveCaptureEpochLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	base := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	hook := testUsageEvent(base, "direct-hook", domain.EventInvoked)
	hook.Provenance = domain.ProvenanceHook
	if err := s.IngestUsageEvent(ctx, hook); err != nil {
		t.Fatalf("first hook ingest: %v", err)
	}
	if err := s.IngestUsageEvent(ctx, hook); err != nil {
		t.Fatalf("idempotent hook ingest: %v", err)
	}
	epochs, err := s.ListCaptureEpochs(ctx, domain.RuntimeCodex)
	if err != nil || len(epochs) != 1 || !epochs[0].Start.Equal(base) || !epochs[0].IsOpen() {
		t.Fatalf("open epoch after successful delivery = %#v, err=%v", epochs, err)
	}
	if err := s.RecordCaptureFailure(ctx, capture.CaptureFailure{
		Runtime: domain.RuntimeCodex, FailedAt: base.Add(time.Hour), Kind: capture.FailureMalformedPayload,
	}); err != nil {
		t.Fatalf("confirmed parser failure: %v", err)
	}
	epochs, err = s.ListCaptureEpochs(ctx, domain.RuntimeCodex)
	if err != nil || len(epochs) != 1 || !epochs[0].End.Equal(base.Add(time.Hour)) || epochs[0].EndReason != history.CaptureEndReasonConfirmedCaptureFailure {
		t.Fatalf("closed epoch after parser failure = %#v, err=%v", epochs, err)
	}
	// An unsupported event is outside the managed capture contract and must
	// not manufacture a lifecycle gap or close otherwise valid coverage.
	if err := s.RecordCaptureFailure(ctx, capture.CaptureFailure{
		Runtime: domain.RuntimeCodex, FailedAt: base.Add(2 * time.Hour), Kind: capture.FailureUnsupportedEvent,
	}); err != nil {
		t.Fatalf("unsupported event: %v", err)
	}
	hook.ObservedAt = base.Add(3 * time.Hour)
	if err := s.IngestUsageEvent(ctx, hook); err != nil {
		t.Fatalf("recovery hook ingest: %v", err)
	}
	epochs, err = s.ListCaptureEpochs(ctx, domain.RuntimeCodex)
	if err != nil || len(epochs) != 2 || !epochs[1].Start.Equal(base.Add(3*time.Hour)) || !epochs[1].IsOpen() {
		t.Fatalf("recovery epochs = %#v, err=%v", epochs, err)
	}
}

func TestCaptureSelfTestRollsBackEpochAndUsageState(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	base := time.Now().UTC().Add(-time.Hour)
	hook := testUsageEvent(base, "existing-hook", domain.EventInvoked)
	hook.Provenance = domain.ProvenanceHook
	if err := s.IngestUsageEvent(ctx, hook); err != nil {
		t.Fatalf("seed hook ingest: %v", err)
	}
	beforeEpochs, err := s.ListCaptureEpochs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SelfTestCaptureIngest(ctx); err != nil {
		t.Fatalf("capture self-test: %v", err)
	}
	afterEpochs, err := s.ListCaptureEpochs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeEpochs, afterEpochs) {
		t.Fatalf("capture self-test changed epochs: before=%#v after=%#v", beforeEpochs, afterEpochs)
	}
	events, err := s.ListUsageEvents(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("capture self-test changed usage history: %#v", events)
	}
}

func TestCaptureEpochPrimitivesAreChronologicalAndIdempotent(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	base := time.Date(2026, 8, 14, 10, 0, 0, 123, time.FixedZone("CEST", 2*60*60))
	if err := s.OpenCaptureEpoch(ctx, domain.RuntimeCodex, base, history.CaptureStartReasonConfirmedDirectDelivery); err != nil {
		t.Fatalf("OpenCaptureEpoch(): %v", err)
	}
	if err := s.OpenCaptureEpoch(ctx, domain.RuntimeCodex, base, history.CaptureStartReasonConfirmedDirectDelivery); err != nil {
		t.Fatalf("idempotent OpenCaptureEpoch(): %v", err)
	}
	if err := s.OpenCaptureEpoch(ctx, domain.RuntimeCodex, base.Add(time.Minute), history.CaptureStartReasonConfirmedDirectDelivery); err != nil {
		t.Fatalf("later delivery continuation: %v", err)
	}
	if err := s.OpenCaptureEpoch(ctx, domain.RuntimeCodex, base.Add(-time.Nanosecond), history.CaptureStartReasonConfirmedDirectDelivery); err == nil {
		t.Fatal("earlier delivery while active succeeded")
	}
	if err := s.CloseCaptureEpoch(ctx, domain.RuntimeCodex, base.Add(time.Hour), history.CaptureEndReasonConfirmedCaptureFailure); err != nil {
		t.Fatalf("CloseCaptureEpoch(): %v", err)
	}
	if err := s.CloseCaptureEpoch(ctx, domain.RuntimeCodex, base.Add(time.Hour), history.CaptureEndReasonConfirmedCaptureFailure); err != nil {
		t.Fatalf("idempotent CloseCaptureEpoch(): %v", err)
	}
	if err := s.CloseCaptureEpoch(ctx, domain.RuntimeCodex, base.Add(2*time.Hour), history.CaptureEndReasonConfirmedCaptureFailure); err != nil {
		t.Fatalf("close without open: %v", err)
	}
	if err := s.OpenCaptureEpoch(ctx, domain.RuntimeCodex, base.Add(30*time.Minute), history.CaptureStartReasonConfirmedDirectDelivery); err == nil {
		t.Fatal("overlapping capture epoch open succeeded")
	}
	second := base.Add(2 * time.Hour)
	if err := s.OpenCaptureEpoch(ctx, domain.RuntimeCodex, second, history.CaptureStartReasonConfirmedDirectDelivery); err != nil {
		t.Fatalf("OpenCaptureEpoch(second): %v", err)
	}
	if err := s.CloseCaptureEpoch(ctx, domain.RuntimeCodex, second.Add(time.Hour), history.CaptureEndReasonManagedHookUninstall); err != nil {
		t.Fatalf("CloseCaptureEpoch(second): %v", err)
	}
	epochs, err := s.ListCaptureEpochs(ctx, domain.RuntimeCodex)
	if err != nil {
		t.Fatalf("ListCaptureEpochs(): %v", err)
	}
	if len(epochs) != 2 || !epochs[0].Start.Equal(base.UTC()) || !epochs[0].End.Equal(base.Add(time.Hour).UTC()) || !epochs[1].Start.Equal(second.UTC()) {
		t.Fatalf("capture epochs = %#v", epochs)
	}
	if err := s.OpenCaptureEpoch(ctx, domain.RuntimeCodex, base.Add(3*time.Hour), history.CaptureStartReason("configuration")); err == nil {
		t.Fatal("invalid start reason accepted")
	}
}

func TestRecordInventoryTransitionsPresenceEpochsWithoutFlattenedBackfill(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	base := time.Date(2026, 8, 14, 23, 59, 59, 999000000, time.FixedZone("CEST", 2*60*60))
	capability := testCapability("present", time.Time{}, time.Time{})
	key := CapabilityPresenceKey{Runtime: domain.RuntimeCodex, CapabilityType: capability.Type, CapabilityName: capability.Name, Scope: capability.Scope, Source: capability.Source}
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, base, []domain.Capability{capability}); err != nil {
		t.Fatalf("RecordInventory(first): %v", err)
	}
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, base.Add(time.Hour), nil); err != nil {
		t.Fatalf("RecordInventory(disappearance): %v", err)
	}
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, base.Add(2*time.Hour), []domain.Capability{capability}); err != nil {
		t.Fatalf("RecordInventory(reappearance): %v", err)
	}
	epochs, err := s.ListCapabilityPresenceEpochs(ctx, key)
	if err != nil {
		t.Fatalf("ListCapabilityPresenceEpochs(): %v", err)
	}
	want := []CapabilityPresenceEpoch{
		{Key: key, Interval: history.Interval{Start: base.UTC(), End: base.Add(time.Hour).UTC()}},
		{Key: key, Interval: history.Interval{Start: base.Add(2 * time.Hour).UTC()}},
	}
	if !reflect.DeepEqual(epochs, want) {
		t.Fatalf("presence epochs = %#v, want %#v", epochs, want)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM capability_presence_epochs`).Scan(&count); err != nil {
		t.Fatalf("count presence epochs: %v", err)
	}
	if count != 2 {
		t.Fatalf("presence epoch count = %d, want 2", count)
	}
}

func TestRecordInventoryPreservesPresenceIdentityAcrossScopeAndSource(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	base := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	project := testCapability("same-name", time.Time{}, time.Time{})
	project.Scope = domain.ScopeProject
	project.Source = "/project/same-name"
	user := project
	user.Scope = domain.ScopeUser
	user.Source = "/user/same-name"
	projectKey := CapabilityPresenceKey{Runtime: domain.RuntimeCodex, CapabilityType: project.Type, CapabilityName: project.Name, Scope: project.Scope, Source: project.Source}
	userKey := CapabilityPresenceKey{Runtime: domain.RuntimeCodex, CapabilityType: user.Type, CapabilityName: user.Name, Scope: user.Scope, Source: user.Source}

	if err := s.RecordInventory(ctx, domain.RuntimeCodex, base, []domain.Capability{project, user}); err != nil {
		t.Fatalf("RecordInventory(first): %v", err)
	}
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, base.Add(time.Hour), []domain.Capability{user}); err != nil {
		t.Fatalf("RecordInventory(project disappears): %v", err)
	}
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, base.Add(2*time.Hour), []domain.Capability{project, user}); err != nil {
		t.Fatalf("RecordInventory(project reappears): %v", err)
	}

	rows, err := s.db.QueryContext(ctx, `SELECT scope, source, started_at, ended_at FROM capability_presence_epochs ORDER BY scope, source, started_at`)
	if err != nil {
		t.Fatalf("query presence rows: %v", err)
	}
	defer rows.Close()
	type persisted struct {
		scope, source, started, ended string
	}
	var got []persisted
	for rows.Next() {
		var row persisted
		var ended sql.NullString
		if err := rows.Scan(&row.scope, &row.source, &row.started, &ended); err != nil {
			t.Fatalf("scan presence row: %v", err)
		}
		row.ended = ended.String
		got = append(got, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate presence rows: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("persisted presence rows = %#v, want three full-identity rows", got)
	}
	if got[0].scope != string(domain.ScopeProject) || got[0].source != project.Source || got[0].ended == "" {
		t.Fatalf("project disappearance row = %#v", got[0])
	}
	if got[1].scope != string(domain.ScopeProject) || got[1].source != project.Source || got[1].ended != "" {
		t.Fatalf("project reappearance row = %#v", got[1])
	}
	if got[2].scope != string(domain.ScopeUser) || got[2].source != user.Source || got[2].ended != "" {
		t.Fatalf("user continuation row = %#v", got[2])
	}

	epochs, err := s.ListCapabilityPresenceEpochs(ctx)
	if err != nil {
		t.Fatalf("ListCapabilityPresenceEpochs(): %v", err)
	}
	wantEpochs := []CapabilityPresenceEpoch{
		{Key: projectKey, Interval: history.Interval{Start: base.UTC(), End: base.Add(time.Hour).UTC()}},
		{Key: projectKey, Interval: history.Interval{Start: base.Add(2 * time.Hour).UTC()}},
		{Key: userKey, Interval: history.Interval{Start: base.UTC()}},
	}
	if !reflect.DeepEqual(epochs, wantEpochs) {
		t.Fatalf("presence epochs = %#v, want full-identity intervals %#v", epochs, wantEpochs)
	}
	projectEpochs, err := s.ListCapabilityPresenceEpochs(ctx, projectKey)
	if err != nil {
		t.Fatalf("ListCapabilityPresenceEpochs(project): %v", err)
	}
	if !reflect.DeepEqual(projectEpochs, wantEpochs[:2]) {
		t.Fatalf("project presence epochs = %#v, want %#v", projectEpochs, wantEpochs[:2])
	}
	userEpochs, err := s.ListCapabilityPresenceEpochs(ctx, userKey)
	if err != nil {
		t.Fatalf("ListCapabilityPresenceEpochs(user): %v", err)
	}
	if !reflect.DeepEqual(userEpochs, wantEpochs[2:]) {
		t.Fatalf("user presence epochs = %#v, want %#v", userEpochs, wantEpochs[2:])
	}
}

func TestCapabilityPresenceEpochValidation(t *testing.T) {
	base := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	valid := CapabilityPresenceEpoch{
		Key: CapabilityPresenceKey{
			Runtime:        domain.RuntimeCodex,
			CapabilityType: domain.CapabilitySkill,
			CapabilityName: "valid",
			Scope:          domain.ScopeProject,
			Source:         "/project/valid",
		},
		Interval: history.Interval{Start: base, End: base.Add(time.Hour)},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid capability presence epoch rejected: %v", err)
	}
	invalidKey := valid
	invalidKey.Key.Scope = domain.ScopeUnknown
	if err := invalidKey.Validate(); err == nil {
		t.Fatal("invalid capability presence scope accepted")
	}
	invalidInterval := valid
	invalidInterval.Interval.End = base
	if err := invalidInterval.Validate(); err == nil {
		t.Fatal("zero-duration capability presence interval accepted")
	}
}

func TestEpochCloseWithoutOpenAndZeroDurationAreIdempotent(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	base := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	reason := history.CaptureEndReasonManagedHookUninstall
	if err := s.CloseCaptureEpoch(ctx, domain.RuntimeCodex, base, reason); err != nil {
		t.Fatalf("close without open: %v", err)
	}
	if err := s.OpenCaptureEpoch(ctx, domain.RuntimeCodex, base, history.CaptureStartReasonConfirmedDirectDelivery); err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.CloseCaptureEpoch(ctx, domain.RuntimeCodex, base, reason); err != nil {
		t.Fatalf("zero-duration close: %v", err)
	}
	if err := s.CloseCaptureEpoch(ctx, domain.RuntimeCodex, base.Add(time.Hour), reason); err != nil {
		t.Fatalf("repeated close after zero-duration discard: %v", err)
	}
	epochs, err := s.ListCaptureEpochs(ctx, domain.RuntimeCodex)
	if err != nil {
		t.Fatalf("ListCaptureEpochs(): %v", err)
	}
	if len(epochs) != 0 {
		t.Fatalf("zero-duration capture epochs = %#v, want empty", epochs)
	}

	capability := testCapability("zero-duration", time.Time{}, time.Time{})
	key := CapabilityPresenceKey{Runtime: capability.Runtime, CapabilityType: capability.Type, CapabilityName: capability.Name, Scope: capability.Scope, Source: capability.Source}
	if err := s.RecordInventory(ctx, capability.Runtime, base, []domain.Capability{capability}); err != nil {
		t.Fatalf("RecordInventory(open): %v", err)
	}
	if err := s.RecordInventory(ctx, capability.Runtime, base, nil); err != nil {
		t.Fatalf("RecordInventory(zero-duration): %v", err)
	}
	presence, err := s.ListCapabilityPresenceEpochs(ctx, key)
	if err != nil {
		t.Fatalf("ListCapabilityPresenceEpochs(): %v", err)
	}
	if len(presence) != 0 {
		t.Fatalf("zero-duration presence epochs = %#v, want empty", presence)
	}
}

func TestEpochTimestampsUseFixedNanosecondOrderingAndChecks(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	base := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	if err := s.OpenCaptureEpoch(ctx, domain.RuntimeCodex, base, history.CaptureStartReasonConfirmedDirectDelivery); err != nil {
		t.Fatalf("OpenCaptureEpoch(exact second): %v", err)
	}
	if err := s.CloseCaptureEpoch(ctx, domain.RuntimeCodex, base.Add(time.Nanosecond), history.CaptureEndReasonConfirmedCaptureFailure); err != nil {
		t.Fatalf("CloseCaptureEpoch(one nanosecond later): %v", err)
	}
	var captureStarted, captureEnded string
	if err := s.db.QueryRowContext(ctx, `SELECT started_at, ended_at FROM capture_epochs WHERE runtime = ?`, domain.RuntimeCodex).Scan(&captureStarted, &captureEnded); err != nil {
		t.Fatalf("read fixed capture timestamps: %v", err)
	}
	if captureStarted != formatEpochTimestamp(base) || captureEnded != formatEpochTimestamp(base.Add(time.Nanosecond)) || captureStarted >= captureEnded {
		t.Fatalf("fixed capture timestamps = %q..%q, want fixed-width lexical order", captureStarted, captureEnded)
	}
	capability := testCapability("nanosecond-order", time.Time{}, time.Time{})
	key := CapabilityPresenceKey{Runtime: capability.Runtime, CapabilityType: capability.Type, CapabilityName: capability.Name, Scope: capability.Scope, Source: capability.Source}
	if err := s.RecordInventory(ctx, capability.Runtime, base, []domain.Capability{capability}); err != nil {
		t.Fatalf("RecordInventory(exact second): %v", err)
	}
	if err := s.RecordInventory(ctx, capability.Runtime, base.Add(time.Nanosecond), nil); err != nil {
		t.Fatalf("RecordInventory(one nanosecond later): %v", err)
	}
	var started, ended string
	if err := s.db.QueryRowContext(ctx, `SELECT started_at, ended_at FROM capability_presence_epochs WHERE runtime = ? AND capability_type = ? AND capability_name = ?`, key.Runtime, key.CapabilityType, key.CapabilityName).Scan(&started, &ended); err != nil {
		t.Fatalf("read fixed epoch timestamps: %v", err)
	}
	if len(started) != len(epochTimestampLayout) || len(ended) != len(epochTimestampLayout) || started >= ended {
		t.Fatalf("fixed epoch timestamps = %q..%q, want fixed-width lexical order", started, ended)
	}
	if started != formatEpochTimestamp(base) || ended != formatEpochTimestamp(base.Add(time.Nanosecond)) {
		t.Fatalf("fixed epoch timestamps = %q..%q, want %q..%q", started, ended, formatEpochTimestamp(base), formatEpochTimestamp(base.Add(time.Nanosecond)))
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO capability_presence_epochs(runtime, capability_type, capability_name, scope, source, started_at, ended_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, capability.Runtime, capability.Type, "bad-check", capability.Scope, "bad-source", formatEpochTimestamp(base.Add(time.Nanosecond)), formatEpochTimestamp(base)); err == nil {
		t.Fatal("reversed fixed timestamps bypassed SQL CHECK")
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO capture_epochs(runtime, started_at, ended_at, start_reason, end_reason) VALUES (?, ?, ?, ?, ?)`, domain.RuntimeCursor, formatEpochTimestamp(base.Add(time.Nanosecond)), formatEpochTimestamp(base), history.CaptureStartReasonConfirmedDirectDelivery, history.CaptureEndReasonConfirmedCaptureFailure); err == nil {
		t.Fatal("reversed capture timestamps bypassed SQL CHECK")
	}
}

func TestRecordInventoryRejectsInvalidScanWithoutPresenceMutation(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	base := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	capability := testCapability("stable", time.Time{}, time.Time{})
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, base, []domain.Capability{capability}); err != nil {
		t.Fatalf("RecordInventory(seed): %v", err)
	}
	invalid := capability
	invalid.Name = " "
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, base.Add(time.Hour), []domain.Capability{invalid}); err == nil {
		t.Fatal("invalid inventory scan accepted")
	}
	epochs, err := s.ListCapabilityPresenceEpochs(ctx)
	if err != nil {
		t.Fatalf("ListCapabilityPresenceEpochs(): %v", err)
	}
	if len(epochs) != 1 || !epochs[0].End.IsZero() {
		t.Fatalf("presence epochs after failed scan = %#v", epochs)
	}
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, base.Add(-time.Minute), nil); err == nil || !strings.Contains(err.Error(), "older") {
		t.Fatalf("out-of-order scan error = %v", err)
	}
}

func TestV6UpgradePreservesUsageProvenanceAndDoesNotBackfillEpochs(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v6.db")
	seed, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open(): %v", err)
	}
	for version := 1; version <= 6; version++ {
		name := []string{"001_initial.sql", "002_capability_corrections.sql", "003_capability_advertisement.sql", "004_inventory_scans.sql", "005_usage_event_observations.sql", "006_history_diagnostics.sql"}[version-1]
		migration, readErr := migrations.ReadFile("migrations/" + name)
		if readErr != nil {
			seed.Close()
			t.Fatalf("read migration %s: %v", name, readErr)
		}
		if _, execErr := seed.ExecContext(ctx, string(migration)); execErr != nil {
			seed.Close()
			t.Fatalf("apply migration %s: %v", name, execErr)
		}
	}
	base := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	for index, provenance := range []domain.Provenance{domain.ProvenanceHook, domain.ProvenanceTranscript, domain.ProvenanceImport} {
		event := testUsageEvent(base.Add(time.Duration(index)*time.Minute), "v6-"+string(provenance), domain.EventInvoked)
		if _, err := seed.ExecContext(ctx, `INSERT INTO usage_events(timestamp, observed_at, source_timestamp, provenance, schema_version, invocation_origin, source_identity, runtime, session_id, project_id, capability_type, capability_name, event_type, fingerprint) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, event.ObservedAt.Format(time.RFC3339Nano), event.ObservedAt.Format(time.RFC3339Nano), nil, provenance, event.SchemaVersion, event.InvocationOrigin, "", event.Runtime, event.SessionID, event.ProjectID, event.CapabilityType, event.CapabilityName, event.EventType, "v6-"+string(provenance)); err != nil {
			seed.Close()
			t.Fatalf("seed %s usage: %v", provenance, err)
		}
		if _, err := seed.ExecContext(ctx, `INSERT INTO usage_event_evidence(fingerprint, provenance, observed_at, source_timestamp, invocation_origin, source_identity) VALUES (?, ?, ?, ?, ?, ?)`, "v6-"+string(provenance), provenance, event.ObservedAt.Format(time.RFC3339Nano), nil, event.InvocationOrigin, ""); err != nil {
			seed.Close()
			t.Fatalf("seed %s evidence: %v", provenance, err)
		}
	}
	if _, err := seed.ExecContext(ctx, `INSERT INTO schema_meta(key, value) VALUES ('version', '6')`); err != nil {
		seed.Close()
		t.Fatalf("seed schema version: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed: %v", err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(v6): %v", err)
	}
	defer s.Close()
	status, err := s.SchemaStatus(ctx)
	if err != nil || status != (SchemaStatus{Current: 7, Latest: 7}) {
		t.Fatalf("v6 upgrade status = %#v, error %v", status, err)
	}
	events, err := s.ListUsageEvents(ctx, time.Time{})
	if err != nil || len(events) != 3 {
		t.Fatalf("v6 usage events = %#v, error %v", events, err)
	}
	for _, provenance := range []domain.Provenance{domain.ProvenanceHook, domain.ProvenanceTranscript, domain.ProvenanceImport} {
		evidence, evidenceErr := s.ListUsageEventEvidence(ctx, "v6-"+string(provenance))
		if evidenceErr != nil || len(evidence) != 1 || evidence[0].Provenance != provenance {
			t.Fatalf("v6 %s evidence = %#v, error %v", provenance, evidence, evidenceErr)
		}
	}
	presence, err := s.ListCapabilityPresenceEpochs(ctx)
	if err != nil || len(presence) != 0 {
		t.Fatalf("v6 presence epochs = %#v, error %v", presence, err)
	}
}
