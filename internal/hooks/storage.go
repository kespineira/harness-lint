package hooks

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func (m *manager) writeNew(data []byte) error {
	if m == nil || m.configPath == "" {
		return errors.New("configuration path is unavailable")
	}
	if err := checkNoSymlinkComponents(m.configPath); err != nil {
		return err
	}
	if err := ensurePrivateDir(filepath.Dir(m.configPath)); err != nil {
		return fmt.Errorf("create configuration parent: %w", err)
	}
	if info, err := os.Lstat(m.configPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("configuration path %s is a symlink; refusing to overwrite it", m.configPath)
		}
		return fmt.Errorf("configuration path %s appeared during install", m.configPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect new configuration path: %w", err)
	}
	if err := atomicWrite(m.configPath, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", m.configPath, err)
	}
	return nil
}

func (m *manager) writeExisting(data []byte, mode os.FileMode) (string, error) {
	if m == nil || m.configPath == "" {
		return "", errors.New("configuration path is unavailable")
	}
	if err := checkNoSymlinkComponents(m.configPath); err != nil {
		return "", err
	}
	info, err := os.Lstat(m.configPath)
	if err != nil {
		return "", fmt.Errorf("recheck configuration path: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("configuration path %s is a symlink; refusing to overwrite it", m.configPath)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("configuration path %s is not a regular file", m.configPath)
	}
	backup, err := backupFile(m.configPath, mode)
	if err != nil {
		return "", err
	}
	if err := atomicWrite(m.configPath, data, mode.Perm()); err != nil {
		return backup, fmt.Errorf("write %s: %w", m.configPath, err)
	}
	return backup, nil
}

func ensurePrivateDir(path string) error {
	path = filepath.Clean(path)
	if path == "." || path == "" {
		return nil
	}
	if err := checkNoSymlinkComponents(path); err != nil {
		return err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(absolute)
	rest := strings.TrimPrefix(absolute, volume)
	separator := string(filepath.Separator)
	if strings.HasPrefix(rest, separator) {
		rest = strings.TrimPrefix(rest, separator)
	}
	current := volume
	if filepath.IsAbs(absolute) {
		current += separator
	}
	for _, part := range strings.Split(rest, separator) {
		if part == "" || part == "." {
			continue
		}
		if current == "" || current == separator || strings.HasSuffix(current, separator) {
			current += part
		} else {
			current = filepath.Join(current, part)
		}
		info, statErr := os.Lstat(current)
		if statErr == nil {
			if !safeDirectoryComponent(current, info) {
				return fmt.Errorf("configuration parent %s is not a directory", current)
			}
			continue
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("inspect configuration parent %s: %w", current, statErr)
		}
		if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create configuration parent %s: %w", current, err)
		}
		info, err = os.Lstat(current)
		if err != nil {
			return fmt.Errorf("recheck configuration parent %s: %w", current, err)
		}
		if !safeDirectoryComponent(current, info) {
			return fmt.Errorf("configuration parent %s became unsafe", current)
		}
	}
	return nil
}

func checkNoSymlinkComponents(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("configuration path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(absolute)
	rest := strings.TrimPrefix(absolute, volume)
	separator := string(filepath.Separator)
	if strings.HasPrefix(rest, separator) {
		rest = strings.TrimPrefix(rest, separator)
	}
	current := volume
	if filepath.IsAbs(absolute) {
		current += separator
	}
	for _, part := range strings.Split(rest, separator) {
		if part == "" || part == "." {
			continue
		}
		if current == "" || current == separator || strings.HasSuffix(current, separator) {
			current += part
		} else {
			current = filepath.Join(current, part)
		}
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			// No existing component remains to be a symlink. The remaining
			// path will be created beneath this regular/nonexistent prefix.
			return nil
		}
		if statErr != nil {
			return fmt.Errorf("inspect path component %s: %w", current, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// Existing symlinked directories such as macOS's /var are safe to
			// traverse when their target is present and a directory. The final
			// configuration file itself is checked separately and never follows
			// a symlink to a regular file.
			resolved, resolveErr := os.Stat(current)
			if resolveErr != nil || !resolved.IsDir() {
				return fmt.Errorf("path component %s is an unsafe or broken symlink", current)
			}
		}
	}
	return nil
}

func safeDirectoryComponent(path string, info os.FileInfo) bool {
	if info == nil {
		return false
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return info.IsDir()
	}
	resolved, err := os.Stat(path)
	return err == nil && resolved.IsDir()
}

func backupFile(path string, mode os.FileMode) (string, error) {
	backupPath, err := nextBackupPath(path)
	if err != nil {
		return "", err
	}
	source, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open configuration for backup: %w", err)
	}
	defer source.Close()
	target, err := os.OpenFile(backupPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return "", fmt.Errorf("create backup %s: %w", backupPath, err)
	}
	removeTarget := true
	defer func() {
		_ = target.Close()
		if removeTarget {
			_ = os.Remove(backupPath)
		}
	}()
	if _, err := io.Copy(target, source); err != nil {
		return "", fmt.Errorf("copy configuration backup: %w", err)
	}
	if err := target.Sync(); err != nil {
		return "", fmt.Errorf("sync configuration backup: %w", err)
	}
	if err := target.Close(); err != nil {
		return "", fmt.Errorf("close configuration backup: %w", err)
	}
	removeTarget = false
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return "", err
	}
	return backupPath, nil
}

func nextBackupPath(path string) (string, error) {
	base := path + ".bak"
	for index := 0; index < 10000; index++ {
		candidate := base
		if index > 0 {
			candidate = fmt.Sprintf("%s.%d", base, index)
		}
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", fmt.Errorf("inspect backup path %s: %w", candidate, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("backup path %s is a symlink; refusing to overwrite it", candidate)
		}
	}
	return "", errors.New("too many existing configuration backups")
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	if err := checkNoSymlinkComponents(directory); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(mode.Perm()); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	removeTemporary = false
	return syncDirectory(directory)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory for sync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		// Directory fsync is not available on every supported filesystem. The
		// file itself was synced and atomically renamed, so this is best effort.
		return nil
	}
	return nil
}
