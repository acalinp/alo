package cli

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

	"alo/internal/config"
	"alo/internal/podman"
	runpkg "alo/internal/run"

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
	case "auth":
		return authCommand(ctx, args[1:], stdin, stdout, stderr)
	case "init":
		return initCommand(ctx, args[1:], stdin, stdout, stderr)
	case "validate":
		return validateCommand(args[1:], stdout, stderr)
	case "try":
		return tryCommand(ctx, args[1:], stdout, stderr)
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
	config, err := config.Load(path)
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
	config, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	result, err := runpkg.Start(ctx, config, runpkg.RunOptions{Stdout: stdout, Runtime: podman.New(stdout)})
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
	result, err := runpkg.Resume(ctx, args[0], runpkg.RunOptions{Stdout: stdout, Runtime: podman.New(stdout)})
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
	if len(args) < 1 || len(args) > 2 {
		fmt.Fprintln(stderr, "usage: alo logs RUN_ID [FILE]")
		return 2
	}
	if err := runpkg.ValidateRunID(args[0]); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	store := runpkg.NewRunStore(runpkg.DefaultStateRoot(), args[0])
	if len(args) == 2 {
		return showLogFile(store.AttemptsDir, args[1], stdout, stderr)
	}
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

func showLogFile(root, name string, stdout, stderr io.Writer) int {
	if filepath.IsAbs(name) {
		fmt.Fprintln(stderr, "log file must be relative to the attempts directory")
		return 2
	}
	path := filepath.Join(root, filepath.Clean(name))
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		fmt.Fprintln(stderr, "log file is outside the attempts directory")
		return 2
	}
	info, err := os.Lstat(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if !info.Mode().IsRegular() {
		fmt.Fprintln(stderr, "log file must be a regular file, not a symlink")
		return 2
	}
	if !strings.HasSuffix(path, ".log") && !strings.HasSuffix(path, ".json") {
		fmt.Fprintln(stderr, "log file must end in .log or .json")
		return 2
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	_, _ = stdout.Write(data)
	return 0
}

func printRunResult(output io.Writer, result runpkg.RunResult) {
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
	if summary := runpkg.OutputSummary(result.State.Outputs); summary != "" {
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
  alo auth set|status|delete openrouter
  alo init
  alo validate [FILE]
  alo try prepare [FILE]
  alo try verify [FILE]
  alo run [FILE]
  alo resume RUN_ID
  alo logs RUN_ID [FILE]`)
}
