package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"alo/internal/config"
)

func TestInitCommandCreatesValidStarterFiles(t *testing.T) {
	root := t.TempDir()
	withWorkingDirectory(t, root)
	var output bytes.Buffer
	var errorOutput bytes.Buffer

	code := initCommand(context.Background(), nil, strings.NewReader("demo\nProduce a result file.\nworkspace\n"), &output, &errorOutput)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, errorOutput.String())
	}
	loaded, err := config.Load(filepath.Join(root, "alo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name != "demo" || loaded.Goal != "Produce a result file." || loaded.Candidate != filepath.Join(root, "workspace") {
		t.Fatalf("generated config = %#v", loaded)
	}
	for _, name := range []string{"prepare", "verify"} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&0o111 == 0 {
			t.Fatalf("%s is not executable", name)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "workspace")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "alo try prepare") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestInitCommandRefusesToOverwrite(t *testing.T) {
	root := t.TempDir()
	withWorkingDirectory(t, root)
	if err := os.WriteFile("verify", []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	var errorOutput bytes.Buffer

	code := initCommand(context.Background(), nil, strings.NewReader("demo\ngoal\ncandidate\n"), &bytes.Buffer{}, &errorOutput)
	if code != 2 || !strings.Contains(errorOutput.String(), "refusing to overwrite verify") {
		t.Fatalf("exit code = %d, stderr = %q", code, errorOutput.String())
	}
	data, err := os.ReadFile("verify")
	if err != nil || string(data) != "keep" {
		t.Fatalf("verify = %q, %v", data, err)
	}
	if _, err := os.Stat("alo.yaml"); !os.IsNotExist(err) {
		t.Fatalf("alo.yaml was created: %v", err)
	}
}

func TestInitCommandCanBeCancelledWhilePrompting(t *testing.T) {
	root := t.TempDir()
	withWorkingDirectory(t, root)
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer

	if code := initCommand(ctx, nil, input, &output, io.Discard); code != 130 {
		t.Fatalf("exit code = %d", code)
	}
	if _, err := os.Stat("alo.yaml"); !os.IsNotExist(err) {
		t.Fatalf("alo.yaml was created: %v", err)
	}
}

func TestTryCommandsExplainOutcomesAndEvidence(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "candidate")
	mustMkdir(t, candidate)
	writeExecutable(t, root, "prepare", `#!/bin/sh
printf evidence > "$ALO_EVIDENCE_DIR/serial.log"
: > "$ALO_EVIDENCE_DIR/empty.log"
`)
	writeExecutable(t, root, "verify", "#!/bin/sh\nexit 1\n")
	config := `version: 1
name: trial
goal: Exercise trusted phases.
candidate: ./candidate
prepare:
  command: [./prepare]
verify:
  command: [./verify]
`
	configPath := filepath.Join(root, "alo.yaml")
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	var prepareOutput bytes.Buffer
	var errorOutput bytes.Buffer
	if code := tryCommand(context.Background(), []string{"prepare", configPath}, &prepareOutput, &errorOutput); code != 0 {
		t.Fatalf("prepare code = %d, stderr = %q", code, errorOutput.String())
	}
	for _, want := range []string{"prepare succeeded", "empty.log\tempty", "serial.log\t8 bytes"} {
		if !strings.Contains(prepareOutput.String(), want) {
			t.Fatalf("prepare output does not contain %q:\n%s", want, prepareOutput.String())
		}
	}

	var verifyOutput bytes.Buffer
	errorOutput.Reset()
	if code := tryCommand(context.Background(), []string{"verify", configPath}, &verifyOutput, &errorOutput); code != 1 {
		t.Fatalf("verify code = %d, stderr = %q", code, errorOutput.String())
	}
	if !strings.Contains(verifyOutput.String(), "verify rejected the candidate (exit 1)") ||
		!strings.Contains(verifyOutput.String(), "verify.log\tempty") {
		t.Fatalf("verify output = %q", verifyOutput.String())
	}
}

func withWorkingDirectory(t *testing.T, directory string) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(directory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Error(err)
		}
	})
}

func writeExecutable(t *testing.T, directory, name, content string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}
