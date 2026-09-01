package alo

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

func TestTerminalProgressKeepsAnimationOutOfAgentStream(t *testing.T) {
	var console bytes.Buffer
	var retained bytes.Buffer
	now := time.Now()
	progress := &terminalProgress{
		output:     &console,
		label:      "[run] agent working",
		active:     true,
		startedAt:  now.Add(-2 * time.Second),
		lastOutput: now.Add(-2 * time.Second),
		lineStart:  true,
	}
	stream := io.MultiWriter(&retained, progress)

	progress.render(now)
	if !strings.Contains(console.String(), "[run] agent working") {
		t.Fatalf("progress output = %q", console.String())
	}
	if _, err := io.WriteString(stream, "agent output"); err != nil {
		t.Fatal(err)
	}
	progress.lastOutput = now.Add(-2 * time.Second)
	before := console.Len()
	progress.render(now)
	if console.Len() != before {
		t.Fatal("progress overwrote partial agent output")
	}
	if _, err := io.WriteString(stream, "\n"); err != nil {
		t.Fatal(err)
	}

	if got := retained.String(); got != "agent output\n" {
		t.Fatalf("retained output = %q", got)
	}
	if strings.Contains(retained.String(), "agent working") || strings.Contains(retained.String(), "\x1b") {
		t.Fatalf("animation leaked into retained output: %q", retained.String())
	}
}
