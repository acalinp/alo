package podman

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

func TestAgentStreamRoutesProtocolAndPlainText(t *testing.T) {
	var retained bytes.Buffer
	progress := &terminalProgress{
		output:     io.Discard,
		active:     true,
		state:      "working",
		stateSince: time.Now(),
	}
	redacted := newSecretRedactor(&retained, []string{"secret-value"})
	stream := newAgentStreamWriter(redacted, progress)
	input := strings.Join([]string{
		`{"v":1,"type":"status","state":"thinking"}`,
		`{"v":1,"type":"output","text":"built secret-value\n"}`,
		`{"v":1,"type":"status","state":"running","tool":"bash -c private"}`,
		"legacy warning",
		"{bad json}",
		`{"v":1,"type":"diagnostic","text":"pi warning\n"}`,
	}, "\n") + "\npartial"
	for len(input) > 0 {
		size := min(7, len(input))
		if _, err := io.WriteString(stream, input[:size]); err != nil {
			t.Fatal(err)
		}
		input = input[size:]
	}
	if err := flushAgentOutput(redacted, stream); err != nil {
		t.Fatal(err)
	}

	if progress.state != "running" || progress.tool != "tool" {
		t.Fatalf("progress state = %q, tool = %q", progress.state, progress.tool)
	}
	want := "built [REDACTED]\nlegacy warning\n{bad json}\npi warning\npartial"
	if retained.String() != want {
		t.Fatalf("retained output = %q, want %q", retained.String(), want)
	}
	if strings.Contains(retained.String(), `"type":"status"`) {
		t.Fatalf("status leaked into retained output: %q", retained.String())
	}
}

func TestAgentStreamBoundsRecords(t *testing.T) {
	progress := newTerminalProgress(io.Discard, "run", time.Time{})
	stream := newAgentStreamWriter(io.Discard, progress)
	if _, err := stream.Write(bytes.Repeat([]byte{'x'}, maximumAgentRecord+1)); err == nil {
		t.Fatal("oversized agent record was accepted")
	}
}
