package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"alo/internal/auth"
	"alo/internal/config"
	runpkg "alo/internal/run"
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
	if loaded.Agent.Provider != "openrouter" || loaded.Agent.Model != "google/gemini-3.8-flash" ||
		loaded.Agent.Thinking != "high" || !loaded.Agent.ZDR || len(loaded.Agent.PassEnv) != 0 ||
		time.Duration(loaded.Agent.BootstrapTimeout) != 30*time.Minute || time.Duration(loaded.Agent.Timeout) != 5*time.Minute {
		t.Fatalf("generated agent config = %#v", loaded.Agent)
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

func TestAuthCommandsManageCredentialWithoutPrintingIt(t *testing.T) {
	previous := credentialStore
	previousFile := fileCredentialStore
	store := &memoryCredentialStore{values: make(map[string]string)}
	credentialStore = store
	fileStore := &memoryCredentialStore{values: make(map[string]string)}
	fileCredentialStore = fileStore
	t.Cleanup(func() {
		credentialStore = previous
		fileCredentialStore = previousFile
	})
	var output bytes.Buffer
	var errorOutput bytes.Buffer

	if code := authCommand(context.Background(), []string{"set", "openrouter"}, strings.NewReader("secret-key\n"), &output, &errorOutput); code != 0 {
		t.Fatalf("set code = %d, stderr = %q", code, errorOutput.String())
	}
	if store.values["openrouter"] != "secret-key" || strings.Contains(output.String(), "secret-key") {
		t.Fatalf("stored = %q, output = %q", store.values["openrouter"], output.String())
	}
	output.Reset()
	if code := authCommand(context.Background(), []string{"status", "openrouter"}, strings.NewReader(""), &output, &errorOutput); code != 0 ||
		!strings.Contains(output.String(), "is configured") {
		t.Fatalf("status code = %d, output = %q, stderr = %q", code, output.String(), errorOutput.String())
	}
	output.Reset()
	if code := authCommand(context.Background(), []string{"delete", "openrouter"}, strings.NewReader(""), &output, &errorOutput); code != 0 {
		t.Fatalf("delete code = %d, stderr = %q", code, errorOutput.String())
	}
	if _, exists := store.values["openrouter"]; exists {
		t.Fatal("credential was not deleted")
	}
	output.Reset()
	if code := authCommand(context.Background(), []string{"set", "openrouter", "--file"}, strings.NewReader("file-key\n"), &output, &errorOutput); code != 0 {
		t.Fatalf("file set code = %d, stderr = %q", code, errorOutput.String())
	}
	if fileStore.values["openrouter"] != "file-key" || !strings.Contains(output.String(), "credential file") {
		t.Fatalf("file stored = %q, output = %q", fileStore.values["openrouter"], output.String())
	}
}

func TestAuthSetCanBeCancelled(t *testing.T) {
	previous := credentialStore
	store := &memoryCredentialStore{values: make(map[string]string)}
	credentialStore = store
	t.Cleanup(func() { credentialStore = previous })
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if code := authCommand(ctx, []string{"set", "openrouter"}, input, io.Discard, io.Discard); code != 130 {
		t.Fatalf("exit code = %d", code)
	}
	if len(store.values) != 0 {
		t.Fatalf("stored credentials = %#v", store.values)
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
	writeExecutable(t, root, "verify", "#!/bin/sh\necho 'result was not ready' >&2\nexit 1\n")
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
		!strings.Contains(verifyOutput.String(), "verify output:\n  result was not ready") ||
		!strings.Contains(verifyOutput.String(), "verify.log\t21 bytes") {
		t.Fatalf("verify output = %q", verifyOutput.String())
	}

	writeExecutable(t, root, "failed-prepare", "#!/bin/sh\necho 'serial device is unavailable' >&2\nexit 2\n")
	failedConfig := strings.Replace(config, "command: [./prepare]", "command: [./failed-prepare]", 1)
	if err := os.WriteFile(configPath, []byte(failedConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	var failedOutput bytes.Buffer
	errorOutput.Reset()
	if code := tryCommand(context.Background(), []string{"prepare", configPath}, &failedOutput, &errorOutput); code != 2 {
		t.Fatalf("failed prepare code = %d", code)
	}
	if !strings.Contains(failedOutput.String(), "prepare failed: prepare exited with status 2\n\nprepare output:\n  serial device is unavailable") ||
		!strings.Contains(failedOutput.String(), "prepare.log\t29 bytes") {
		t.Fatalf("failed prepare output = %q", failedOutput.String())
	}
	if errorOutput.Len() != 0 {
		t.Fatalf("failed prepare stderr = %q", errorOutput.String())
	}
}

func TestLogsCommandListsAndDisplaysRetainedFiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ALO_STATE_DIR", root)
	store := runpkg.NewRunStore(root, "test-run")
	if err := store.Create(); err != nil {
		t.Fatal(err)
	}
	attempt, err := store.EnsureAttempt(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attempt, "prepare.log"), []byte("serial unavailable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	if code := logsCommand([]string{"test-run"}, &output, &errorOutput); code != 0 ||
		!strings.Contains(output.String(), "0001/prepare.log\t19 bytes") {
		t.Fatalf("list code = %d, output = %q, stderr = %q", code, output.String(), errorOutput.String())
	}
	output.Reset()
	if code := logsCommand([]string{"test-run", "0001/prepare.log"}, &output, &errorOutput); code != 0 || output.String() != "serial unavailable\n" {
		t.Fatalf("show code = %d, output = %q, stderr = %q", code, output.String(), errorOutput.String())
	}
}

func TestLogsCommandRejectsPathsOutsideRun(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ALO_STATE_DIR", root)
	store := runpkg.NewRunStore(root, "test-run")
	if err := store.Create(); err != nil {
		t.Fatal(err)
	}
	var errorOutput bytes.Buffer
	if code := logsCommand([]string{"test-run", "../state.json"}, io.Discard, &errorOutput); code != 2 ||
		!strings.Contains(errorOutput.String(), "outside the attempts directory") {
		t.Fatalf("code = %d, stderr = %q", code, errorOutput.String())
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

type memoryCredentialStore struct {
	values map[string]string
}

func (s *memoryCredentialStore) Set(provider, secret string) error {
	s.values[provider] = secret
	return nil
}

func (s *memoryCredentialStore) Get(provider string) (string, error) {
	secret, exists := s.values[provider]
	if !exists {
		return "", auth.ErrNotFound
	}
	return secret, nil
}

func (s *memoryCredentialStore) Delete(provider string) error {
	if _, exists := s.values[provider]; !exists {
		return auth.ErrNotFound
	}
	delete(s.values, provider)
	return nil
}
