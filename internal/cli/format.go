package cli

import (
	"fmt"
	"strings"
	"unicode"
)

func formatAdvertisedSessionEvidence(invoked, advertised *int64) string {
	if invoked == nil || advertised == nil {
		return "unknown"
	}
	return fmt.Sprintf("invoked in %d / %d advertised sessions", *invoked, *advertised)
}

func cleanText(value string) string {
	var builder strings.Builder
	for _, character := range value {
		if unicode.IsControl(character) {
			builder.WriteByte(' ')
			continue
		}
		builder.WriteRune(character)
	}
	return strings.TrimSpace(builder.String())
}
