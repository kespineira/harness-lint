package presentation

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// FormatInteger formats an integer with comma group separators.
func FormatInteger(value int64) string {
	negative := value < 0
	digits := strconv.FormatUint(magnitude(value), 10)
	first := len(digits) % 3
	if first == 0 {
		first = 3
	}

	var builder strings.Builder
	if negative {
		builder.WriteByte('-')
	}
	builder.WriteString(digits[:first])
	for position := first; position < len(digits); position += 3 {
		builder.WriteByte(',')
		builder.WriteString(digits[position : position+3])
	}
	return builder.String()
}

// FormatInt is an alias for FormatInteger.
func FormatInt(value int64) string {
	return FormatInteger(value)
}

// FormatBytes formats bytes using powers of 1024 and IEC unit names.  Values
// below 1024 remain exact bytes; scaled values use at most one decimal place.
func FormatBytes(value int64) string {
	negative := value < 0
	absolute := magnitude(value)

	const base = float64(1024)
	units := [...]string{"B", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	scaled := float64(absolute)
	unit := 0
	for scaled >= base && unit < len(units)-1 {
		scaled /= base
		unit++
	}

	var formatted string
	if unit == 0 {
		formatted = strconv.FormatUint(absolute, 10)
	} else {
		formatted = trimDecimal(strconv.FormatFloat(scaled, 'f', 1, 64))
	}
	if negative {
		formatted = "-" + formatted
	}
	return formatted + " " + units[unit]
}

// FormatByteSize is an alias for FormatBytes.
func FormatByteSize(value int64) string {
	return FormatBytes(value)
}

// FormatTokens formats an estimated token count.  The leading ~ makes the
// estimate explicit even for small values.  Counts at or above one thousand
// use decimal thousands (k), keeping the unit consistent across views.
func FormatTokens(value int64) string {
	negative := value < 0
	absolute := magnitude(value)

	var formatted string
	if absolute < 1000 {
		formatted = strconv.FormatUint(absolute, 10)
	} else {
		scaled := float64(absolute) / 1000
		formatted = trimDecimal(strconv.FormatFloat(scaled, 'f', 1, 64))
	}
	if negative {
		formatted = "-" + formatted
	}
	if absolute >= 1000 {
		return "~" + formatted + "k"
	}
	return "~" + formatted
}

// FormatTokenEstimate is an alias for FormatTokens.
func FormatTokenEstimate(value int64) string {
	return FormatTokens(value)
}

func trimDecimal(value string) string {
	value = strings.TrimRight(value, "0")
	value = strings.TrimRight(value, ".")
	if value == "" {
		return "0"
	}
	return value
}

func magnitude(value int64) uint64 {
	if value >= 0 {
		return uint64(value)
	}
	return uint64(-(value + 1)) + 1
}

// FormatDuration formats a duration using hours, minutes, and seconds. It
// intentionally does not render days so long durations remain comparable at a
// glance (39h 15m rather than 1d 15h 15m). Only the two most useful units are
// kept, and sub-second noise is discarded.
func FormatDuration(value time.Duration) string {
	negative := value < 0
	absolute := magnitude(int64(value))

	hours := absolute / uint64(time.Hour)
	absolute %= uint64(time.Hour)
	minutes := absolute / uint64(time.Minute)
	absolute %= uint64(time.Minute)
	seconds := absolute / uint64(time.Second)

	parts := make([]string, 0, 3)
	if hours > 0 {
		parts = append(parts, strconv.FormatUint(hours, 10)+"h")
		if minutes > 0 {
			parts = append(parts, strconv.FormatUint(minutes, 10)+"m")
		}
	} else if minutes > 0 {
		parts = append(parts, strconv.FormatUint(minutes, 10)+"m")
	} else {
		parts = append(parts, strconv.FormatUint(seconds, 10)+"s")
	}
	result := strings.Join(parts, " ")
	if negative {
		return "-" + result
	}
	return result
}

// ConciseDuration is an alias for FormatDuration.
func ConciseDuration(value time.Duration) string {
	return FormatDuration(value)
}

// FormatRelativeTime renders recent timestamps as concise ages and older
// timestamps as an unambiguous UTC date.  A future timestamp is kept explicit
// rather than being mislabeled as past activity.
func FormatRelativeTime(value, now time.Time) string {
	delta := now.Sub(value)
	if delta < 0 {
		return "in " + relativeUnit(-delta)
	}
	if delta < time.Minute {
		return "just now"
	}
	if delta < 7*24*time.Hour {
		return relativeUnit(delta) + " ago"
	}
	value = value.UTC()
	if value.Year() != now.UTC().Year() {
		return value.Format("Jan 2, 2006")
	}
	return value.Format("Jan 2, 15:04")
}

func relativeUnit(value time.Duration) string {
	switch {
	case value < time.Minute:
		return "less than 1m"
	case value < time.Hour:
		return strconv.FormatInt(int64(value/time.Minute), 10) + "m"
	case value < 24*time.Hour:
		return strconv.FormatInt(int64(value/time.Hour), 10) + "h"
	default:
		return strconv.FormatInt(int64(value/(24*time.Hour)), 10) + "d"
	}
}

// RelativeTimestamp is an alias for FormatRelativeTime.
func RelativeTimestamp(value, now time.Time) string {
	return FormatRelativeTime(value, now)
}

// FormatTimestamp emits the exact UTC RFC3339Nano companion representation.
func FormatTimestamp(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

// ExactTimestamp is an alias for FormatTimestamp.
func ExactTimestamp(value time.Time) string {
	return FormatTimestamp(value)
}

// RFC3339NanoTimestamp is an explicit alias for FormatTimestamp.
func RFC3339NanoTimestamp(value time.Time) string {
	return FormatTimestamp(value)
}

// ContractHomePath contracts a path only when it is the home directory itself
// or a descendant.  Prefix lookalikes such as /home/alice-old are left alone.
// Relative paths and an empty home are never contracted.
func ContractHomePath(path, home string) string {
	if path == "" || home == "" || !filepath.IsAbs(path) || !filepath.IsAbs(home) {
		return path
	}

	cleanPath := filepath.Clean(path)
	cleanHome := filepath.Clean(home)
	relative, err := filepath.Rel(cleanHome, cleanPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return path
	}
	if relative == "." {
		return "~"
	}
	return filepath.Join("~", relative)
}

// ShortPath is the process-home convenience form of ContractHomePath.
func ShortPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return ContractHomePath(path, home)
}
