package alo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type RunOptions struct {
	StateDir string
	Stdout   io.Writer
	Stderr   io.Writer
	Agent    AgentRunner
}

type RunResult struct {
	ID       string
	ExitCode int
	State    *RunState
}

func StartRun(ctx context.Context, config *Config, options RunOptions) (RunResult, error) {
	if config == nil {
		return RunResult{ExitCode: 2}, errors.New("configuration is nil")
	}
	if err := config.Validate(); err != nil {
		return RunResult{ExitCode: 2}, err
	}
	agent := options.Agent
	if agent == nil {
		agent = NewPodmanAgent(options.Stdout)
	}
	imageID, err := agent.ResolveImage(ctx)
	if err != nil {
		return RunResult{ExitCode: 2}, err
	}
	id, err := NewRunID(time.Now())
	if err != nil {
		return RunResult{ExitCode: 2}, err
	}
	store := NewRunStore(stateRoot(options.StateDir), id)
	if err := store.Create(); err != nil {
		return RunResult{ID: id, ExitCode: 2}, err
	}
	if err := store.Lock(); err != nil {
		return RunResult{ID: id, ExitCode: 2}, err
	}
	defer store.Unlock()
	if err := validateRunLayout(config, store); err != nil {
		return RunResult{ID: id, ExitCode: 2}, err
	}
	state := &RunState{
		ID:        id,
		ConfigDir: config.BaseDir,
		Status:    StatusPreparing,
		Attempt:   1,
		Phase:     PhaseVerify,
		ImageID:   imageID,
		Outputs:   make(map[string]OutputRecord),
	}
	if err := writeStoredConfig(store.ConfigPath, config); err != nil {
		return RunResult{ID: id, ExitCode: 2, State: state}, err
	}
	if err := store.Save(state); err != nil {
		return RunResult{ID: id, ExitCode: 2, State: state}, err
	}
	return executeRun(ctx, config, state, store, agent, options)
}

func ResumeRun(ctx context.Context, id string, options RunOptions) (RunResult, error) {
	if err := validateRunID(id); err != nil {
		return RunResult{ID: id, ExitCode: 2}, err
	}
	store := NewRunStore(stateRoot(options.StateDir), id)
	if info, err := os.Stat(store.Dir); err != nil {
		return RunResult{ID: id, ExitCode: 2}, fmt.Errorf("find run %s: %w", id, err)
	} else if !info.IsDir() {
		return RunResult{ID: id, ExitCode: 2}, fmt.Errorf("run path is not a directory: %s", store.Dir)
	}
	if err := store.Lock(); err != nil {
		return RunResult{ID: id, ExitCode: 2}, err
	}
	defer store.Unlock()
	state, err := store.Load()
	if err != nil {
		return RunResult{ID: id, ExitCode: 2}, err
	}
	if state.Status == StatusSucceeded {
		return RunResult{ID: id, ExitCode: 0, State: state}, nil
	}
	if state.Status == StatusFailed && state.Attempt >= 1 {
		return RunResult{ID: id, ExitCode: 1, State: state}, nil
	}
	config, err := LoadConfig(store.ConfigPath)
	if err != nil {
		return RunResult{ID: id, ExitCode: 2, State: state}, err
	}
	config.BaseDir = state.ConfigDir
	if err := config.Validate(); err != nil {
		return RunResult{ID: id, ExitCode: 2, State: state}, err
	}
	if err := validateRunLayout(config, store); err != nil {
		return RunResult{ID: id, ExitCode: 2, State: state}, err
	}
	agent := options.Agent
	if agent == nil {
		agent = NewPodmanAgent(options.Stdout)
	}
	return executeRun(ctx, config, state, store, agent, options)
}

