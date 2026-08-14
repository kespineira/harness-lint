//go:build storage_scale

package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestStorageScale100kMetadataOnly(t *testing.T) {
	ctx := context.Background()
	const eventCount = 100000
	base := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	sourcePath := filepath.Join(t.TempDir(), "storage-scale.db")
	s, err := Open(sourcePath)
	if err != nil {
		t.Fatalf("Open(%q): %v", sourcePath, err)
	}
	defer s.Close()
	if err := seedStorageScaleCoverage(ctx, s, base); err != nil {
		t.Fatalf("seed coverage: %v", err)
	}
	forbiddenPayload := storageScaleForbiddenPayloadLikeData()
	events := metadataOnlyScaleEvents(eventCount, base)
	assertStorageScaleMetadataOnlyInput(t, events, forbiddenPayload)
	started := time.Now()
	if err := s.InsertUsageEvents(ctx, events); err != nil {
		t.Fatalf("InsertUsageEvents(%d): %v", eventCount, err)
	}
	insertionDuration := time.Since(started)

	status, err := s.DatabaseStatus(ctx)
	if err != nil {
		t.Fatalf("DatabaseStatus(): %v", err)
	}
	if status.UsageEventCount != eventCount || status.SizeBytes == nil || *status.SizeBytes <= 0 {
		t.Fatalf("database status = %#v, want %d events and positive file size", status, eventCount)
	}
	wantOldest := base.Add(5 * time.Minute)
	wantLatest := base.AddDate(0, 1, 0).Add(time.Duration(eventCount/2-1) * time.Second).Add(5 * time.Minute)
	if status.OldestObservedAt == nil || !status.OldestObservedAt.Equal(wantOldest) || status.LatestObservedAt == nil || !status.LatestObservedAt.Equal(wantLatest) {
		t.Fatalf("database status observed range = %v/%v, want %s/%s", status.OldestObservedAt, status.LatestObservedAt, wantOldest, wantLatest)
	}
	checkStarted := time.Now()
	check, err := s.CheckDatabase(ctx)
	checkDuration := time.Since(checkStarted)
	if err != nil {
		t.Fatalf("CheckDatabase(): %v", err)
	}
	if !check.Healthy || check.QuickCheck != IntegrityOK || check.ForeignKeyCheck != IntegrityOK || check.Schema != IntegrityOK {
		t.Fatalf("database check = %#v, want healthy", check)
	}

	queryDurations := assertStorageScaleQueries(t, ctx, s, eventCount, base)
	assertStorageScalePrivacy(t, ctx, s, forbiddenPayload, "source", sourcePath)

	backupPath := filepath.Join(t.TempDir(), "storage-scale-backup.db")
	backupStarted := time.Now()
	if err := s.Backup(ctx, backupPath); err != nil {
		t.Fatalf("Backup(): %v", err)
	}
	backupDuration := time.Since(backupStarted)
	backup, err := Open(backupPath)
	if err != nil {
		t.Fatalf("Open(backup): %v", err)
	}
	defer backup.Close()
	backupStatus, err := backup.DatabaseStatus(ctx)
	if err != nil {
		t.Fatalf("backup DatabaseStatus(): %v", err)
	}
	if backupStatus.Schema != status.Schema || backupStatus.UsageEventCount != status.UsageEventCount {
		t.Fatalf("backup status = %#v, source status = %#v", backupStatus, status)
	}
	sourceCounts, err := storageObjectCounts(ctx, s.db)
	if err != nil {
		t.Fatalf("source schema counts: %v", err)
	}
	backupCounts, err := storageObjectCounts(ctx, backup.db)
	if err != nil {
		t.Fatalf("backup schema counts: %v", err)
	}
	if len(sourceCounts) != len(backupCounts) {
		t.Fatalf("backup schema object count keys = %#v, source = %#v", backupCounts, sourceCounts)
	}
	for objectType, sourceCount := range sourceCounts {
		if backupCounts[objectType] != sourceCount {
			t.Fatalf("backup %s count = %d, source = %d", objectType, backupCounts[objectType], sourceCount)
		}
	}
	var sourceEventCount, sourceEvidenceCount, backupEventCount, backupEvidenceCount int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_events`).Scan(&sourceEventCount); err != nil {
		t.Fatalf("source usage row count: %v", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_event_evidence`).Scan(&sourceEvidenceCount); err != nil {
		t.Fatalf("source evidence row count: %v", err)
	}
	if err := backup.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_events`).Scan(&backupEventCount); err != nil {
		t.Fatalf("backup usage row count: %v", err)
	}
	if err := backup.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_event_evidence`).Scan(&backupEvidenceCount); err != nil {
		t.Fatalf("backup evidence row count: %v", err)
	}
	if backupEventCount != sourceEventCount || backupEvidenceCount != sourceEvidenceCount || backupEventCount != int64(eventCount) || backupEvidenceCount != int64(eventCount) {
		t.Fatalf("backup row counts = usage %d/evidence %d, source = %d/%d, want %d/%d", backupEventCount, backupEvidenceCount, sourceEventCount, sourceEvidenceCount, eventCount, eventCount)
	}
	assertStorageScalePrivacy(t, ctx, backup, forbiddenPayload, "backup", backupPath)
	t.Logf("storage scale: events=%d insertion=%s QueryInvocationHistory=%s QueryMonthlyInvocations=%s QueryEffectiveCoverage=%s CheckDatabase=%s Backup=%s database_size_bytes=%d status_count=%d observed_range=%s..%s check=%+v backup_rows=%d/%d query_results=asserted schema=%+v", eventCount, insertionDuration.Round(time.Millisecond), queryDurations.History.Round(time.Millisecond), queryDurations.Monthly.Round(time.Millisecond), queryDurations.Coverage.Round(time.Millisecond), checkDuration.Round(time.Millisecond), backupDuration.Round(time.Millisecond), *status.SizeBytes, status.UsageEventCount, status.OldestObservedAt.Format(time.RFC3339), status.LatestObservedAt.Format(time.RFC3339), check, backupEventCount, backupEvidenceCount, status.Schema)
}
