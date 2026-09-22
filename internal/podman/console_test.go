package podman

import (
	"bytes"
	"io"
	"testing"
)

func TestLinePrefixWriterLabelsChunkedLines(t *testing.T) {
	var output bytes.Buffer
	var retained bytes.Buffer
	writer := newLinePrefixWriter(&output, "[replay] ")
	target := io.MultiWriter(&retained, writer)
	for _, text := range []string{"first", " line\nsecond\n", "third", " line"} {
		if _, err := io.WriteString(target, text); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.EndLine(); err != nil {
		t.Fatal(err)
	}
	want := "[replay] first line\n[replay] second\n[replay] third line\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
	if retained.String() != "first line\nsecond\nthird line" {
		t.Fatalf("retained output = %q", retained.String())
	}
}
