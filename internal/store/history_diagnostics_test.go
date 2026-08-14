package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/capture"
	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/history"
)

func TestIngestUsageEventUpdatesHealthAtomicallyAndDeduplicatesRetry(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	firstAt := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	event := testUsageEvent(firstAt, "terminal", domain.EventInvoked)
	event.Provenance = domain.ProvenanceHook
	event.SourceIdentity = "hook-delivery-1"
	if err := s.IngestUsageEvent(ctx, event); err != nil {
		t.Fatalf("IngestUsageEvent(first): %v", err)
	}
	retry := event
	retry.ObservedAt = firstAt.Add(time.Minute)
	if err := s.IngestUsageEvent(ctx, retry); err != nil {
		t.Fatalf("IngestUsageEvent(retry): %v", err)
	}
	events, err := s.ListUsageEvents(ctx, time.Time{})
	if err != nil {
		t.Fatalf("ListUsageEvents(): %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("usage events after retry = %d, want 1", len(events))
	}
	health, err := s.GetCaptureHealth(ctx, domain.RuntimeCodex)
	if err != nil {
		t.Fatalf("GetCaptureHealth(): %v", err)
	}
	if health.ConsecutiveFailures != 0 || health.LastSuccessfulDelivery == nil || !health.LastSuccessfulDelivery.Equal(retry.ObservedAt) {
		t.Fatalf("capture health after retry = %#v", health)
	}
	if health.LastFailedDelivery != nil || health.LastFailureKind != nil {
		t.Fatalf("unexpected failure state after successful delivery = %#v", health)
	}
	if err := s.IngestUsageEvent(ctx, domain.UsageEvent{Provenance: domain.ProvenanceTranscript}); err == nil {
		t.Fatal("transcript event accepted by direct hook ingest")
	}
	var evidenceCount int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_event_evidence`).Scan(&evidenceCount); err != nil {
		t.Fatalf("evidence count: %v", err)
	}
	if evidenceCount != 1 {
		t.Fatalf("evidence count = %d, want one hook relation", evidenceCount)
	}
}

func TestCaptureHealthBoundedFailureThenSuccessAndPrivacySafe(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	base := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	for i := 0; i < capture.MaxConsecutiveFailures+5; i++ {
		if err := s.RecordCaptureFailure(ctx, capture.CaptureFailure{
			Runtime:  domain.RuntimeClaude,
			FailedAt: base.Add(time.Duration(i) * time.Minute),
			Kind:     capture.FailureDatabaseUnavailable,
		}); err != nil {
			t.Fatalf("RecordCaptureFailure(%d): %v", i, err)
		}
	}
	health, err := s.GetCaptureHealth(ctx, domain.RuntimeClaude)
	if err != nil {
		t.Fatalf("GetCaptureHealth(): %v", err)
	}
	if health.ConsecutiveFailures != capture.MaxConsecutiveFailures {
		t.Fatalf("bounded failures = %#v, want max %d", health, capture.MaxConsecutiveFailures)
	}
	if health.LastFailedDelivery == nil || !health.LastFailedDelivery.Equal(base.Add(time.Duration(capture.MaxConsecutiveFailures+4)*time.Minute)) {
		t.Fatalf("last failed delivery = %#v", health.LastFailedDelivery)
	}
	if health.LastFailureKind == nil || *health.LastFailureKind != capture.FailureDatabaseUnavailable {
		t.Fatalf("last failure kind = %#v", health.LastFailureKind)
	}
	sentinel := "raw diagnostic error should never persist"
	if err := s.RecordCaptureFailure(ctx, capture.CaptureFailure{Runtime: domain.RuntimeClaude, FailedAt: base, Kind: capture.FailureKind(sentinel)}); err == nil {
		t.Fatal("arbitrary failure kind accepted")
	}
	event := testUsageEvent(base.Add(time.Hour), "terminal", domain.EventInvoked)
	event.Provenance = domain.ProvenanceHook
	event.Runtime = domain.RuntimeClaude
	if err := s.IngestUsageEvent(ctx, event); err != nil {
		t.Fatalf("successful ingest after failures: %v", err)
	}
	health, err = s.GetCaptureHealth(ctx, domain.RuntimeClaude)
	if err != nil {
		t.Fatalf("GetCaptureHealth() after success: %v", err)
	}
	if health.ConsecutiveFailures != 0 || health.LastFailureKind == nil || *health.LastFailureKind != capture.FailureDatabaseUnavailable {
		t.Fatalf("failure history after success = %#v", health)
	}
	row, err := s.db.QueryContext(ctx, `SELECT * FROM capture_delivery_health`)
	if err != nil {
		t.Fatalf("query capture health: %v", err)
	}
	defer row.Close()
	columns, err := row.Columns()
	if err != nil {
		t.Fatalf("capture health columns: %v", err)
	}
	values := make([]any, len(columns))
	destinations := make([]any, len(columns))
	for i := range values {
		destinations[i] = &values[i]
	}
	if !row.Next() {
		t.Fatal("capture health row missing")
	}
	if err := row.Scan(destinations...); err != nil {
		t.Fatalf("scan capture health: %v", err)
	}
	for i, value := range values {
		if strings.Contains(fmt.Sprint(value), sentinel) {
			t.Fatalf("capture diagnostic column %q contains raw sentinel", columns[i])
		}
	}
}

func TestSelfTestCaptureIngestAlwaysRollsBackUsageEvidenceAndHealth(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seed := testUsageEvent(time.Date(2026, 8, 14, 10, 30, 0, 0, time.UTC), "existing", domain.EventInvoked)
	seed.Provenance = domain.ProvenanceHook
	seed.SourceIdentity = "existing-hook"
	if err := s.IngestUsageEvent(ctx, seed); err != nil {
		t.Fatalf("seed IngestUsageEvent(): %v", err)
	}
	if err := s.RecordCaptureFailure(ctx, capture.CaptureFailure{Runtime: domain.RuntimeCodex, FailedAt: seed.ObservedAt.Add(time.Minute), Kind: capture.FailureDatabaseBusy}); err != nil {
		t.Fatalf("seed RecordCaptureFailure(): %v", err)
	}
	before, err := s.captureSelfTestState(ctx)
	if err != nil {
		t.Fatalf("capture self-test state before: %v", err)
	}
	if err := s.SelfTestCaptureIngest(ctx); err != nil {
		t.Fatalf("SelfTestCaptureIngest(): %v", err)
	}
	after, err := s.captureSelfTestState(ctx)
	if err != nil {
		t.Fatalf("capture self-test state after: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("self-test changed persisted state: before=%#v after=%#v", before, after)
	}
	for _, table := range []string{"usage_events", "usage_event_evidence", "capture_delivery_health"} {
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
			t.Fatalf("count %s after self-test: %v", table, err)
		}
		if count == 0 && table != "capture_delivery_health" {
			t.Fatalf("expected seeded rows in %s", table)
		}
	}
}

func TestHistoryAggregatesInventoryUsageEvidenceAndAdvertisedSessions(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	observed := time.Date(2026, 8, 14, 11, 0, 0, 0, time.UTC)
	capability := testCapability("installed-only", observed, observed)
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, observed, []domain.Capability{capability}); err != nil {
		t.Fatalf("RecordInventory(): %v", err)
	}
	hook := testUsageEvent(observed.Add(time.Minute), "terminal", domain.EventInvoked)
	hook.Provenance = domain.ProvenanceHook
	hook.SourceIdentity = "stable-runtime-invocation"
	if err := s.IngestUsageEvent(ctx, hook); err != nil {
		t.Fatalf("IngestUsageEvent(): %v", err)
	}
	transcript := hook
	transcript.Provenance = domain.ProvenanceTranscript
	transcript.ObservedAt = observed.Add(2 * time.Minute)
	sourceAt := observed.Add(-time.Minute)
	transcript.SourceTimestamp = &sourceAt
	if err := s.InsertUsageEvents(ctx, []domain.UsageEvent{transcript}); err != nil {
		t.Fatalf("InsertUsageEvents(transcript): %v", err)
	}
	advertised := testUsageEvent(observed.Add(3*time.Minute), "terminal", domain.EventAdvertised)
	advertised.Provenance = domain.ProvenanceTranscript
	advertised.SessionID = "advertised-session"
	if err := s.InsertUsageEvents(ctx, []domain.UsageEvent{advertised}); err != nil {
		t.Fatalf("InsertUsageEvents(advertised): %v", err)
	}
	rows, err := s.QueryInvocationHistory(ctx, history.Query{Runtime: domain.RuntimeCodex})
	if err != nil {
		t.Fatalf("QueryInvocationHistory(): %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("history rows = %#v, want installed-only and usage-only keys", rows)
	}
	var installed, used history.Aggregate
	for _, row := range rows {
		switch row.CapabilityName {
		case "installed-only":
			installed = row
		case "terminal":
			used = row
		}
	}
	if !installed.Installed || !reflect.DeepEqual(installed.InstalledScopes, []domain.Scope{domain.ScopeProject}) || installed.Uses != 0 {
		t.Fatalf("installed zero-use aggregate = %#v", installed)
	}
	if installed.AdvertisedObservations != 0 || installed.LoadedObservations != 0 || installed.ObservedAdvertisedSessions != nil {
		t.Fatalf("installed zero-use observation counts = %#v", installed)
	}
	if installed.Coverage == nil || installed.Coverage.FirstInventoryObservedAt == nil || !installed.Coverage.FirstInventoryObservedAt.Equal(observed) || installed.Coverage.LastInventoryObservedAt == nil || !installed.Coverage.LastInventoryObservedAt.Equal(observed) || installed.Coverage.FirstUsageObservedAt != nil || installed.Coverage.LastUsageObservedAt != nil || installed.Coverage.FirstDirectHookObservedAt != nil || installed.Coverage.LastDirectHookObservedAt != nil {
		t.Fatalf("installed zero-use coverage = %#v", installed.Coverage)
	}
	if used.Installed || used.Uses != 1 || used.DistinctInvocationSessions != 1 || used.AdvertisedObservations != 1 || used.LoadedObservations != 0 {
		t.Fatalf("usage aggregate identity counts = %#v", used)
	}
	if used.InvocationEvidence[domain.ProvenanceHook] != 1 || used.InvocationEvidence[domain.ProvenanceTranscript] != 1 {
		t.Fatalf("usage evidence counts = %#v", used.InvocationEvidence)
	}
	if used.ObservedAdvertisedSessions == nil || *used.ObservedAdvertisedSessions != 1 {
		t.Fatalf("advertised sessions = %#v, want known one", used.ObservedAdvertisedSessions)
	}
	if used.FirstObservedAt == nil || !used.FirstObservedAt.Equal(hook.ObservedAt) || used.FirstEffectiveActivityAt == nil || !used.FirstEffectiveActivityAt.Equal(sourceAt) {
		t.Fatalf("canonical observed/effective times = %#v/%#v", used.FirstObservedAt, used.FirstEffectiveActivityAt)
	}
	if used.Coverage == nil || used.Coverage.FirstInventoryObservedAt != nil || used.Coverage.LastInventoryObservedAt != nil || used.Coverage.FirstUsageObservedAt == nil || !used.Coverage.FirstUsageObservedAt.Equal(hook.ObservedAt) || used.Coverage.LastUsageObservedAt == nil || !used.Coverage.LastUsageObservedAt.Equal(advertised.ObservedAt) || used.Coverage.FirstDirectHookObservedAt == nil || !used.Coverage.FirstDirectHookObservedAt.Equal(hook.ObservedAt) || used.Coverage.LastDirectHookObservedAt == nil || !used.Coverage.LastDirectHookObservedAt.Equal(hook.ObservedAt) {
		t.Fatalf("usage-only coverage = %#v", used.Coverage)
	}
	if _, err := s.QueryInvocationHistory(ctx, history.Query{Runtime: domain.RuntimeCodex, CapabilityType: domain.CapabilitySkill}); err != nil {
		t.Fatalf("runtime/type composed filter: %v", err)
	}
	evidence, err := s.ListUsageEventEvidence(ctx, usedFingerprint(t, s, hook))
	if err != nil {
		t.Fatalf("ListUsageEventEvidence(): %v", err)
	}
	if len(evidence) != 2 || evidence[0].Provenance != domain.ProvenanceHook || evidence[1].Provenance != domain.ProvenanceTranscript {
		t.Fatalf("normalized evidence = %#v", evidence)
	}
}

func TestHistoryStateObservationCountsUseClosedIntervalAndStayIndependent(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	start := time.Date(2026, 8, 14, 11, 30, 0, 0, time.UTC)
	end := start.Add(2 * time.Minute)

	installed := testCapability("inventory-only", time.Time{}, time.Time{})
	installed.Type = domain.CapabilityTool
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, start, []domain.Capability{installed}); err != nil {
		t.Fatalf("RecordInventory(): %v", err)
	}

	advertisedAtStart := testUsageEvent(start, "stateful", domain.EventAdvertised)
	advertisedAtStart.SessionID = "advertised-session"
	advertisedAtStart.SourceIdentity = "advertised-start"
	advertisedAtEnd := advertisedAtStart
	advertisedAtEnd.ObservedAt = end
	advertisedAtEnd.SourceIdentity = "advertised-end"
	loadedAtStart := testUsageEvent(start, "stateful", domain.EventLoaded)
	loadedAtStart.SessionID = "loaded-session"
	loadedAtStart.SourceIdentity = "loaded-start"
	invokedAtEnd := testUsageEvent(end, "stateful", domain.EventInvoked)
	invokedAtEnd.SessionID = "invocation-session"
	invokedAtEnd.SourceIdentity = "invoked-end"
	advertisedBefore := testUsageEvent(start.Add(-time.Second), "stateful", domain.EventAdvertised)
	advertisedBefore.SessionID = advertisedAtStart.SessionID
	advertisedBefore.SourceIdentity = "advertised-before"
	loadedAfter := testUsageEvent(end.Add(time.Second), "stateful", domain.EventLoaded)
	loadedAfter.SessionID = loadedAtStart.SessionID
	loadedAfter.SourceIdentity = "loaded-after"
	invokedAfter := testUsageEvent(end.Add(time.Second), "stateful", domain.EventInvoked)
	invokedAfter.SessionID = invokedAtEnd.SessionID
	invokedAfter.SourceIdentity = "invoked-after"

	otherAdvertised := testUsageEvent(start, "other-name", domain.EventAdvertised)
	otherAdvertised.Runtime = domain.RuntimeClaude
	otherAdvertised.SessionID = "other-advertised-session"
	otherAdvertised.SourceIdentity = "other-advertised"
	otherLoaded := testUsageEvent(end, "other-name", domain.EventLoaded)
	otherLoaded.Runtime = domain.RuntimeClaude
	otherLoaded.SessionID = "other-loaded-session"
	otherLoaded.SourceIdentity = "other-loaded"
	if err := s.InsertUsageEvents(ctx, []domain.UsageEvent{
		advertisedAtStart, advertisedAtEnd, loadedAtStart, invokedAtEnd,
		advertisedBefore, loadedAfter, invokedAfter, otherAdvertised, otherLoaded,
	}); err != nil {
		t.Fatalf("InsertUsageEvents(state observations): %v", err)
	}

	rows, err := s.QueryInvocationHistory(ctx, history.Query{CapabilityType: domain.CapabilityTool, Start: start, End: end})
	if err != nil {
		t.Fatalf("QueryInvocationHistory(state observations): %v", err)
	}
	byKey := make(map[string]history.Aggregate, len(rows))
	for _, row := range rows {
		byKey[string(row.Runtime)+"/"+row.CapabilityName] = row
	}
	if len(byKey) != 3 {
		t.Fatalf("state observation rows = %#v, want codex stateful, codex inventory-only, and claude other-name", rows)
	}

	stateful, ok := byKey["codex/stateful"]
	if !ok {
		t.Fatalf("stateful aggregate missing from %#v", byKey)
	}
	if stateful.AdvertisedObservations != 2 || stateful.LoadedObservations != 1 {
		t.Fatalf("state observation counts = %#v, want advertised=2 loaded=1", stateful)
	}
	if stateful.ObservedAdvertisedSessions == nil || *stateful.ObservedAdvertisedSessions != 1 {
		t.Fatalf("state advertised sessions = %#v, want one session", stateful.ObservedAdvertisedSessions)
	}
	if stateful.Uses != 1 || stateful.DistinctInvocationSessions != 1 || stateful.FirstObservedAt == nil || !stateful.FirstObservedAt.Equal(end) || stateful.LastObservedAt == nil || !stateful.LastObservedAt.Equal(end) || stateful.FirstEffectiveActivityAt == nil || !stateful.FirstEffectiveActivityAt.Equal(end) || stateful.LastEffectiveActivityAt == nil || !stateful.LastEffectiveActivityAt.Equal(end) {
		t.Fatalf("state invocation-only aggregate = %#v", stateful)
	}

	inventory := byKey["codex/inventory-only"]
	if !inventory.Installed || inventory.Uses != 0 || inventory.DistinctInvocationSessions != 0 || inventory.AdvertisedObservations != 0 || inventory.LoadedObservations != 0 || inventory.ObservedAdvertisedSessions != nil || inventory.FirstObservedAt != nil || inventory.LastObservedAt != nil || inventory.Coverage == nil || inventory.Coverage.FirstUsageObservedAt != nil || inventory.Coverage.LastUsageObservedAt != nil || inventory.Coverage.FirstDirectHookObservedAt != nil || inventory.Coverage.LastDirectHookObservedAt != nil {
		t.Fatalf("current inventory without interval events = %#v", inventory)
	}

	other := byKey[string(domain.RuntimeClaude)+"/other-name"]
	if other.Uses != 0 || other.DistinctInvocationSessions != 0 || other.AdvertisedObservations != 1 || other.LoadedObservations != 1 || other.ObservedAdvertisedSessions == nil || *other.ObservedAdvertisedSessions != 1 || other.FirstObservedAt != nil || other.LastObservedAt != nil {
		t.Fatalf("multiple runtime/name state aggregate = %#v", other)
	}
}

func TestHistoryCoverageUnknownRemainsNil(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	capability := testCapability("unknown-coverage", time.Time{}, time.Time{})
	if err := s.UpsertCapabilities(ctx, []domain.Capability{capability}); err != nil {
		t.Fatalf("UpsertCapabilities(): %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO current_inventory(runtime, capability_type, name, scope, source) VALUES (?, ?, ?, ?, ?)`, capability.Runtime, capability.Type, capability.Name, capability.Scope, capability.Source); err != nil {
		t.Fatalf("seed current inventory without observation: %v", err)
	}
	rows, err := s.QueryInvocationHistory(ctx, history.Query{Runtime: capability.Runtime, CapabilityType: capability.Type, CapabilityName: capability.Name})
	if err != nil {
		t.Fatalf("QueryInvocationHistory(): %v", err)
	}
	if len(rows) != 1 || rows[0].Coverage != nil {
		t.Fatalf("unknown coverage = %#v, want nil", rows)
	}
}

