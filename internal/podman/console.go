package podman

import (
	"io"
	"sync"
)

type linePrefixWriter struct {
	mu        sync.Mutex
	output    io.Writer
	prefix    []byte
	lineStart bool
}

func newLinePrefixWriter(output io.Writer, prefix string) *linePrefixWriter {
	return &linePrefixWriter{output: output, prefix: []byte(prefix), lineStart: true}
}

func (w *linePrefixWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	written := 0
	for len(data) > 0 {
		if w.lineStart {
			if _, err := w.output.Write(w.prefix); err != nil {
				return written, err
			}
			w.lineStart = false
		}
		end := len(data)
		for index, value := range data {
			if value == '\n' {
				end = index + 1
				break
			}
		}
		count, err := w.output.Write(data[:end])
		written += count
		if count > 0 && data[count-1] == '\n' {
			w.lineStart = true
		}
		if err != nil {
			return written, err
		}
		if count != end {
			return written, io.ErrShortWrite
		}
		data = data[end:]
	}
	return written, nil
}

func (w *linePrefixWriter) EndLine() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.lineStart {
		return nil
	}
	_, err := io.WriteString(w.output, "\n")
	w.lineStart = true
	return err
}
