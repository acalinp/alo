package alo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

type CommandResult struct {
	ExitCode int
	TimedOut bool
}

func runProcess(
	ctx context.Context,
	directory string,
	command []string,
	environment []string,
	stdout, stderr io.Writer,
) (CommandResult, error) {
	if len(command) == 0 {
		return CommandResult{}, errors.New("command is empty")
	}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = directory
	cmd.Env = environment
	cmd.Stdin = nil
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return CommandResult{}, fmt.Errorf("start %q: %w", command[0], err)
	}

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case err := <-waited:
		return commandResult(cmd.ProcessState, false, err)
	case <-ctx.Done():
	}

	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case err := <-waited:
		result, waitErr := commandResult(cmd.ProcessState, true, err)
		if waitErr != nil {
			return result, errors.Join(ctx.Err(), waitErr)
		}
		return result, ctx.Err()
	case <-timer.C:
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-waited
		return CommandResult{ExitCode: 124, TimedOut: true}, ctx.Err()
	}
}

func commandResult(state *os.ProcessState, timedOut bool, waitErr error) (CommandResult, error) {
	result := CommandResult{ExitCode: -1, TimedOut: timedOut}
	if state != nil {
		result.ExitCode = state.ExitCode()
	}
	if waitErr == nil {
		return result, nil
	}
	var exitError *exec.ExitError
	if errors.As(waitErr, &exitError) {
		return result, nil
	}
	return result, waitErr
}