func TestHistoryCoverageIdentityFiltersIgnorePeriodAndUnrelatedKeys(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	old := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	middle := old.Add(24 * time.Hour)
	recent := old.Add(48 * time.Hour)
	targetCapability := testCapability("target", old, old)
	targetCapability.Type = domain.CapabilityTool
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, old, []domain.Capability{targetCapability}); err != nil {
		t.Fatalf("RecordInventory(target old): %v", err)
	}
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, recent, []domain.Capability{targetCapability}); err != nil {
		t.Fatalf("RecordInventory(target recent): %v", err)
	}
	otherNameCapability := targetCapability
	otherNameCapability.Name = "other-name"
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, recent, []domain.Capability{otherNameCapability}); err != nil {
		t.Fatalf("RecordInventory(other name): %v", err)
	}
	otherRuntimeCapability := targetCapability
	otherRuntimeCapability.Runtime = domain.RuntimeClaude
	otherRuntimeCapability.Name = "other-runtime"
	if err := s.RecordInventory(ctx, domain.RuntimeClaude, recent, []domain.Capability{otherRuntimeCapability}); err != nil {
		t.Fatalf("RecordInventory(other runtime): %v", err)
	}
	targetOld := testUsageEvent(old, "target", domain.EventInvoked)
	targetOld.SourceIdentity = "coverage-target-old"
	targetHook := testUsageEvent(middle, "target", domain.EventInvoked)
	targetHook.Provenance = domain.ProvenanceHook
	targetHook.SourceIdentity = "coverage-target-hook"
	targetRecent := testUsageEvent(recent, "target", domain.EventInvoked)
	targetRecent.SourceIdentity = "coverage-target-recent"
	otherName := testUsageEvent(middle, "other-name", domain.EventInvoked)
	otherName.SourceIdentity = "coverage-other-name"
	otherRuntime := testUsageEvent(middle, "other-runtime", domain.EventInvoked)
	otherRuntime.Runtime = domain.RuntimeClaude
	otherRuntime.SourceIdentity = "coverage-other-runtime"
	if err := s.InsertUsageEvents(ctx, []domain.UsageEvent{targetOld, targetRecent, otherName, otherRuntime}); err != nil {
		t.Fatalf("InsertUsageEvents(coverage identities): %v", err)
	}
	if err := s.IngestUsageEvent(ctx, targetHook); err != nil {
		t.Fatalf("IngestUsageEvent(target hook): %v", err)
	}
	narrow := history.Query{Runtime: domain.RuntimeCodex, CapabilityType: domain.CapabilityTool, CapabilityName: "target", Start: recent, End: recent}
	allTime := narrow
	allTime.Start = time.Time{}
	allTime.End = time.Time{}
	allCoverage, err := s.historyCoverage(ctx, allTime)
	if err != nil {
		t.Fatalf("historyCoverage(all time): %v", err)
	}
	narrowCoverage, err := s.historyCoverage(ctx, narrow)
	if err != nil {
		t.Fatalf("historyCoverage(narrow period): %v", err)
	}
	targetKey := historyKey{runtime: domain.RuntimeCodex, typ: domain.CapabilityTool, name: "target"}
	if len(allCoverage) != 1 || len(narrowCoverage) != 1 || allCoverage[targetKey] == nil || narrowCoverage[targetKey] == nil {
		t.Fatalf("coverage keys = all=%#v narrow=%#v, want only target", allCoverage, narrowCoverage)
	}
	if !reflect.DeepEqual(allCoverage[targetKey], narrowCoverage[targetKey]) {
		t.Fatalf("coverage changed with period filter: all=%#v narrow=%#v", allCoverage[targetKey], narrowCoverage[targetKey])
	}
	rows, err := s.QueryInvocationHistory(ctx, narrow)
	if err != nil {
		t.Fatalf("QueryInvocationHistory(narrow identity/period): %v", err)
	}
	if len(rows) != 1 || rows[0].Runtime != domain.RuntimeCodex || rows[0].CapabilityName != "target" || rows[0].Coverage == nil {
		t.Fatalf("narrow history rows = %#v, want only target with coverage", rows)
	}
	if !reflect.DeepEqual(rows[0].Coverage, allCoverage[targetKey]) {
		t.Fatalf("query result coverage = %#v, want all-time target coverage %#v", rows[0].Coverage, allCoverage[targetKey])
	}
	coverage := rows[0].Coverage
	if coverage.FirstUsageObservedAt == nil || !coverage.FirstUsageObservedAt.Equal(old) || coverage.LastUsageObservedAt == nil || !coverage.LastUsageObservedAt.Equal(recent) || coverage.FirstDirectHookObservedAt == nil || !coverage.FirstDirectHookObservedAt.Equal(middle) || coverage.LastDirectHookObservedAt == nil || !coverage.LastDirectHookObservedAt.Equal(middle) {
		t.Fatalf("target all-time coverage = %#v", coverage)
	}
}

