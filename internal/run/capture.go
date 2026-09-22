package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"alo/internal/config"
)

var captureReadyTimeout = 5 * time.Second

type Capture struct {
	LogPath string

	command *exec.Cmd
	log     *os.File
	waited  chan error
	failed  chan error

	mu       sync.Mutex
	finished bool
	stopping bool
	waitErr  error
}

func StartCapture(ctx context.Context, configuration *config.Config, store *RunStore, attempt int) (*Capture, error) {
	if configuration.Capture == nil {
		return nil, nil
	}
	attemptDir, err := store.EnsureAttempt(attempt)
	if err != nil {
		return nil, err
	}
	logPath, err := nextAvailablePath(attemptDir, "capture.log")
	if err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create capture log: %w", err)
	}
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("create capture readiness pipe: %w", err)
	}
	command := exec.Command(configuration.Capture.Command[0], configuration.Capture.Command[1:]...)
	command.Dir = configuration.BaseDir
	command.Env = append(captureEnvironment(configuration, attemptDir), "ALO_CAPTURE_READY_FD=3")
	command.ExtraFiles = []*os.File{readyWriter}
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if err := command.Start(); err != nil {
		_ = readyReader.Close()
		_ = readyWriter.Close()
		_ = logFile.Close()
		return nil, fmt.Errorf("start capture command: %w", err)
	}
	_ = readyWriter.Close()
	capture := &Capture{
		LogPath: logPath,
		command: command,
		log:     logFile,
		waited:  make(chan error, 1),
		failed:  make(chan error, 1),
	}
	go capture.wait()
	ready := make(chan error, 1)
	go func() {
		buffer := []byte{0}
		count, err := readyReader.Read(buffer)
		_ = readyReader.Close()
		if err == nil && count == 1 {
			ready <- nil
			return
		}
		ready <- errors.New("capture command closed readiness pipe without signaling")
	}()
	timer := time.NewTimer(captureReadyTimeout)
	defer timer.Stop()
	select {
	case err := <-ready:
		if err == nil {
			return capture, nil
		}
		return nil, errors.Join(err, capture.Stop())
	case err := <-capture.failed:
		return nil, errors.Join(err, capture.Stop())
	case <-timer.C:
		return nil, errors.Join(fmt.Errorf("capture command did not become ready within %s", captureReadyTimeout), capture.Stop())
	case <-ctx.Done():
		return nil, errors.Join(ctx.Err(), capture.Stop())
	}
}

func (c *Capture) wait() {
	err := c.command.Wait()
	result := captureExitError(c.command.ProcessState, err)
	c.mu.Lock()
	c.finished = true
	c.waitErr = result
	c.mu.Unlock()
	c.waited <- result
	c.mu.Lock()
	stopping := c.stopping
	c.mu.Unlock()
	if !stopping {
		c.failed <- result
	}
}

func (c *Capture) Failure() <-chan error {
	if c == nil {
		return nil
	}
	return c.failed
}

func (c *Capture) Stop() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	finished := c.finished
	if !finished {
		c.stopping = true
	}
	waitErr := c.waitErr
	c.mu.Unlock()
	if finished {
		return errors.Join(waitErr, c.log.Close())
	}
	var killErr error
	killErr = syscall.Kill(-c.command.Process.Pid, syscall.SIGTERM)
	if errors.Is(killErr, syscall.ESRCH) {
		killErr = nil
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-c.waited:
	case <-timer.C:
		if err := syscall.Kill(-c.command.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			killErr = errors.Join(killErr, err)
		}
		<-c.waited
	}
	return errors.Join(killErr, c.log.Close())
}

func MonitorCapture(ctx context.Context, capture *Capture, action func(context.Context) error) error {
	if capture == nil {
		return action(ctx)
	}
	phaseContext, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- action(phaseContext) }()
	select {
	case err := <-done:
		cancel()
		select {
		case captureErr := <-capture.Failure():
			return errors.Join(err, captureErr)
		default:
			return err
		}
	case captureErr := <-capture.Failure():
		cancel()
		return errors.Join(captureErr, <-done)
	case <-ctx.Done():
		cancel()
		return errors.Join(ctx.Err(), <-done)
	}
}

func captureExitError(state *os.ProcessState, waitErr error) error {
	if state != nil {
		if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return fmt.Errorf("capture command stopped by %s", status.Signal())
		}
		return fmt.Errorf("capture command exited before replay completed with status %d", state.ExitCode())
	}
	if waitErr != nil {
		return fmt.Errorf("capture command stopped before replay completed: %w", waitErr)
	}
	return errors.New("capture command stopped before replay completed")
}

func stopCapture(capture **Capture) error {
	if *capture == nil {
		return nil
	}
	err := (*capture).Stop()
	*capture = nil
	return err
}

func captureLogPath(capture *Capture) string {
	if capture == nil {
		return ""
	}
	return capture.LogPath
}
