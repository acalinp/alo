package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"alo/internal/config"
)

func RunPrepare(ctx context.Context, config *config.Config, store *RunStore, attempt int) (string, error) {
	if config.Prepare == nil {
		return "", nil
	}
	attemptDir, err := store.EnsureAttempt(attempt)
	if err != nil {
		return "", err
	}
	logPath, err := nextAvailablePath(attemptDir, "prepare.log")
	if err != nil {
		return "", err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create prepare log: %w", err)
	}
	defer logFile.Close()

	prepareContext, cancel := context.WithTimeout(ctx, time.Duration(config.Prepare.Timeout))
	result, runErr := runProcess(
		prepareContext,
		config.BaseDir,
		config.Prepare.Command,
		trustedEnvironment(config, config.Prepare.PassEnv, attempt, attemptDir, ""),
		logFile,
		logFile,
	)
	contextErr := prepareContext.Err()
	cancel()
	switch {
	case runErr != nil && contextErr == nil:
		return logPath, runErr
	case ctx.Err() != nil:
		return logPath, ctx.Err()
	case result.TimedOut || errors.Is(contextErr, context.DeadlineExceeded):
		return logPath, fmt.Errorf("prepare timed out after %s", time.Duration(config.Prepare.Timeout))
	case result.ExitCode != 0:
		return logPath, fmt.Errorf("prepare exited with status %d", result.ExitCode)
	default:
		return logPath, nil
	}
}