func executeRun(
	ctx context.Context,
	config *Config,
	state *RunState,
	store *RunStore,
	agent AgentRunner,
	options RunOptions,
) (RunResult, error) {
	stdout := options.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	state.Status = StatusRunning
	state.BlockedReason = ""
	if err := store.Save(state); err != nil {
		return RunResult{ID: state.ID, ExitCode: 2, State: state}, err
	}

	for state.Attempt <= config.Attempts {
		switch state.Phase {
		case PhaseVerify:
			fmt.Fprintf(stdout, "[%s] attempt %d/%d: verifier running\n", state.ID, state.Attempt, config.Attempts)
			started := time.Now()
			result, err := runVerifier(ctx, config, store, state.Attempt)
			if err != nil {
				if ctx.Err() != nil {
					return stopRun(store, state, StatusStopped, 130, ctx.Err().Error(), "")
				}
				return stopRunWithError(store, state, err)
			}
			switch result.Outcome {
			case VerifySuccess:
				state.Status = StatusSucceeded
				state.Failure = ""
				state.BlockedReason = ""
				state.Outputs = result.Outputs
				if err := store.Save(state); err != nil {
					return RunResult{ID: state.ID, ExitCode: 2, State: state}, err
				}
				fmt.Fprintf(stdout, "[%s] verifier passed (%s)\n", state.ID, elapsed(started))
				return RunResult{ID: state.ID, ExitCode: 0, State: state}, nil
			case VerifyInfrastructureFailure:
				printFailedLog(stdout, result.LogPath)
				fmt.Fprintf(stdout, "[%s] verifier infrastructure failure: %s\n", state.ID, result.Failure)
				return stopRun(store, state, StatusStopped, 2, result.Failure, "")
			case VerifyCandidateFailure:
				state.Failure = result.Failure
				fmt.Fprintf(stdout, "[%s] verifier rejected candidate (%s)\n", state.ID, elapsed(started))
				if state.Attempt == config.Attempts {
					printFailedLog(stdout, result.LogPath)
					return stopRun(store, state, StatusFailed, 1, result.Failure, "")
				}
				state.Phase = PhaseAgent
				if err := store.Save(state); err != nil {
					return RunResult{ID: state.ID, ExitCode: 2, State: state}, err
				}
			}

		case PhaseAgent:
			attemptDir, err := store.EnsureAttempt(state.Attempt)
			if err != nil {
				return stopRunWithError(store, state, err)
			}
			logPath, err := nextAvailablePath(attemptDir, "agent.log")
			if err != nil {
				return stopRunWithError(store, state, err)
			}
			requestPath, err := nextAvailablePath(attemptDir, "request.json")
			if err != nil {
				return stopRunWithError(store, state, err)
			}
			fmt.Fprintf(stdout, "[%s] attempt %d/%d: agent running\n", state.ID, state.Attempt, config.Attempts)
			started := time.Now()
			agentContext, cancel := context.WithTimeout(ctx, time.Duration(config.Agent.Timeout))
			agentResult, turnErr := agent.Turn(agentContext, AgentTurn{
				Config:      config,
				Store:       store,
				Attempt:     state.Attempt,
				ImageID:     state.ImageID,
				LogPath:     logPath,
				RequestPath: requestPath,
				Console:     stdout,
			})
			timedOut := errors.Is(agentContext.Err(), context.DeadlineExceeded)
			cancel()
			if turnErr != nil {
				if ctx.Err() != nil {
					return stopRun(store, state, StatusStopped, 130, ctx.Err().Error(), "")
				}
				if timedOut {
					return stopRun(
						store, state, StatusStopped, 2,
						fmt.Sprintf("agent timed out after %s", time.Duration(config.Agent.Timeout)), "",
					)
				}
				return stopRunWithError(store, state, turnErr)
			}
			if agentResult.BlockedReason != "" {
				fmt.Fprintf(stdout, "[%s] agent blocked: %s\n", state.ID, agentResult.BlockedReason)
				return stopRun(
					store, state, StatusBlocked, 3,
					state.Failure, agentResult.BlockedReason,
				)
			}
			fmt.Fprintf(stdout, "[%s] agent completed (%s)\n", state.ID, elapsed(started))
			state.Attempt++
			state.Phase = PhaseVerify
			state.Failure = ""
			state.BlockedReason = ""
			if err := store.Save(state); err != nil {
				return RunResult{ID: state.ID, ExitCode: 2, State: state}, err
			}

		default:
			return stopRunWithError(store, state, fmt.Errorf("unknown run phase %q", state.Phase))
		}
	}
	return stopRun(store, state, StatusFailed, 1, "attempt limit reached", "")
}

func stopRun(
	store *RunStore,
	state *RunState,
	status Status,
	exitCode int,
	failure, blocked string,
) (RunResult, error) {
	state.Status = status
	state.Failure = failure
	state.BlockedReason = blocked
	if err := store.Save(state); err != nil {
		return RunResult{ID: state.ID, ExitCode: 2, State: state}, err
	}
	return RunResult{ID: state.ID, ExitCode: exitCode, State: state}, nil
}

func stopRunWithError(store *RunStore, state *RunState, err error) (RunResult, error) {
	result, saveErr := stopRun(store, state, StatusStopped, 2, err.Error(), "")
	return result, errors.Join(err, saveErr)
}

func stateRoot(configured string) string {
	if configured != "" {
		return configured
	}
	return DefaultStateRoot()
}

func writeStoredConfig(path string, config *Config) error {
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("encode stored configuration: %w", err)
	}
	if err := atomicWrite(path, data, 0o600); err != nil {
		return fmt.Errorf("write stored configuration: %w", err)
	}
	return nil
}

func validateRunLayout(config *Config, store *RunStore) error {
	runPath, err := filepath.EvalSymlinks(store.Dir)
	if err != nil {
		return fmt.Errorf("resolve run state path: %w", err)
	}
	runPath = filepath.Clean(runPath)
	if strings.Contains(runPath, ",") {
		return errors.New("run state path may not contain a comma")
	}
	for name, candidate := range config.Candidates {
		if pathsOverlap(runPath, candidate) {
			return fmt.Errorf("candidate %q overlaps run state %q", name, runPath)
		}
	}
	for name, reference := range config.References {
		if pathsOverlap(runPath, reference) {
			return fmt.Errorf("reference %q overlaps run state %q", name, runPath)
		}
	}
	return nil
}

func printFailedLog(output io.Writer, path string) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return
	}
	fmt.Fprintf(output, "--- %s ---\n", filepath.Base(path))
	_, _ = output.Write(data)
	if data[len(data)-1] != '\n' {
		fmt.Fprintln(output)
	}
	fmt.Fprintln(output, "--- end verifier output ---")
}

func elapsed(start time.Time) time.Duration {
	result := time.Since(start).Round(100 * time.Millisecond)
	if result < 100*time.Millisecond {
		return 0
	}
	return result
}

func outputSummary(outputs map[string]OutputRecord) string {
	var lines []string
	for _, name := range sortedKeys(outputs) {
		output := outputs[name]
		lines = append(lines, fmt.Sprintf("output %s: %s\nsha256 %s: %s", name, output.Path, name, output.SHA256))
	}
	return strings.Join(lines, "\n")
}
