package podman

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sync"
)

const maximumAgentRecord = 16 << 20

var agentToolPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)

var agentStates = map[string]bool{
	"starting":   true,
	"thinking":   true,
	"responding": true,
	"preparing":  true,
	"running":    true,
	"compacting": true,
	"retrying":   true,
	"finishing":  true,
}

type agentStreamRecord struct {
	Version int    `json:"v"`
	Type    string `json:"type"`
	State   string `json:"state"`
	Tool    string `json:"tool"`
	Text    string `json:"text"`
}

// agentStreamWriter consumes the private, line-delimited protocol emitted by
// Alo's managed runner. Unrecognized input is passed through so existing
// workshop images that print ordinary text remain usable.
type agentStreamWriter struct {
	mu       sync.Mutex
	output   io.Writer
	progress *terminalProgress
	pending  []byte
	failed   error
}

func newAgentStreamWriter(output io.Writer, progress *terminalProgress) *agentStreamWriter {
	return &agentStreamWriter{output: output, progress: progress}
}

func (w *agentStreamWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failed != nil {
		return 0, w.failed
	}
	w.pending = append(w.pending, data...)
	for {
		newline := bytes.IndexByte(w.pending, '\n')
		if newline < 0 {
			if len(w.pending) > maximumAgentRecord {
				w.failed = fmt.Errorf("agent stream record exceeds %d bytes", maximumAgentRecord)
				return len(data), w.failed
			}
			return len(data), nil
		}
		if newline > maximumAgentRecord {
			w.failed = fmt.Errorf("agent stream record exceeds %d bytes", maximumAgentRecord)
			return len(data), w.failed
		}
		line := append([]byte(nil), w.pending[:newline]...)
		raw := append(line, '\n')
		w.pending = w.pending[newline+1:]
		if err := w.route(line, raw); err != nil {
			w.failed = err
			return len(data), err
		}
	}
}

func (w *agentStreamWriter) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failed != nil {
		return w.failed
	}
	if len(w.pending) == 0 {
		return nil
	}
	if len(w.pending) > maximumAgentRecord {
		return fmt.Errorf("agent stream record exceeds %d bytes", maximumAgentRecord)
	}
	pending := append([]byte(nil), w.pending...)
	w.pending = nil
	_, err := w.output.Write(pending)
	return err
}

func (w *agentStreamWriter) route(line, raw []byte) error {
	line = bytes.TrimSuffix(line, []byte{'\r'})
	var record agentStreamRecord
	if json.Unmarshal(line, &record) != nil || record.Version != 1 {
		_, err := w.output.Write(raw)
		return err
	}
	switch record.Type {
	case "status":
		if !agentStates[record.State] {
			_, err := w.output.Write(raw)
			return err
		}
		tool := record.Tool
		if record.State != "preparing" && record.State != "running" {
			tool = ""
		} else if tool != "" && !agentToolPattern.MatchString(tool) {
			tool = "tool"
		}
		w.progress.SetState(record.State, tool)
		return nil
	case "output", "diagnostic":
		_, err := io.WriteString(w.output, record.Text)
		return err
	default:
		_, err := w.output.Write(raw)
		return err
	}
}

func flushAgentOutput(redacted *secretRedactor, streams ...*agentStreamWriter) error {
	var flushErr error
	for _, stream := range streams {
		flushErr = errors.Join(flushErr, stream.Flush())
	}
	return errors.Join(flushErr, redacted.Flush())
}
