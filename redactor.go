package alo

import (
	"bytes"
	"io"
	"sync"
)

type secretRedactor struct {
	mu      sync.Mutex
	target  io.Writer
	secrets [][]byte
	pending []byte
}

func newSecretRedactor(target io.Writer, values []string) *secretRedactor {
	redactor := &secretRedactor{target: target}
	seen := make(map[string]bool)
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		secret := []byte(value)
		redactor.secrets = append(redactor.secrets, secret)
	}
	return redactor
}

func (w *secretRedactor) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = append(w.pending, data...)
	if err := w.writeReady(false); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (w *secretRedactor) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writeReady(true)
}

func (w *secretRedactor) writeReady(final bool) error {
	if len(w.pending) == 0 {
		return nil
	}
	hold := 0
	if !final {
		for _, secret := range w.secrets {
			maximum := min(len(w.pending), len(secret)-1)
			for length := maximum; length > hold; length-- {
				if bytes.Equal(w.pending[len(w.pending)-length:], secret[:length]) {
					hold = length
					break
				}
			}
		}
	}
	ready := append([]byte(nil), w.pending[:len(w.pending)-hold]...)
	for _, secret := range w.secrets {
		ready = bytes.ReplaceAll(ready, secret, []byte("[REDACTED]"))
	}
	if len(ready) > 0 {
		if _, err := w.target.Write(ready); err != nil {
			return err
		}
	}
	w.pending = append(w.pending[:0], w.pending[len(w.pending)-hold:]...)
	return nil
}

type tailBuffer struct {
	maximum int
	data    []byte
}

func (w *tailBuffer) Write(data []byte) (int, error) {
	w.data = append(w.data, data...)
	if len(w.data) > w.maximum {
		w.data = append(w.data[:0], w.data[len(w.data)-w.maximum:]...)
	}
	return len(data), nil
}

func (w *tailBuffer) String() string { return string(w.data) }
