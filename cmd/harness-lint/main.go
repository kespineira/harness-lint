package main

import (
	"fmt"
	"os"

	"github.com/kespineira/harness-lint/internal/cli"
)

func main() {
	if err := cli.Execute(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "harness-lint:", err)
		os.Exit(2)
	}
}
