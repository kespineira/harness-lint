package cli

import "strings"

func indentHumanBlock(value string, spaces int) string {
	if value == "" || spaces <= 0 {
		return value
	}
	prefix := strings.Repeat(" ", spaces)
	return prefix + strings.ReplaceAll(value, "\n", "\n"+prefix)
}
