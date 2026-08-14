package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
)

func TestBackupHealthyPopulatedDatabasePreservesSchemaAndEvents(t *testing.T) {
	ctx := context.Background()
	sourcePath := filepath.Join(t.TempDir(), "source.db")
	destinationPath := filepath.Join(t.TempDir(), "backup.db")
	s, err := Open(sourcePath)
	if err != nil {
		t.Fatalf("Open(source): %v", err)
	}
	defer s.Close()
	events := []domain.UsageEvent{
		testUsageEvent(time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC), "backup-one", domain.EventInvoked),
		testUsageEvent(time.Date(2026, 8, 14, 12, 1, 0, 0, time.UTC), "backup-two", domain.EventLoaded),
	}
	if err := s.InsertUsageEvents(ctx, events); err != nil {
		t.Fatalf("InsertUsageEvents(): %v", err)
	}
	if err := s.Backup(ctx, destinationPath); err != nil {
		t.Fatalf("Backup(): %v", err)
	}

	backup, err := Open(destinationPath)
	if err != nil {
		t.Fatalf("Open(backup): %v", err)
	}
	defer backup.Close()
	status, err := backup.SchemaStatus(ctx)
	if err != nil {
		t.Fatalf("backup SchemaStatus(): %v", err)
	}
	if status != (SchemaStatus{Current: 6, Latest: 6}) {
		t.Fatalf("backup schema = %#v", status)
	}
	got, err := backup.ListUsageEvents(ctx, time.Time{})
	if err != nil {
		t.Fatalf("backup ListUsageEvents(): %v", err)
	}
	if len(got) != len(events) || got[0].CapabilityName != events[0].CapabilityName || got[1].CapabilityName != events[1].CapabilityName {
		t.Fatalf("backup events = %#v, want %#v", got, events)
	}

	original, err := s.ListUsageEvents(ctx, time.Time{})
	if err != nil {
		t.Fatalf("source ListUsageEvents(): %v", err)
	}
	if len(original) != len(events) {
		t.Fatalf("source events after backup = %d, want %d", len(original), len(events))
	}
}

func TestBackupWALSnapshotSurvivesConcurrentWriter(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.db")
	s, err := Open(sourcePath)
	if err != nil {
		t.Fatalf("Open(source): %v", err)
	}
	defer s.Close()
	if _, err := s.db.ExecContext(ctx, `PRAGMA journal_mode = WAL`); err != nil {
		t.Fatalf("enable WAL: %v", err)
	}
	initial := testUsageEvent(time.Date(2026, 8, 14, 13, 0, 0, 0, time.UTC), "before-writer", domain.EventInvoked)
	if err := s.InsertUsageEvents(ctx, []domain.UsageEvent{initial}); err != nil {
		t.Fatalf("seed source: %v", err)
	}

	writer, err := Open(sourcePath)
	if err != nil {
		t.Fatalf("Open(writer): %v", err)
	}
	defer writer.Close()
	tx, err := writer.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin writer transaction: %v", err)
	}
	deferredEvent := testUsageEvent(time.Date(2026, 8, 14, 13, 1, 0, 0, time.UTC), "uncommitted-writer", domain.EventInvoked)
	if _, err := insertUsageEventTx(ctx, tx, deferredEvent); err != nil {
		tx.Rollback()
		t.Fatalf("stage writer event: %v", err)
	}

	destinationPath := filepath.Join(dir, "wal-backup.db")
	if err := s.Backup(ctx, destinationPath); err != nil {
		tx.Rollback()
		t.Fatalf("Backup() with active writer: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit writer event: %v", err)
	}

	backup, err := Open(destinationPath)
	if err != nil {
		t.Fatalf("Open(backup): %v", err)
	}
	defer backup.Close()
	backupEvents, err := backup.ListUsageEvents(ctx, time.Time{})
	if err != nil {
		t.Fatalf("backup ListUsageEvents(): %v", err)
	}
	if len(backupEvents) != 1 || backupEvents[0].CapabilityName != initial.CapabilityName {
		t.Fatalf("backup events = %#v, want committed snapshot before writer event", backupEvents)
	}
	sourceEvents, err := s.ListUsageEvents(ctx, time.Time{})
	if err != nil {
		t.Fatalf("source ListUsageEvents(): %v", err)
	}
	if len(sourceEvents) != 2 {
		t.Fatalf("source events after writer commit = %d, want 2", len(sourceEvents))
	}
}

func TestBackupRejectsCollisionAndSourceDestinationAlias(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.db")
	s, err := Open(sourcePath)
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	defer s.Close()

	if err := s.Backup(ctx, sourcePath); !errors.Is(err, ErrBackupSourceDestinationSame) {
		t.Fatalf("Backup(source): %v, want source/destination error", err)
	}
	collision := filepath.Join(dir, "collision.db")
	if err := os.WriteFile(collision, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("seed collision: %v", err)
	}
	if err := s.Backup(ctx, collision); !errors.Is(err, ErrBackupDestinationExists) {
		t.Fatalf("Backup(collision): %v, want collision error", err)
	}
	contents, err := os.ReadFile(collision)
	if err != nil {
		t.Fatalf("read collision: %v", err)
	}
	if string(contents) != "keep me" {
		t.Fatalf("collision contents = %q, want unchanged", contents)
	}
}

func TestBackupCancellationAndFailureCleanupDoNotLeaveOutput(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "source.db")
	s, err := Open(sourcePath)
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	defer s.Close()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	canceledPath := filepath.Join(t.TempDir(), "canceled.db")
	if err := s.Backup(canceled, canceledPath); !errors.Is(err, context.Canceled) {
		t.Fatalf("Backup(canceled): %v, want context cancellation", err)
	}
	if _, err := os.Stat(canceledPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled output stat = %v, want absent", err)
	}

	missingParentPath := filepath.Join(t.TempDir(), "missing", "failed.db")
	if err := s.Backup(context.Background(), missingParentPath); !errors.Is(err, ErrBackupDestinationParent) {
		t.Fatalf("Backup(missing parent): %v, want parent error", err)
	}
}

func TestBackupErrorsDoNotExposeDestinationSentinel(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "source.db"))
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	defer s.Close()
	sentinel := "private-backup-path-sentinel"
	err = s.Backup(context.Background(), filepath.Join(t.TempDir(), sentinel, "backup.db"))
	if err == nil || strings.Contains(err.Error(), sentinel) {
		t.Fatalf("Backup() error = %v, want non-sensitive error", err)
	}
}
