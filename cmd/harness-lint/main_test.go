package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/kespineira/harness-lint/internal/presentation"
)

func TestWriteCLIErrorWrapsLongIdentityWithoutLosingIt(t *testing.T) {
	name := strings.Repeat("long-capability-", 12)
	message := "No capability named \"" + name + "\" was found.\n\nTry:\n  harness-lint report --all"
	var output bytes.Buffer
	writeCLIError(&output, errors.New(message))

	compact := strings.Join(strings.Fields(output.String()), "")
	if !strings.Contains(compact, strings.Join(strings.Fields(name), "")) {
		t.Fatalf("wrapped error lost capability identity: %q", output.String())
	}
	for _, line := range strings.Split(output.String(), "\n") {
		if presentation.VisibleWidth(line) > presentation.DefaultWidth {
			t.Fatalf("error line exceeds %d columns: %q", presentation.DefaultWidth, line)
		}
	}
}
