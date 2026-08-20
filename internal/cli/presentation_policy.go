package cli

import (
	"io"
	"os"
	"strings"
	"time"

	"github.com/kespineira/harness-lint/internal/presentation"
	"github.com/mattn/go-isatty"
)

// resolveHumanRenderer centralizes terminal and NO_COLOR handling. JSON
// callers still receive a renderer for structural convenience, but all JSON
// paths bypass it before writing bytes.
func resolveHumanRenderer(options Options, flags parsedFlags, out io.Writer) (presentation.HumanRenderer, error) {
	mode, err := normalizeColorMode(flags.color)
	if err != nil {
		return presentation.HumanRenderer{}, err
	}
	color := false
	if !flags.json {
		switch mode {
		case "always":
			color = true
		case "never":
			color = false
		default:
			lookup := options.LookupEnv
			if lookup == nil {
				lookup = os.LookupEnv
			}
			_, noColor := lookup("NO_COLOR")
			if !noColor {
				terminal := options.IsTerminal
				if terminal == nil {
					terminal = defaultIsTerminal
				}
				color = terminal(out)
			}
		}
	}
	home := options.Home
	return presentation.NewHumanRenderer(presentation.Options{
		Color:   color,
		Width:   presentation.DefaultWidth,
		HomeDir: home,
		Now:     rendererNow(options),
	}), nil
}

func normalizeColorMode(value string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(value))
	if mode == "" {
		mode = "auto"
	}
	switch mode {
	case "auto", "always", "never":
		return mode, nil
	default:
		return "", &invalidColorModeError{value: mode}
	}
}

type invalidColorModeError struct{ value string }

func (e *invalidColorModeError) Error() string {
	return "invalid --color " + e.value + " (want auto, always, or never)"
}

func rendererNow(options Options) time.Time {
	if options.Now == nil {
		return time.Time{}
	}
	return options.Now().UTC()
}

func defaultIsTerminal(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(file.Fd()) || isatty.IsCygwinTerminal(file.Fd())
}
