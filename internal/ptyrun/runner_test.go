package ptyrun

import (
	"context"
	"strings"
	"testing"
)

// TestStreamStdinIsNotATTY guards against TTY-gated CLIs (e.g. Homebrew
// Cask) prompting interactively instead of failing non-interactively.
func TestStreamStdinIsNotATTY(t *testing.T) {
	argv := []string{"sh", "-c", "if [ -t 0 ]; then echo TTY; else echo NOTTY; fi"}
	chunks, done := Stream(context.Background(), argv, Options{})

	var out strings.Builder
	for ch := range chunks {
		out.WriteString(ch.Data)
	}
	if err := <-done; err != nil {
		t.Fatalf("Stream: %v", err)
	}

	got := strings.TrimSpace(out.String())
	if got != "NOTTY" {
		t.Fatalf("expected child to see a non-TTY stdin, got %q", got)
	}
}
