package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/kespineira/harness-lint/internal/presentation"
)

func indentHumanBlock(value string, spaces int) string {
	if value == "" || spaces <= 0 {
		return value
	}
	prefix := strings.Repeat(" ", spaces)
	return prefix + strings.ReplaceAll(value, "\n", "\n"+prefix)
}

func humanRendererForIndent(renderer presentation.HumanRenderer, spaces int) presentation.HumanRenderer {
	options := renderer.Options()
	width := options.Width
	if width <= 0 {
		width = presentation.DefaultWidth
	}
	if spaces > 0 && width > spaces {
		width -= spaces
	}
	options.Width = width
	return presentation.NewHumanRenderer(options)
}

func humanRows(renderer presentation.HumanRenderer, rows [][]string, indent int) string {
	return humanRendererForIndent(renderer, indent).Rows(rows)
}

func humanTable(renderer presentation.HumanRenderer, headers []string, rows [][]string, indent int) string {
	return humanRendererForIndent(renderer, indent).Table(headers, rows)
}

func humanKeyValues(renderer presentation.HumanRenderer, values []presentation.KeyValue, indent int) string {
	return humanRendererForIndent(renderer, indent).KeyValues(values)
}

func humanWrap(renderer presentation.HumanRenderer, value string, indent int) string {
	return humanRendererForIndent(renderer, indent).Wrap(value)
}

func writeHumanText(out io.Writer, renderer presentation.HumanRenderer, value string, indent int) {
	fmt.Fprintln(out, indentHumanBlock(humanWrap(renderer, value, indent), indent))
}

func humanDayCount(renderer presentation.HumanRenderer, days int) string {
	unit := "days"
	if days == 1 {
		unit = "day"
	}
	return renderer.Integer(int64(days)) + " " + unit
}