func TestHistoryInvocationTimesExcludeNonInvocationsAndCoverageUsesAllRecordedHistory(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	old := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	invocation := old.Add(24 * time.Hour)
	hookAt := old.Add(48 * time.Hour)
	advertised := old.Add(72 * time.Hour)
	laterInventory := old.Add(96 * time.Hour)
	capability := testCapability("covered", old, old)
	capability.Type = domain.CapabilityTool
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, old, []domain.Capability{capability}); err != nil {
		t.Fatalf("RecordInventory(old): %v", err)
	}
	loaded := testUsageEvent(old, "covered", domain.EventLoaded)
	olderInvocation := testUsageEvent(invocation, "covered", domain.EventInvoked)
	advertisedEvent := testUsageEvent(advertised, "covered", domain.EventAdvertised)
	if err := s.InsertUsageEvents(ctx, []domain.UsageEvent{loaded, olderInvocation, advertisedEvent}); err != nil {
		t.Fatalf("InsertUsageEvents(history): %v", err)
	}
	hook := testUsageEvent(hookAt, "covered", domain.EventInvoked)
	hook.Provenance = domain.ProvenanceHook
	hook.SourceIdentity = "coverage-hook"
	if err := s.IngestUsageEvent(ctx, hook); err != nil {
		t.Fatalf("IngestUsageEvent(hook): %v", err)
	}
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, laterInventory, []domain.Capability{capability}); err != nil {
		t.Fatalf("RecordInventory(later): %v", err)
	}
	rows, err := s.QueryInvocationHistory(ctx, history.Query{Runtime: domain.RuntimeCodex, CapabilityType: domain.CapabilityTool, CapabilityName: "covered", Start: hookAt, End: hookAt})
	if err != nil {
		t.Fatalf("QueryInvocationHistory(narrow): %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("narrow history rows = %#v", rows)
	}
	aggregate := rows[0]
	if aggregate.Uses != 1 || aggregate.FirstObservedAt == nil || !aggregate.FirstObservedAt.Equal(hookAt) || aggregate.LastObservedAt == nil || !aggregate.LastObservedAt.Equal(hookAt) || aggregate.FirstEffectiveActivityAt == nil || !aggregate.FirstEffectiveActivityAt.Equal(hookAt) || aggregate.LastEffectiveActivityAt == nil || !aggregate.LastEffectiveActivityAt.Equal(hookAt) {
		t.Fatalf("invocation timestamps include non-invocations or wrong period = %#v", aggregate)
	}
	if aggregate.Coverage == nil {
		t.Fatal("coverage is nil for recorded history")
	}
	coverage := aggregate.Coverage
	if coverage.FirstInventoryObservedAt == nil || !coverage.FirstInventoryObservedAt.Equal(old) || coverage.LastInventoryObservedAt == nil || !coverage.LastInventoryObservedAt.Equal(laterInventory) {
		t.Fatalf("inventory coverage = %#v", coverage)
	}
	if coverage.FirstUsageObservedAt == nil || !coverage.FirstUsageObservedAt.Equal(old) || coverage.LastUsageObservedAt == nil || !coverage.LastUsageObservedAt.Equal(advertised) {
		t.Fatalf("usage coverage = %#v", coverage)
	}
	if coverage.FirstDirectHookObservedAt == nil || !coverage.FirstDirectHookObservedAt.Equal(hookAt) || coverage.LastDirectHookObservedAt == nil || !coverage.LastDirectHookObservedAt.Equal(hookAt) {
		t.Fatalf("direct-hook coverage = %#v", coverage)
	}
}

