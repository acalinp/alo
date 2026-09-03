package podman

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
		runID:      "run",
		active:     true,
		startedAt:  now.Add(-5 * time.Second),
		deadline:   now.Add(55 * time.Second),
		lastOutput: now.Add(-2 * time.Second),
		state:      "working",
		stateSince: now.Add(-2 * time.Second),
		lineStart:  true,
	}
	stream := io.MultiWriter(&retained, progress)

	progress.render(now)
	if !strings.Contains(console.String(), "[run] agent working") {
		t.Fatalf("progress output = %q", console.String())
	}
	if !strings.Contains(console.String(), "5s elapsed · 55s remaining") {
		t.Fatalf("progress timing = %q", console.String())
	}
	progress.SetState("running", "bash")
	stateSince := progress.stateSince
	progress.SetState("running", "bash")
	if !progress.stateSince.Equal(stateSince) {
		t.Fatal("repeated status reset its elapsed time")
	}
	progress.lastOutput = now.Add(-2 * time.Second)
	progress.render(now.Add(2 * time.Second))
	if !strings.Contains(console.String(), "[run] agent running bash") {
		t.Fatalf("stateful progress output = %q", console.String())
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

func TestTerminalProgressClampsExpiredDeadline(t *testing.T) {
	now := time.Now()
	progress := &terminalProgress{
		startedAt: now.Add(-time.Minute),
		deadline:  now.Add(-time.Second),
	}
	if got := progress.timing(now); got != "1m0s elapsed · 0s remaining" {
		t.Fatalf("timing = %q", got)
	}
}
