package presentation

import (
	"strings"
)

// Style identifies a deliberately small ANSI palette.  Styles decorate
// already-meaningful text; no status is communicated by color alone.
type Style uint8

const (
	StyleNone Style = iota
	StyleSuccess
	StyleWarning
	StyleError
	StyleMuted
)

// Tone is a readable alias for Style for callers that prefer semantic names.
type Tone = Style

const (
	ToneNone    = StyleNone
	ToneSuccess = StyleSuccess
	ToneWarning = StyleWarning
	ToneError   = StyleError
	ToneMuted   = StyleMuted
)

// Colorize applies a single ANSI SGR style when enabled.  Disabled output is
// byte-for-byte the supplied text, which keeps redirection and goldens stable.
func Colorize(value string, style Style, enabled bool) string {
	if !enabled || style == StyleNone {
		return value
	}
	code := styleCode(style)
	if code == "" {
		return value
	}
	return "\x1b[" + code + "m" + value + "\x1b[0m"
}

// StyleText is an explicit alias for Colorize.
func StyleText(value string, style Style, enabled bool) string {
	return Colorize(value, style, enabled)
}

func styleCode(style Style) string {
	switch style {
	case StyleSuccess:
		return "32"
	case StyleWarning:
		return "33"
	case StyleError:
		return "31"
	case StyleMuted:
		return "2"
	default:
		return ""
	}
}

// StripANSI removes the small CSI/SGR sequences used by this package.  It is
// exported to make ANSI-aware tests and capture consumers straightforward.
func StripANSI(value string) string {
	var builder strings.Builder
	for position := 0; position < len(value); {
		if value[position] == 0x1b && position+1 < len(value) && value[position+1] == '[' {
			position += 2
			for position < len(value) {
				byteValue := value[position]
				position++
				if byteValue >= 0x40 && byteValue <= 0x7e {
					break
				}
			}
			continue
		}
		builder.WriteByte(value[position])
		position++
	}
	return builder.String()
}

// StatusInfo is the semantic, color-independent representation of a status.
type StatusInfo struct {
	Symbol string
	Word   string
	Style  Style
}

// StatusInfoFor maps the common status vocabulary used by command views to a
// stable symbol, human word, and optional style.  Unknown values retain a
// humanized word and use a question mark so missing knowledge is explicit.
func StatusInfoFor[T ~string](value T) StatusInfo {
	key := NormalizeStatusKey(string(value))
	switch key {
	case "ok", "healthy", "active", "enabled", "installed", "configured", "advertised", "loaded", "used", "keep", "success", "complete", "current", "available":
		return StatusInfo{Symbol: "✓", Word: statusWord(key), Style: StyleSuccess}
	case "warning", "warn", "review", "stale", "partial", "degraded", "usage_only", "estimated", "partially_installed":
		return StatusInfo{Symbol: "!", Word: statusWord(key), Style: StyleWarning}
	case "error", "failed", "failure", "broken", "dead", "invalid", "unavailable", "malformed":
		return StatusInfo{Symbol: "✗", Word: statusWord(key), Style: StyleError}
	case "idle", "disabled", "inactive", "not_configured", "not_installed", "none", "never", "not_observed", "no_activity_observed", "not_available":
		return StatusInfo{Symbol: "-", Word: statusWord(key), Style: StyleMuted}
	case "unknown", "":
		return StatusInfo{Symbol: "?", Word: statusWord(key), Style: StyleMuted}
	default:
		return StatusInfo{Symbol: "?", Word: humanizeStatus(key), Style: StyleMuted}
	}
}

// StatusSymbol returns the semantic status symbol without ANSI styling.
func StatusSymbol[T ~string](value T) string {
	return StatusInfoFor(value).Symbol
}

// StatusWord returns the semantic human word without ANSI styling.
func StatusWord[T ~string](value T) string {
	return StatusInfoFor(value).Word
}

// RenderStatus renders both the symbol and the human word.  Color is strictly
// optional and never carries the status meaning by itself.
func RenderStatus[T ~string](value T, color bool) string {
	status := StatusInfoFor(value)
	return Colorize(status.Symbol+" "+status.Word, status.Style, color)
}

func statusWord(key string) string {
	switch key {
	case "ok":
		return "OK"
	case "keep":
		return "Keep"
	case "review":
		return "Review"
	case "stale":
		return "Stale"
	case "dead":
		return "Dead"
	case "not_observed":
		return "Not observed"
	case "no_activity_observed":
		return "No activity observed"
	case "usage_only":
		return "Usage only"
	case "not_installed":
		return "Not installed"
	case "partially_installed":
		return "Partially installed"
	case "not_configured":
		return "Not configured"
	default:
		return humanizeStatus(key)
	}
}

func humanizeStatus(value string) string {
	if value == "" {
		return "Unknown"
	}
	words := strings.Fields(strings.ReplaceAll(value, "_", " "))
	for position, word := range words {
		if word == "mcp" {
			words[position] = "MCP"
			continue
		}
		if word == "" {
			continue
		}
		words[position] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, " ")
}