func TestHistoryFallbackAndAdvertisedUnknownRemainConservative(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	base := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	first := testUsageEvent(base, "fallback", domain.EventInvoked)
	first.Provenance = domain.ProvenanceHook
	second := first
	second.Provenance = domain.ProvenanceTranscript
	second.ObservedAt = base.Add(time.Second)
	if err := s.InsertUsageEvents(ctx, []domain.UsageEvent{first, second}); err != nil {
		t.Fatalf("InsertUsageEvents(fallback): %v", err)
	}
	rows, err := s.QueryInvocationHistory(ctx, history.Query{Start: base, End: base.Add(time.Second)})
	if err != nil {
		t.Fatalf("QueryInvocationHistory(): %v", err)
	}
	if len(rows) != 1 || rows[0].Uses != 2 || rows[0].DistinctInvocationSessions != 1 {
		t.Fatalf("fallback aggregation = %#v, want two conservative uses", rows)
	}
	if rows[0].ObservedAdvertisedSessions != nil {
		t.Fatalf("advertised sessions without advertised evidence = %#v, want unknown", rows[0].ObservedAdvertisedSessions)
	}
	if rows[0].AdvertisedObservations != 0 || rows[0].LoadedObservations != 0 {
		t.Fatalf("fallback state counts = %#v, want zero", rows[0])
	}
}

