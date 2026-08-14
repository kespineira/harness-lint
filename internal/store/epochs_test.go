package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/history"
)

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
	if err := s.OpenCaptureEpoch(ctx, domain.RuntimeCodex, base.Add(time.Minute), history.CaptureStartReasonConfirmedDirectDelivery); err == nil {
		t.Fatal("second open while active succeeded")
	}
	if err := s.CloseCaptureEpoch(ctx, domain.RuntimeCodex, base.Add(time.Hour), history.CaptureEndReasonConfirmedCaptureFailure); err != nil {
		t.Fatalf("CloseCaptureEpoch(): %v", err)
	}
	if err := s.CloseCaptureEpoch(ctx, domain.RuntimeCodex, base.Add(time.Hour), history.CaptureEndReasonConfirmedCaptureFailure); err != nil {
		t.Fatalf("idempotent CloseCaptureEpoch(): %v", err)
	}
	if err := s.CloseCaptureEpoch(ctx, domain.RuntimeCodex, base.Add(2*time.Hour), history.CaptureEndReasonConfirmedCaptureFailure); err == nil {
		t.Fatal("second close succeeded")
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
	key := history.CoverageKey{Runtime: domain.RuntimeCodex, CapabilityType: capability.Type, CapabilityName: capability.Name}
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
	want := []history.CapabilityPresenceEpoch{
		{CoverageKey: key, Interval: history.Interval{Start: base.UTC(), End: base.Add(time.Hour).UTC()}},
		{CoverageKey: key, Interval: history.Interval{Start: base.Add(2 * time.Hour).UTC()}},
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
