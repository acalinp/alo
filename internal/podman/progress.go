package podman

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
	runID  string
	active bool

	mu         sync.Mutex
	started    bool
	startedAt  time.Time
	deadline   time.Time
	lastOutput time.Time
	state      string
	tool       string
	stateSince time.Time
	lineStart  bool
	visible    bool
	frame      int
	stop       chan struct{}
	done       chan struct{}
}

func newTerminalProgress(output io.Writer, runID string, deadline time.Time) *terminalProgress {
	now := time.Now()
	return &terminalProgress{
		output:     output,
		runID:      runID,
		active:     isTerminal(output),
		startedAt:  now,
		deadline:   deadline,
		lastOutput: now,
		state:      "working",
		stateSince: now,
		lineStart:  true,
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
}

func (p *terminalProgress) SetState(state, tool string) {
	if !p.active {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state == state && p.tool == tool {
		return
	}
	p.state = state
	p.tool = tool
	p.stateSince = time.Now()
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
		p.output, "%s[%s] %s %s %s",
		clearLine,
		p.runID,
		p.description(),
		frame,
		p.timing(now),
	)
	p.visible = err == nil
}

func (p *terminalProgress) timing(now time.Time) string {
	timing := progressDuration(now.Sub(p.startedAt)) + " elapsed"
	if !p.deadline.IsZero() {
		timing += " · " + progressDuration(p.deadline.Sub(now)) + " remaining"
	}
	return timing
}

func (p *terminalProgress) description() string {
	switch p.state {
	case "starting":
		return "agent starting"
	case "thinking":
		return "agent thinking"
	case "responding":
		return "agent responding"
	case "preparing":
		return "agent preparing " + progressTool(p.tool)
	case "running":
		return "agent running " + progressTool(p.tool)
	case "compacting":
		return "agent compacting context"
	case "retrying":
		return "agent retrying"
	case "finishing":
		return "agent finishing"
	default:
		return "agent working"
	}
}

func progressTool(tool string) string {
	if tool == "" {
		return "tool"
	}
	return tool
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