func TestMonthlyHistoryUsesClosedUTCIntervalAndQueryPlanIndex(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	start := time.Date(2026, 8, 31, 23, 59, 59, 0, time.UTC)
	end := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for index, at := range []time.Time{start, end, end.Add(time.Second)} {
		event := testUsageEvent(at, "monthly", domain.EventInvoked)
		event.SourceIdentity = fmt.Sprintf("monthly-%d", index)
		if err := s.InsertUsageEvents(ctx, []domain.UsageEvent{event}); err != nil {
			t.Fatalf("InsertUsageEvents(%d): %v", index, err)
		}
	}
	otherName := testUsageEvent(start, "other-monthly", domain.EventInvoked)
	otherName.SourceIdentity = "other-monthly-0"
	if err := s.InsertUsageEvents(ctx, []domain.UsageEvent{otherName}); err != nil {
		t.Fatalf("InsertUsageEvents(other name): %v", err)
	}
	monthly, err := s.QueryMonthlyInvocations(ctx, history.Query{Start: start, End: end, Runtime: domain.RuntimeCodex, CapabilityType: domain.CapabilityTool})
	if err != nil {
		t.Fatalf("QueryMonthlyInvocations(): %v", err)
	}
	if len(monthly) != 3 {
		t.Fatalf("monthly closed-boundary rows = %#v", monthly)
	}
	monthlyByNameMonth := make(map[string]int64, len(monthly))
	for _, aggregate := range monthly {
		monthlyByNameMonth[aggregate.CapabilityName+"/"+aggregate.Month.Format("2006-01")] = aggregate.Uses
	}
	if monthlyByNameMonth["monthly/2026-08"] != 1 || monthlyByNameMonth["monthly/2026-09"] != 1 || monthlyByNameMonth["other-monthly/2026-08"] != 1 {
		t.Fatalf("monthly name/month buckets = %#v", monthlyByNameMonth)
	}
	plan, err := s.ExplainHistoryQueryPlan(ctx, history.Query{Runtime: domain.RuntimeCodex, CapabilityType: domain.CapabilityTool, Start: start, End: end})
	if err != nil {
		t.Fatalf("ExplainHistoryQueryPlan(): %v", err)
	}
	joined := strings.Join(plan, "\n")
	if !strings.Contains(joined, "usage_events_history_filter_idx") {
		t.Fatalf("history query plan = %q, want v6 filtered index", joined)
	}
}

