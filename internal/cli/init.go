package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"alo/internal/config"

	"gopkg.in/yaml.v3"
)

const prepareTemplate = `#!/usr/bin/env bash
set -Eeuo pipefail

evidence=${ALO_EVIDENCE_DIR:?}

# Reset or initialize the fixture here. Delete the prepare section from
# alo.yaml if the fixture needs no setup.

# Save observations that will help the agent diagnose failures. For example:
# timeout 5s cat /dev/ttyACM0 >"$evidence/serial.log" 2>&1 || true
`

const verifyTemplate = `#!/usr/bin/env bash
set -Eeuo pipefail

candidate=${ALO_CANDIDATE:?}

# Replace this example with an observable check of the goal.
# Exit 0 means success, exit 1 rejects the candidate, and any other nonzero
# exit means the verifier itself failed.
if [[ -f "$candidate/result" ]]; then
	exit 0
fi

echo "candidate failure: expected $candidate/result" >&2
exit 1
`

type initialConfig struct {
	Version   int                  `yaml:"version"`
	Name      string               `yaml:"name"`
	Goal      string               `yaml:"goal"`
	Candidate string               `yaml:"candidate"`
	Agent     initialAgentConfig   `yaml:"agent"`
	Prepare   initialCommandConfig `yaml:"prepare"`
	Verify    initialCommandConfig `yaml:"verify"`
}

type initialAgentConfig struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
	Thinking string `yaml:"thinking"`
	ZDR      bool   `yaml:"zdr"`
	Timeout  string `yaml:"timeout"`
}

type initialCommandConfig struct {
	Command []string `yaml:"command"`
}

func initCommand(ctx context.Context, args []string, input io.Reader, output, errorOutput io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(errorOutput, "usage: alo init")
		return 2
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(errorOutput, err)
		return 2
	}
	reader := bufio.NewReader(input)
	name, err := prompt(ctx, reader, output, "Name", filepath.Base(workingDirectory), false)
	if err != nil {
		return initPromptError(err, output, errorOutput)
	}
	goal, err := prompt(ctx, reader, output, "Goal", "", true)
	if err != nil {
		return initPromptError(err, output, errorOutput)
	}
	candidate, err := prompt(ctx, reader, output, "Candidate directory", "candidate", false)
	if err != nil {
		return initPromptError(err, output, errorOutput)
	}
	candidate = filepath.Clean(candidate)
	if filepath.IsAbs(candidate) || candidate == "." || candidate == ".." || strings.HasPrefix(candidate, ".."+string(filepath.Separator)) {
		fmt.Fprintln(errorOutput, "candidate directory must be a child of the current directory")
		return 2
	}

	initial := initialConfig{
		Version:   1,
		Name:      name,
		Goal:      goal,
		Candidate: "./" + filepath.ToSlash(candidate),
		Agent: initialAgentConfig{
			Provider: "openrouter",
			Model:    "google/gemini-3.8-flash",
			Thinking: "high",
			ZDR:      true,
			Timeout:  "15m",
		},
		Prepare: initialCommandConfig{Command: []string{"./prepare"}},
		Verify:  initialCommandConfig{Command: []string{"./verify"}},
	}
	configData, err := yaml.Marshal(initial)
	if err != nil {
		fmt.Fprintln(errorOutput, err)
		return 2
	}
	files := []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{name: "alo.yaml", data: configData, mode: 0o644},
		{name: "prepare", data: []byte(prepareTemplate), mode: 0o755},
		{name: "verify", data: []byte(verifyTemplate), mode: 0o755},
	}
	for _, file := range files {
		if _, err := os.Lstat(file.name); err == nil {
			fmt.Fprintf(errorOutput, "refusing to overwrite %s\n", file.name)
			return 2
		} else if !os.IsNotExist(err) {
			fmt.Fprintln(errorOutput, err)
			return 2
		}
	}
	candidateCreated := false
	if err := os.Mkdir(candidate, 0o755); err == nil {
		candidateCreated = true
	} else if !os.IsExist(err) {
		fmt.Fprintln(errorOutput, err)
		return 2
	} else if info, statErr := os.Stat(candidate); statErr != nil || !info.IsDir() {
		fmt.Fprintf(errorOutput, "candidate path %q is not a directory\n", candidate)
		return 2
	}
	var created []string
	cleanup := func() {
		for _, path := range created {
			_ = os.Remove(path)
		}
		if candidateCreated {
			_ = os.Remove(candidate)
		}
	}
	for _, file := range files {
		if err := writeNewFile(file.name, file.data, file.mode); err != nil {
			cleanup()
			fmt.Fprintln(errorOutput, err)
			return 2
		}
		created = append(created, file.name)
	}
	if _, err := config.Load("alo.yaml"); err != nil {
		cleanup()
		fmt.Fprintln(errorOutput, "generated configuration is invalid:", err)
		return 2
	}
	fmt.Fprintln(output, "Created alo.yaml, prepare, verify, and", candidate+string(filepath.Separator))
	fmt.Fprintln(output, "Next: run `alo auth set openrouter`, edit the scripts, then use `alo try prepare` and `alo try verify`.")
	return 0
}

func prompt(ctx context.Context, reader *bufio.Reader, output io.Writer, label, fallback string, required bool) (string, error) {
	if fallback == "" {
		fmt.Fprintf(output, "%s: ", label)
	} else {
		fmt.Fprintf(output, "%s [%s]: ", label, fallback)
	}
	type readResult struct {
		value string
		err   error
	}
	result := make(chan readResult, 1)
	go func() {
		value, err := reader.ReadString('\n')
		result <- readResult{value: value, err: err}
	}()
	var value string
	var err error
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case read := <-result:
		value, err = read.value, read.err
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	if required && value == "" {
		return "", fmt.Errorf("%s is required", strings.ToLower(label))
	}
	return value, nil
}

func initPromptError(err error, output, errorOutput io.Writer) int {
	if errors.Is(err, context.Canceled) {
		fmt.Fprintln(output)
		return 130
	}
	fmt.Fprintln(errorOutput, err)
	return 2
}

func writeNewFile(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	return errors.Join(writeErr, closeErr)
}
