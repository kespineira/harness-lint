package presentation

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// KeyValue is one deterministic human-facing key/value line.
type KeyValue struct {
	Key   string
	Value string
}

// Row is an alias so callers can use either []string rows or named rows
// without conversion.
type Row = []string

// WrapText returns lines no wider than width when possible.  Words are kept
// intact where they fit; an overlong token is split across lines, preserving
// every rune rather than silently truncating an identity.  ANSI CSI sequences
// consume no columns and are carried through unchanged.
func WrapText(value string, width int) []string {
	if width <= 0 {
		width = DefaultWidth
	}
	paragraphs := strings.Split(value, "\n")
	lines := make([]string, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		lines = append(lines, wrapParagraph(paragraph, width)...)
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

// Wrap returns wrapped text joined with newlines.
func Wrap(value string, width int) string {
	return strings.Join(WrapText(value, width), "\n")
}

// VisibleWidth reports the simple display width used by this package.  It
// deliberately counts runes (rather than attempting full terminal grapheme or
// East Asian width handling) and ignores ANSI CSI sequences.
func VisibleWidth(value string) int {
	width := 0
	for _, token := range tokenize(value) {
		width += token.width
	}
	return width
}

// RenderKeyValues produces aligned `key: value` lines and wraps values with
// continuation indentation.  The optional width argument defaults to 80 and
// exists to make narrow-view tests deterministic.
func RenderKeyValues(values []KeyValue, widths ...int) string {
	width := layoutWidth(widths)
	if len(values) == 0 {
		return ""
	}

	maxKeyWidth := 0
	for _, value := range values {
		if current := VisibleWidth(value.Key); current > maxKeyWidth {
			maxKeyWidth = current
		}
	}

	lines := make([]string, 0, len(values))
	for _, value := range values {
		prefixWidth := maxKeyWidth + 2 // key + ": "
		if prefixWidth >= width {
			// A pathological key should still be preserved and wrapped rather
			// than forcing an impossible alignment or truncating its identity.
			lines = append(lines, WrapText(value.Key+": "+value.Value, width)...)
			continue
		}

		keyPadding := strings.Repeat(" ", maxKeyWidth-VisibleWidth(value.Key)+1)
		wrapped := WrapText(value.Value, width-prefixWidth)
		if len(wrapped) == 0 {
			wrapped = []string{""}
		}
		lines = append(lines, value.Key+":"+keyPadding+wrapped[0])
		continuation := strings.Repeat(" ", prefixWidth)
		for _, line := range wrapped[1:] {
			lines = append(lines, continuation+line)
		}
	}
	return strings.Join(lines, "\n")
}

// KeyValues is an alias for RenderKeyValues.
func KeyValues(values []KeyValue, widths ...int) string {
	return RenderKeyValues(values, widths...)
}

// RenderRows aligns columns with two spaces and wraps cell contents.  Long
// identities are split rather than clipped.  If a single identity is wider
// than the requested table width, the split lines still remain within it.
func RenderRows(rows [][]string, widths ...int) string {
	return renderRows(rows, layoutWidth(widths))
}

// RenderTable renders headers followed by rows.  It intentionally avoids
// borders and separator glyphs so output stays compact and easy to pipe.
func RenderTable(headers []string, rows [][]string, widths ...int) string {
	allRows := make([][]string, 0, len(rows)+1)
	if len(headers) > 0 {
		allRows = append(allRows, headers)
	}
	allRows = append(allRows, rows...)
	return renderRows(allRows, layoutWidth(widths))
}

// Rows is an alias for RenderRows.
func Rows(rows [][]string, widths ...int) string {
	return RenderRows(rows, widths...)
}

// Table is an alias for RenderTable.
func Table(headers []string, rows [][]string, widths ...int) string {
	return RenderTable(headers, rows, widths...)
}

func layoutWidth(widths []int) int {
	if len(widths) > 0 && widths[0] > 0 {
		return widths[0]
	}
	return DefaultWidth
}

func renderRows(rows [][]string, width int) string {
	if len(rows) == 0 {
		return ""
	}
	columnCount := 0
	for _, row := range rows {
		if len(row) > columnCount {
			columnCount = len(row)
		}
	}
	if columnCount == 0 {
		return ""
	}

	columnWidths := chooseColumnWidths(rows, columnCount, width)
	output := make([]string, 0, len(rows))
	for _, row := range rows {
		cells := make([][]string, columnCount)
		rowHeight := 1
		for column := 0; column < columnCount; column++ {
			value := ""
			if column < len(row) {
				value = row[column]
			}
			cells[column] = WrapText(value, columnWidths[column])
			if len(cells[column]) > rowHeight {
				rowHeight = len(cells[column])
			}
		}

		for line := 0; line < rowHeight; line++ {
			parts := make([]string, 0, columnCount)
			for column := 0; column < columnCount; column++ {
				cell := ""
				if line < len(cells[column]) {
					cell = cells[column][line]
				}
				if column < columnCount-1 {
					cell += strings.Repeat(" ", columnWidths[column]-VisibleWidth(cell))
				}
				parts = append(parts, cell)
			}
			output = append(output, strings.TrimRight(strings.Join(parts, "  "), " "))
		}
	}
	return strings.Join(output, "\n")
}

func chooseColumnWidths(rows [][]string, columnCount, width int) []int {
	natural := make([]int, columnCount)
	for _, row := range rows {
		for column, value := range row {
			if column >= columnCount {
				break
			}
			if current := maxLineWidth(value); current > natural[column] {
				natural[column] = current
			}
		}
	}
	for column := range natural {
		if natural[column] == 0 {
			natural[column] = 1
		}
	}

	separatorWidth := 2 * (columnCount - 1)
	available := width - separatorWidth
	minimum := columnCount
	if available < minimum {
		// There is no possible layout narrower than one rune per column plus
		// separators.  Keep identities intact rather than dropping columns.
		available = minimum
	}
	widths := append([]int(nil), natural...)
	if sum(widths) <= available {
		return widths
	}

	// Start with a fair share for every column so a short status/type column
	// does not collapse to one rune merely because an identity is long.  The
	// remaining budget then follows the columns with the most useful content.
	share := available / columnCount
	if share < 1 {
		share = 1
	}
	for column := range widths {
		if widths[column] > share {
			widths[column] = share
		}
	}
	remaining := available - sum(widths)
	for remaining > 0 {
		chosen := -1
		largestNeed := 0
		for column := range widths {
			need := natural[column] - widths[column]
			if need > largestNeed {
				largestNeed = need
				chosen = column
			}
		}
		if chosen < 0 {
			break
		}
		widths[chosen]++
		remaining--
	}
	return widths
}

func maxLineWidth(value string) int {
	maximum := 0
	for _, line := range strings.Split(value, "\n") {
		if current := VisibleWidth(line); current > maximum {
			maximum = current
		}
	}
	return maximum
}

func sum(values []int) int {
	total := 0
	for _, value := range values {
		total += value
	}
	return total
}

type textToken struct {
	raw   string
	width int
	space bool
}

func tokenize(value string) []textToken {
	tokens := make([]textToken, 0, utf8.RuneCountInString(value))
	for position := 0; position < len(value); {
		if value[position] == 0x1b && position+1 < len(value) && value[position+1] == '[' {
			end := ansiEnd(value, position)
			tokens = append(tokens, textToken{raw: value[position:end]})
			position = end
			continue
		}
		runeValue, size := utf8.DecodeRuneInString(value[position:])
		if runeValue == utf8.RuneError && size == 0 {
			size = 1
		}
		tokens = append(tokens, textToken{
			raw:   value[position : position+size],
			width: 1,
			space: unicode.IsSpace(runeValue),
		})
		position += size
	}
	return tokens
}

func ansiEnd(value string, start int) int {
	position := start + 2
	for position < len(value) {
		byteValue := value[position]
		position++
		if byteValue >= 0x40 && byteValue <= 0x7e {
			return position
		}
	}
	return len(value)
}

func wrapParagraph(value string, width int) []string {
	tokens := tokenize(value)
	if visibleTokenWidth(tokens) <= width {
		return []string{value}
	}

	lines := make([]string, 0, visibleTokenWidth(tokens)/width+1)
	start := 0
	for start < len(tokens) {
		lineWidth := 0
		end := start
		for end < len(tokens) && lineWidth+tokens[end].width <= width {
			lineWidth += tokens[end].width
			end++
		}
		if end == start {
			// This only occurs for a defensive zero/negative width; normal
			// callers always have width >= 1.
			end++
		}

		cut := end
		for candidate := end - 1; candidate > start; candidate-- {
			if tokens[candidate].space {
				cut = candidate
				break
			}
		}
		if cut == start {
			cut = end
		}

		line := rawTokens(tokens[start:cut])
		line = strings.TrimRightFunc(line, unicode.IsSpace)
		if line == "" && cut < end {
			// Avoid emitting an empty line when the boundary starts with
			// whitespace; advance through it below.
			cut = end
			line = rawTokens(tokens[start:cut])
			line = strings.TrimRightFunc(line, unicode.IsSpace)
		}
		lines = append(lines, line)
		start = cut
		for start < len(tokens) && tokens[start].space {
			start++
		}
	}
	return lines
}

func visibleTokenWidth(tokens []textToken) int {
	width := 0
	for _, token := range tokens {
		width += token.width
	}
	return width
}

func rawTokens(tokens []textToken) string {
	var builder strings.Builder
	for _, token := range tokens {
		builder.WriteString(token.raw)
	}
	return builder.String()
}