func TestHistoryAggregationHandlesMultipleRuntimesAndThousandsOfRows(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	base := time.Date(2026, 8, 14, 14, 0, 0, 0, time.UTC)
	const rows = 2500
	events := make([]domain.UsageEvent, 0, rows+1)
	for index := 0; index < rows; index++ {
		event := testUsageEvent(base.Add(time.Duration(index)*time.Second), "bulk", domain.EventInvoked)
		event.SessionID = fmt.Sprintf("bulk-session-%d", index%17)
		event.SourceIdentity = fmt.Sprintf("bulk-delivery-%d", index)
		events = append(events, event)
	}
	otherRuntime := testUsageEvent(base.Add(time.Hour), "bulk", domain.EventInvoked)
	otherRuntime.Runtime = domain.RuntimeClaude
	otherRuntime.SessionID = "claude-session"
	otherRuntime.SourceIdentity = "claude-delivery"
	events = append(events, otherRuntime)
	if err := s.InsertUsageEvents(ctx, events); err != nil {
		t.Fatalf("InsertUsageEvents(%d): %v", len(events), err)
	}
	rowsByRuntime, err := s.QueryInvocationHistory(ctx, history.Query{Runtime: domain.RuntimeCodex, CapabilityType: domain.CapabilityTool, CapabilityName: "bulk"})
	if err != nil {
		t.Fatalf("QueryInvocationHistory(codex): %v", err)
	}
	if len(rowsByRuntime) != 1 || rowsByRuntime[0].Uses != rows || rowsByRuntime[0].DistinctInvocationSessions != 17 {
		t.Fatalf("bulk aggregate = %#v, want %d uses and 17 sessions", rowsByRuntime, rows)
	}
	allRows, err := s.QueryInvocationHistory(ctx, history.Query{CapabilityType: domain.CapabilityTool, CapabilityName: "bulk"})
	if err != nil {
		t.Fatalf("QueryInvocationHistory(all runtimes): %v", err)
	}
	if len(allRows) != 2 || allRows[0].Runtime != domain.RuntimeClaude || allRows[1].Runtime != domain.RuntimeCodex {
		t.Fatalf("multiple-runtime history rows = %#v", allRows)
	}
}

