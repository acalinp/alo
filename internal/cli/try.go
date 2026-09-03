package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"alo/internal/config"
	runpkg "alo/internal/run"
)

func tryCommand(ctx context.Context, args []string, output, errorOutput io.Writer) int {
	if len(args) < 1 || len(args) > 2 || (args[0] != "prepare" && args[0] != "verify") {
		fmt.Fprintln(errorOutput, "usage: alo try prepare|verify [FILE]")
		return 2
	}
	path := "alo.yaml"
	if len(args) == 2 {
		path = args[1]
	}
	config, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(errorOutput, err)
		return 2
	}
	root, err := os.MkdirTemp("", "alo-try-")
	if err != nil {
		fmt.Fprintln(errorOutput, err)
		return 2
	}
	defer os.RemoveAll(root)
	store := runpkg.NewRunStore(root, "try")
	if err := store.Create(); err != nil {
		fmt.Fprintln(errorOutput, err)
		return 2
	}
	if _, err := store.EnsureAttempt(1); err != nil {
		fmt.Fprintln(errorOutput, err)
		return 2
	}

	var code int
	switch args[0] {
	case "prepare":
		code = tryPrepare(ctx, config, store, output)
	case "verify":
		code = tryVerify(ctx, config, store, output)
	}
	if err := printTrialEvidence(output, store.AttemptDir(1)); err != nil {
		fmt.Fprintln(errorOutput, err)
		return 2
	}
	return code
}

func tryPrepare(ctx context.Context, config *config.Config, store *runpkg.RunStore, output io.Writer) int {
	if config.Prepare == nil {
		fmt.Fprintln(output, "prepare is not configured")
		return 0
	}
	logPath, err := runpkg.RunPrepare(ctx, config, store, 1)
	if err == nil {
		fmt.Fprintln(output, "prepare succeeded")
		return 0
	}
	if ctx.Err() != nil {
		fmt.Fprintln(output, "prepare stopped:", ctx.Err())
		printTrialOutput(output, logPath)
		return 130
	}
	fmt.Fprintln(output, "prepare failed:", err)
	printTrialOutput(output, logPath)
	return 2
}

func tryVerify(ctx context.Context, config *config.Config, store *runpkg.RunStore, output io.Writer) int {
	result, err := runpkg.RunVerifier(ctx, config, store, 1)
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(output, "verify stopped:", ctx.Err())
			printTrialOutput(output, result.LogPath)
			return 130
		}
		fmt.Fprintln(output, "verify failed:", err)
		printTrialOutput(output, result.LogPath)
		return 2
	}
	switch result.Outcome {
	case runpkg.VerifySuccess:
		fmt.Fprintln(output, "verify succeeded (exit 0)")
		if summary := runpkg.OutputSummary(result.Outputs); summary != "" {
			fmt.Fprintln(output, summary)
		}
		return 0
	case runpkg.VerifyCandidateFailure:
		fmt.Fprintln(output, "verify rejected the candidate (exit 1)")
		printTrialOutput(output, result.LogPath)
		return 1
	default:
		fmt.Fprintln(output, "verify failed (infrastructure):", result.Failure)
		printTrialOutput(output, result.LogPath)
		return 2
	}
}

func printTrialOutput(output io.Writer, path string) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return
	}
	const maximum = 16 << 10
	omitted := 0
	if len(data) > maximum {
		omitted = len(data) - maximum
		data = data[omitted:]
	}
	fmt.Fprintln(output)
	if omitted > 0 {
		fmt.Fprintf(output, "output (last 16 KiB; %d bytes omitted):\n", omitted)
	} else {
		fmt.Fprintln(output, "output:")
	}
	text := strings.TrimSuffix(string(data), "\n")
	fmt.Fprintln(output, "  "+strings.ReplaceAll(text, "\n", "\n  "))
}

func printTrialEvidence(output io.Writer, directory string) error {
	files, err := runpkg.EvidenceFiles(directory)
	if err != nil {
		return err
	}
	fmt.Fprintln(output, "evidence:")
	for _, file := range files {
		fmt.Fprintf(output, "  %s\t%s\n", file.Name, runpkg.EvidenceSize(file.Size))
	}
	return nil
}
