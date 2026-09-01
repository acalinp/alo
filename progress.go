package alo

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/term"
)

const (
	progressInterval = 250 * time.Millisecond
	progressQuiet    = time.Second
	clearLine        = "\r\x1b[2K"
)

var progressFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// terminalProgress passes agent output through unchanged while owning one
// ephemeral terminal line during quiet periods. Animation is never sent to the
// retained log because this writer wraps only the console branch.
type terminalProgress struct {
	output io.Writer
	label  string
	active bool

	mu         sync.Mutex
	started    bool
	startedAt  time.Time
	lastOutput time.Time
	lineStart  bool
	visible    bool
	frame      int
	stop       chan struct{}
	done       chan struct{}
}

func newTerminalProgress(output io.Writer, label string) *terminalProgress {
	now := time.Now()
	return &terminalProgress{
		output:     output,
		label:      label,
		active:     isTerminal(output),
		startedAt:  now,
		lastOutput: now,
		lineStart:  true,
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
}

func isTerminal(output io.Writer) bool {
	file, ok := output.(interface{ Fd() uintptr })
	return ok && os.Getenv("TERM") != "dumb" && term.IsTerminal(int(file.Fd()))
}

func (p *terminalProgress) Start() {
	if !p.active || p.started {
		return
	}
	p.started = true
	go p.animate()
}

func (p *terminalProgress) Stop() {
	if !p.active || !p.started {
		return
	}
	close(p.stop)
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = p.clear()
}

func (p *terminalProgress) Write(data []byte) (int, error) {
	if !p.active {
		return p.output.Write(data)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.clear(); err != nil {
		return 0, err
	}
	written, err := p.output.Write(data)
	if written > 0 {
		p.lastOutput = time.Now()
		last := data[written-1]
		p.lineStart = last == '\n' || last == '\r'
	}
	return written, err
}

func (p *terminalProgress) animate() {
	ticker := time.NewTicker(progressInterval)
	defer func() {
		ticker.Stop()
		close(p.done)
	}()
	for {
		select {
		case now := <-ticker.C:
			p.render(now)
		case <-p.stop:
			return
		}
	}
}

func (p *terminalProgress) render(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.lineStart || now.Sub(p.lastOutput) < progressQuiet {
		return
	}
	frame := progressFrames[p.frame%len(progressFrames)]
	p.frame++
	_, err := fmt.Fprintf(
		p.output, "%s%s %s %s · silent %s",
		clearLine,
		p.label,
		frame,
		progressDuration(now.Sub(p.startedAt)),
		progressDuration(now.Sub(p.lastOutput)),
	)
	p.visible = err == nil
}

func (p *terminalProgress) clear() error {
	if !p.visible {
		return nil
	}
	p.visible = false
	_, err := io.WriteString(p.output, clearLine)
	return err
}

func progressDuration(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	return duration.Round(time.Second).String()
}