func TestV5MigrationBackfillsEvidenceAndRunsIdempotently(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v5.db")
	seed, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("seed sql.Open(): %v", err)
	}
	for _, name := range []string{"001_initial.sql", "002_capability_corrections.sql", "003_capability_advertisement.sql", "004_inventory_scans.sql", "005_usage_event_observations.sql"} {
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
	firstSeen := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	lastSeen := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	legacyCapability := testCapability("legacy", firstSeen, lastSeen)
	if _, err := seed.ExecContext(ctx, `INSERT INTO capabilities(runtime, capability_type, name, scope, source, enabled_state, advertisement_state, hash, metadata_tokens_value, metadata_tokens_confidence, metadata_tokens_basis, body_tokens_value, body_tokens_confidence, body_tokens_basis, first_seen, last_seen) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, legacyCapability.Runtime, legacyCapability.Type, legacyCapability.Name, legacyCapability.Scope, legacyCapability.Source, legacyCapability.Enabled, legacyCapability.Advertisement, legacyCapability.Hash, legacyCapability.MetadataTokens.Value, legacyCapability.MetadataTokens.Confidence, legacyCapability.MetadataTokens.Basis, legacyCapability.BodyTokens.Value, legacyCapability.BodyTokens.Confidence, legacyCapability.BodyTokens.Basis, legacyCapability.FirstSeen.Format(time.RFC3339Nano), legacyCapability.LastSeen.Format(time.RFC3339Nano)); err != nil {
		seed.Close()
		t.Fatalf("seed v5 capability: %v", err)
	}
	if _, err := seed.ExecContext(ctx, `INSERT INTO inventory_scans(runtime, observed_at) VALUES (?, ?)`, legacyCapability.Runtime, legacyCapability.LastSeen.Format(time.RFC3339Nano)); err != nil {
		seed.Close()
		t.Fatalf("seed v5 inventory scan: %v", err)
	}
	if _, err := seed.ExecContext(ctx, `INSERT INTO current_inventory(runtime, capability_type, name, scope, source) VALUES (?, ?, ?, ?, ?)`, legacyCapability.Runtime, legacyCapability.Type, legacyCapability.Name, legacyCapability.Scope, legacyCapability.Source); err != nil {
		seed.Close()
		t.Fatalf("seed v5 current inventory: %v", err)
	}
	event := testUsageEvent(time.Date(2026, 8, 14, 13, 0, 0, 0, time.UTC), "legacy", domain.EventInvoked)
	if _, err := seed.ExecContext(ctx, `INSERT INTO schema_meta(key, value) VALUES ('version', '5')`); err != nil {
		seed.Close()
		t.Fatalf("seed schema version: %v", err)
	}
	if _, err := seed.ExecContext(ctx, `INSERT INTO usage_events(timestamp, observed_at, source_timestamp, provenance, schema_version, invocation_origin, source_identity, runtime, session_id, project_id, capability_type, capability_name, event_type, fingerprint) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, event.ObservedAt.Format(time.RFC3339Nano), event.ObservedAt.Format(time.RFC3339Nano), nil, event.Provenance, event.SchemaVersion, event.InvocationOrigin, "", event.Runtime, event.SessionID, event.ProjectID, event.CapabilityType, event.CapabilityName, event.EventType, "legacy-v5-fingerprint"); err != nil {
		seed.Close()
		t.Fatalf("seed usage event: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seeded v5 db: %v", err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(v5): %v", err)
	}
	status, err := s.SchemaStatus(ctx)
	if err != nil {
		t.Fatalf("SchemaStatus(v5 upgrade): %v", err)
	}
	if status != (SchemaStatus{Current: 7, Latest: 7}) {
		t.Fatalf("v5 upgraded schema status = %#v, want 7/7", status)
	}
	capabilities, err := s.ListCapabilities(ctx)
	if err != nil {
		t.Fatalf("ListCapabilities(v5 upgrade): %v", err)
	}
	if len(capabilities) != 1 || !reflect.DeepEqual(capabilities[0], legacyCapability) {
		t.Fatalf("v5 capabilities after upgrade = %#v, want %#v", capabilities, []domain.Capability{legacyCapability})
	}
	current, err := s.ListCurrentCapabilities(ctx, domain.RuntimeCodex)
	if err != nil {
		t.Fatalf("ListCurrentCapabilities(v5 upgrade): %v", err)
	}
	if len(current) != 1 || !reflect.DeepEqual(current[0], legacyCapability) {
		t.Fatalf("v5 current inventory after upgrade = %#v, want %#v", current, []domain.Capability{legacyCapability})
	}
	events, err := s.ListUsageEvents(ctx, time.Time{})
	if err != nil {
		t.Fatalf("ListUsageEvents(v5 upgrade): %v", err)
	}
	if len(events) != 1 || events[0].Fingerprint != "legacy-v5-fingerprint" || events[0].CapabilityName != "legacy" {
		t.Fatalf("v5 usage after upgrade = %#v", events)
	}
	countEvidence := func() int {
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_event_evidence`).Scan(&count); err != nil {
			t.Fatalf("evidence count: %v", err)
		}
		return count
	}
	if count := countEvidence(); count != 1 {
		t.Fatalf("evidence count after v5 upgrade = %d, want 1", count)
	}
	evidence, err := s.ListUsageEventEvidence(ctx, "legacy-v5-fingerprint")
	if err != nil {
		t.Fatalf("ListUsageEventEvidence(v5 upgrade): %v", err)
	}
	if len(evidence) != 1 || evidence[0].Provenance != domain.ProvenanceImport {
		t.Fatalf("v5 evidence after upgrade = %#v", evidence)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close upgraded db: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen upgraded db: %v", err)
	}
	defer reopened.Close()
	status, err = reopened.SchemaStatus(ctx)
	if err != nil {
		t.Fatalf("SchemaStatus(second open): %v", err)
	}
	if status != (SchemaStatus{Current: 7, Latest: 7}) {
		t.Fatalf("schema status after reopen = %#v, want 7/7", status)
	}
	if err := reopened.migrate(ctx); err != nil {
		t.Fatalf("second no-op migration: %v", err)
	}
	reopenedCapabilities, err := reopened.ListCapabilities(ctx)
	if err != nil {
		t.Fatalf("ListCapabilities(after no-op): %v", err)
	}
	if !reflect.DeepEqual(reopenedCapabilities, capabilities) {
		t.Fatalf("capabilities after reopen/no-op = %#v, want %#v", reopenedCapabilities, capabilities)
	}
	reopenedCurrent, err := reopened.ListCurrentCapabilities(ctx, domain.RuntimeCodex)
	if err != nil {
		t.Fatalf("ListCurrentCapabilities(after no-op): %v", err)
	}
	if !reflect.DeepEqual(reopenedCurrent, current) {
		t.Fatalf("current inventory after reopen/no-op = %#v, want %#v", reopenedCurrent, current)
	}
	reopenedEvents, err := reopened.ListUsageEvents(ctx, time.Time{})
	if err != nil {
		t.Fatalf("ListUsageEvents(after no-op): %v", err)
	}
	if !reflect.DeepEqual(reopenedEvents, events) {
		t.Fatalf("usage after reopen/no-op = %#v, want %#v", reopenedEvents, events)
	}
	var count int
	if err := reopened.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_event_evidence`).Scan(&count); err != nil {
		t.Fatalf("evidence count after second migration run: %v", err)
	}
	if count != 1 {
		t.Fatalf("evidence count after second migration run = %d, want 1", count)
	}
}

func usedFingerprint(t *testing.T, s *Store, event domain.UsageEvent) string {
	t.Helper()
	fingerprint, err := domain.FingerprintForUsageEvent(event)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	return fingerprint
}
