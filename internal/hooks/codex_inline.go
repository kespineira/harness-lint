package hooks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/BurntSushi/toml"
)

func inspectCodexInlineHooks(configRoot string) (bool, string) {
	path := filepath.Join(configRoot, "config.toml")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, ""
	}
	if err != nil {
		return false, "unable to inspect Codex config.toml for inline hooks: " + err.Error()
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, "Codex config.toml is a symlink; inline hook review is unavailable"
	}
	if !info.Mode().IsRegular() {
		return false, "Codex config.toml is not a regular file; inline hook review is unavailable"
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return false, "unable to inspect Codex config.toml for inline hooks: " + err.Error()
	}
	values := make(map[string]any)
	if _, err := toml.Decode(string(content), &values); err != nil {
		return false, "unable to parse Codex config.toml for inline hooks: " + err.Error()
	}
	rawHooks, found := values["hooks"]
	if !found {
		return false, ""
	}
	value := reflect.ValueOf(rawHooks)
	if value.IsValid() && value.Kind() == reflect.Map {
		if value.Len() == 0 {
			return false, ""
		}
		return true, ""
	}
	return false, fmt.Sprintf("Codex config.toml has a non-table hooks value (%T); inline hook review is unavailable", rawHooks)
}

func hasInlineWarning(report StatusReport) bool {
	for _, warning := range report.Warnings {
		if strings.Contains(warning, "inline hooks") {
			return true
		}
	}
	return false
}
