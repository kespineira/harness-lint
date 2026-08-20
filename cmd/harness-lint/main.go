package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kespineira/harness-lint/internal/cli"
	"github.com/kespineira/harness-lint/internal/presentation"
)

func main() {
	if err := cli.Execute(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		writeCLIError(os.Stderr, err)
		os.Exit(2)
	}
}

func writeCLIError(out io.Writer, err error) {
	const prefix = "harness-lint: "
	paragraphs := strings.Split(strings.TrimSpace(err.Error()), "\n")
	if len(paragraphs) == 0 {
		fmt.Fprintln(out, strings.TrimSpace(prefix))
		return
	}
	firstLines := presentation.WrapText(paragraphs[0], presentation.DefaultWidth-len(prefix))
	fmt.Fprintln(out, prefix+firstLines[0])
	continuation := strings.Repeat(" ", len(prefix))
	for _, line := range firstLines[1:] {
		fmt.Fprintln(out, continuation+line)
	}
	for _, paragraph := range paragraphs[1:] {
		for _, line := range presentation.WrapText(paragraph, presentation.DefaultWidth) {
			fmt.Fprintln(out, line)
		}
	}
}
