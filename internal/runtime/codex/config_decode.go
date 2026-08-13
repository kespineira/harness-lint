package codex

import "github.com/BurntSushi/toml"

// decodeTOML delegates the supported Codex configuration structures to the
// established TOML implementation. Keeping the result metadata-only avoids
// retaining instruction or agent bodies in capabilities.
func decodeTOML(content []byte) (map[string]any, error) {
	values := make(map[string]any)
	_, err := toml.Decode(string(content), &values)
	return values, err
}
