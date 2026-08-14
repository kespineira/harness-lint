package store

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"modernc.org/sqlite"
)

var (
	// ErrBackupDestinationExists means the requested output already exists.
	ErrBackupDestinationExists = errors.New("backup destination exists")
	// ErrBackupSourceDestinationSame means source and destination identify the same file.
	ErrBackupSourceDestinationSame = errors.New("backup destination is the source database")
	// ErrBackupDestinationParent means the destination parent is not an existing directory.
	ErrBackupDestinationParent = errors.New("backup destination parent is unavailable")
)

const backupPageStep = 64

// Backup creates a consistent SQLite online backup at destination. The
// destination is an explicit, new file; an existing file is never replaced.
// SQLite's Online Backup API provides a snapshot while allowing WAL writers
// to continue, and bounded steps keep cancellation observable between pages.
func (s *Store) Backup(ctx context.Context, destination string) error {
	if s.isClosed() {
		return errors.New("store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	destinationPath, err := backupDestinationPath(destination)
	if err != nil {
		return err
	}
	sourcePath := sqliteFilePath(s.path)
	if sourcePath != "" && sameBackupPath(sourcePath, destinationPath) {
		return ErrBackupSourceDestinationSame
	}

	parent := filepath.Dir(destinationPath)
	parentInfo, err := os.Stat(parent)
	if err != nil || !parentInfo.IsDir() {
		return ErrBackupDestinationParent
	}
	reservation, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrBackupDestinationExists
		}
		return errors.New("reserve backup destination")
	}
	reservationInfo, err := reservation.Stat()
	if err != nil {
		_ = reservation.Close()
		return errors.New("reserve backup destination")
	}

	completed := false
	defer func() {
		if !completed {
			if current, statErr := os.Stat(destinationPath); statErr == nil && os.SameFile(reservationInfo, current) {
				_ = os.Remove(destinationPath)
			}
		}
		_ = reservation.Close()
	}()

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.Raw(func(driverConn any) error {
		backuper, ok := driverConn.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return errors.New("sqlite online backup is unavailable")
		}
		backup, err := backuper.NewBackup(sqliteDSN(destinationPath))
		if err != nil {
			return errors.New("start sqlite online backup")
		}
		for {
			if err := ctx.Err(); err != nil {
				_ = backup.Finish()
				return err
			}
			more, stepErr := backup.Step(backupPageStep)
			if stepErr != nil {
				_ = backup.Finish()
				return errors.New("copy sqlite online backup pages")
			}
			if !more {
				if err := backup.Finish(); err != nil {
					return errors.New("finish sqlite online backup")
				}
				return nil
			}
		}
	}); err != nil {
		return err
	}
	completed = true
	return nil
}

func backupDestinationPath(destination string) (string, error) {
	if strings.TrimSpace(destination) == "" {
		return "", errors.New("backup destination is required")
	}
	if strings.HasPrefix(destination, "file:") || destination == ":memory:" {
		return "", errors.New("backup destination must be a filesystem path")
	}
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return "", errors.New("resolve backup destination")
	}
	return filepath.Clean(absolute), nil
}

// sqliteFilePath strips the optional SQLite file URI/query so identity checks
// compare filesystem paths rather than driver DSNs.
func sqliteFilePath(path string) string {
	if path == "" || path == ":memory:" || strings.HasPrefix(path, "file::memory:") {
		return ""
	}
	if strings.HasPrefix(path, "file:") {
		raw := strings.TrimPrefix(path, "file:")
		if index := strings.IndexByte(raw, '?'); index >= 0 {
			raw = raw[:index]
		}
		if decoded, err := url.PathUnescape(raw); err == nil {
			raw = decoded
		}
		if strings.HasPrefix(raw, "//") {
			raw = strings.TrimPrefix(raw, "//")
			if volume := strings.IndexByte(raw, '/'); volume >= 0 {
				raw = raw[volume:]
			}
		}
		return raw
	}
	if index := strings.IndexByte(path, '?'); index >= 0 {
		return path[:index]
	}
	return path
}

func sameBackupPath(source, destination string) bool {
	sourceAbsolute, sourceErr := filepath.Abs(source)
	destinationAbsolute, destinationErr := filepath.Abs(destination)
	if sourceErr == nil && destinationErr == nil && filepath.Clean(sourceAbsolute) == filepath.Clean(destinationAbsolute) {
		return true
	}
	sourceReal, sourceErr := filepath.EvalSymlinks(sourceAbsolute)
	destinationReal, destinationErr := filepath.EvalSymlinks(destinationAbsolute)
	if sourceErr != nil || destinationErr != nil {
		return false
	}
	return filepath.Clean(sourceReal) == filepath.Clean(destinationReal)
}

var _ interface {
	Backup(context.Context, string) error
} = (*Store)(nil)
