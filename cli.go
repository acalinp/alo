package alo

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"
)

func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	switch args[0] {
	case "validate":
		return validateCommand(args[1:], stdout, stderr)
	case "run":
		return runCommand(ctx, args[1:], stdout, stderr)
	case "resume":
		return resumeCommand(ctx, args[1:], stdout, stderr)
	case "logs":
		return logsCommand(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func validateCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		fmt.Fprintln(stderr, "usage: alo validate [FILE]")
		return 2
	}
	path := "alo.yaml"
	if len(args) == 1 {
		path = args[0]
	}
	config, err := LoadConfig(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	data, err := yaml.Marshal(config)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	_, _ = stdout.Write(data)
	return 0
}

func runCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		fmt.Fprintln(stderr, "usage: alo run [FILE]")
		return 2
	}
	path := "alo.yaml"
	if len(args) == 1 {
		path = args[0]
	}
	config, err := LoadConfig(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	result, err := StartRun(ctx, config, RunOptions{Stdout: stdout})
	if err != nil {
		fmt.Fprintln(stderr, err)
	}
	printRunResult(stdout, result)
	if result.ExitCode == 0 && err != nil {
		return 2
	}
	return result.ExitCode
}

func resumeCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: alo resume RUN_ID")
		return 2
	}
	result, err := ResumeRun(ctx, args[0], RunOptions{Stdout: stdout})
	if err != nil {
		fmt.Fprintln(stderr, err)
	}
	printRunResult(stdout, result)
	if result.ExitCode == 0 && err != nil {
		return 2
	}
	return result.ExitCode
}

func logsCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: alo logs RUN_ID")
		return 2
	}
	if err := validateRunID(args[0]); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	store := NewRunStore(DefaultStateRoot(), args[0])
	var paths []string
	err := filepath.WalkDir(store.AttemptsDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && (strings.HasSuffix(path, ".log") || strings.HasSuffix(path, ".json")) {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	sort.Strings(paths)
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		relative, _ := filepath.Rel(store.AttemptsDir, path)
		fmt.Fprintf(stdout, "%s\t%d bytes\n", relative, info.Size())
	}
	return 0
}

func printRunResult(output io.Writer, result RunResult) {
	if result.ID != "" {
		fmt.Fprintf(output, "run: %s\n", result.ID)
	}
	if result.State == nil {
		return
	}
	fmt.Fprintf(output, "status: %s\n", result.State.Status)
	if result.State.Failure != "" {
		fmt.Fprintf(output, "failure: %s\n", result.State.Failure)
	}
	if result.State.BlockedReason != "" {
		fmt.Fprintf(output, "blocked: %s\n", result.State.BlockedReason)
	}
	if summary := outputSummary(result.State.Outputs); summary != "" {
		fmt.Fprintln(output, summary)
	}
	if result.ExitCode != 0 {
		fmt.Fprintf(output, "logs: alo logs %s\n", result.ID)
		fmt.Fprintf(output, "resume: alo resume %s\n", result.ID)
	}
}

func printUsage(output io.Writer) {
	fmt.Fprintln(output, `Alo runs a trusted verifier and puts a containerized agent in its failure loop.

Usage:
  alo validate [FILE]
  alo run [FILE]
  alo resume RUN_ID
  alo logs RUN_ID`)
}
