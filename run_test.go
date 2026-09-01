package alo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRuntime struct {
	turns     int
	exercises int
	cleanups  int
	turn      func(context.Context, AgentTurn) (AgentResult, error)
}

func (r *fakeRuntime) ResolveImage(context.Context) (string, error) {
	return "sha256:test-agent", nil
}

func (r *fakeRuntime) Exercise(ctx context.Context, turn ExerciseTurn) (ExerciseResult, error) {
	r.exercises++
	logFile, err := os.OpenFile(turn.LogPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return ExerciseResult{}, err
	}
	defer logFile.Close()
	result, err := runProcess(
		ctx,
		turn.Config.Candidate,
		[]string{filepath.Join(turn.Config.Candidate, ".alo", "run")},
		os.Environ(),
		logFile,
		logFile,
	)
	return ExerciseResult{ExitCode: result.ExitCode}, err
}

func (r *fakeRuntime) Turn(ctx context.Context, turn AgentTurn) (AgentResult, error) {
	r.turns++
	if r.turn == nil {
		return AgentResult{}, nil
	}
	return r.turn(ctx, turn)
}

func (r *fakeRuntime) RemoveWorkshop(context.Context, *RunStore) error {
	r.cleanups++
	return nil
}

func TestRunReplaysAgentBuiltCandidateAndSucceeds(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "candidate")
	mustMkdir(t, candidate)
	prepareCount := filepath.Join(root, "prepare-count")
	prepare := writeExecutable(t, root, "prepare", `#!/bin/sh
set -eu
printf 'prepared\n' >> "`+prepareCount+`"
`)
	verifier := writeExecutable(t, root, "verify", `#!/bin/sh
set -eu
artifact="$ALO_CANDIDATE/out/firmware.bin"
if [ ! -s "$artifact" ]; then
    echo "firmware artifact is missing"
    exit 1
fi
`)
	config := testConfig(root, candidate, verifier)
	config.Prepare = &CommandConfig{Command: []string{prepare}, Timeout: Duration(defaultCommandTimeout)}
	runtime := &fakeRuntime{turn: func(_ context.Context, turn AgentTurn) (AgentResult, error) {
		entrypoint := filepath.Join(turn.Config.Candidate, ".alo", "run")
		mustMkdir(t, filepath.Dir(entrypoint))
		return AgentResult{}, os.WriteFile(entrypoint, []byte(`#!/bin/sh
set -eu
mkdir -p out
printf agent-built-firmware > out/firmware.bin
printf '{"outputs":{"firmware":"out/firmware.bin"}}\n' > .alo/outputs.json
`), 0o755)
	}}

	result, err := StartRun(context.Background(), config, RunOptions{
		StateDir: filepath.Join(root, "state"),
		Runtime:  runtime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.State.Status != StatusSucceeded {
		t.Fatalf("result = code %d, status %s", result.ExitCode, result.State.Status)
	}
	if runtime.turns != 1 || runtime.exercises != 1 || runtime.cleanups != 1 || result.State.Attempt != 2 {
		t.Fatalf("turns=%d exercises=%d cleanups=%d attempt=%d", runtime.turns, runtime.exercises, runtime.cleanups, result.State.Attempt)
	}
	prepared, err := os.ReadFile(prepareCount)
	if err != nil || strings.Count(string(prepared), "prepared") != 2 {
		t.Fatalf("prepare evidence = %q, %v", prepared, err)
	}
	output := result.State.Outputs["firmware"]
	wantDigest := sha256.Sum256([]byte("agent-built-firmware"))
	if output.SHA256 != hex.EncodeToString(wantDigest[:]) || output.Size != 20 {
		t.Fatalf("output = %#v", output)
	}
	firstExercise := filepath.Join(root, "state", "runs", result.ID, "attempts", "0001", "exercise.log")
	data, err := os.ReadFile(firstExercise)
	if err != nil || !strings.Contains(string(data), ".alo/run is missing") {
		t.Fatalf("first exercise evidence = %q, %v", data, err)
	}
}

func TestRunTerminalOutcomes(t *testing.T) {
	tests := []struct {
		name        string
		verifyExit  int
		exercise    int
		attempts    int
		agentResult AgentResult
		wantCode    int
		wantStatus  Status
		wantTurns   int
		wantCleanup int
	}{
		{name: "verifier infrastructure", verifyExit: 2, attempts: 2, wantCode: 2, wantStatus: StatusStopped},
		{name: "agent blocked", verifyExit: 1, attempts: 2, agentResult: AgentResult{BlockedReason: "missing board data"}, wantCode: 3, wantStatus: StatusBlocked, wantTurns: 1},
		{name: "attempts exhausted", verifyExit: 1, attempts: 1, wantCode: 1, wantStatus: StatusFailed, wantCleanup: 1},
		{name: "replay failure cannot pass", verifyExit: 0, exercise: 7, attempts: 1, wantCode: 1, wantStatus: StatusFailed, wantCleanup: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			candidate := filepath.Join(root, "candidate")
			mustMkdir(t, candidate)
			writeCandidateEntrypoint(t, candidate, "#!/bin/sh\nexit "+string(rune('0'+test.exercise))+"\n")
			verifier := writeExecutable(t, root, "verify", "#!/bin/sh\nexit "+string(rune('0'+test.verifyExit))+"\n")
			config := testConfig(root, candidate, verifier)
			config.Attempts = test.attempts
			runtime := &fakeRuntime{turn: func(context.Context, AgentTurn) (AgentResult, error) {
				return test.agentResult, nil
			}}
			result, err := StartRun(context.Background(), config, RunOptions{
				StateDir: filepath.Join(root, "state"),
				Runtime:  runtime,
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.ExitCode != test.wantCode || result.State.Status != test.wantStatus {
				t.Fatalf("result = code %d, status %s", result.ExitCode, result.State.Status)
			}
			if runtime.turns != test.wantTurns || runtime.cleanups != test.wantCleanup {
				t.Fatalf("turns=%d cleanups=%d", runtime.turns, runtime.cleanups)
			}
		})
	}
}

func TestResumeInterruptedAgentTurn(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "candidate")
	mustMkdir(t, candidate)
	writeCandidateEntrypoint(t, candidate, "#!/bin/sh\nexit 0\n")
	verifier := writeExecutable(t, root, "verify", `#!/bin/sh
test -f "$ALO_CANDIDATE/complete" || exit 1
`)
	config := testConfig(root, candidate, verifier)
	ctx, cancel := context.WithCancel(context.Background())
	firstRuntime := &fakeRuntime{turn: func(ctx context.Context, turn AgentTurn) (AgentResult, error) {
		if err := os.WriteFile(filepath.Join(turn.Config.Candidate, "partial"), []byte("partial"), 0o644); err != nil {
			t.Fatal(err)
		}
		cancel()
		<-ctx.Done()
		return AgentResult{}, ctx.Err()
	}}
	first, err := StartRun(ctx, config, RunOptions{
		StateDir: filepath.Join(root, "state"),
		Runtime:  firstRuntime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ExitCode != 130 || first.State.Phase != PhaseAgent {
		t.Fatalf("interrupted result = code %d, phase %s", first.ExitCode, first.State.Phase)
	}
	secondRuntime := &fakeRuntime{turn: func(_ context.Context, turn AgentTurn) (AgentResult, error) {
		if _, err := os.Stat(filepath.Join(turn.Config.Candidate, "partial")); err != nil {
			t.Fatalf("partial edit was not retained: %v", err)
		}
		return AgentResult{}, os.WriteFile(filepath.Join(turn.Config.Candidate, "complete"), []byte("complete"), 0o644)
	}}
	resumed, err := ResumeRun(context.Background(), first.ID, RunOptions{
		StateDir: filepath.Join(root, "state"),
		Runtime:  secondRuntime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ExitCode != 0 || resumed.State.Status != StatusSucceeded || secondRuntime.turns != 1 {
		t.Fatalf("resumed = code %d, status %s, turns %d", resumed.ExitCode, resumed.State.Status, secondRuntime.turns)
	}
}

func TestConfigRejectsUnsafeLayouts(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "candidate")
	mustMkdir(t, candidate)
	verifier := writeExecutable(t, candidate, "verify", "#!/bin/sh\nexit 0\n")
	config := testConfig(root, candidate, verifier)
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "inside candidate") {
		t.Fatalf("unsafe verifier error = %v", err)
	}

	outsideVerifier := writeExecutable(t, root, "safe-verify", "#!/bin/sh\nexit 0\n")
	config = testConfig(root, candidate, outsideVerifier)
	config.References["nested"] = filepath.Join(candidate, "reference")
	mustMkdir(t, config.References["nested"])
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("overlap error = %v", err)
	}

	linkedCandidate := filepath.Join(root, "linked-candidate")
	if err := os.Symlink(root, linkedCandidate); err != nil {
		t.Fatal(err)
	}
	config = testConfig(root, linkedCandidate, outsideVerifier)
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "inside candidate") {
		t.Fatalf("symlinked candidate error = %v", err)
	}

	config = testConfig(root, candidate, outsideVerifier)
	config.Sandbox.Devices = []string{root}
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "beneath /dev") {
		t.Fatalf("unsafe device error = %v", err)
	}
}

func testConfig(base, candidate, verifier string) *Config {
	config := &Config{
		Version:    1,
		Name:       "test-loop",
		Goal:       "Produce the requested artifact.",
		Candidate:  candidate,
		References: make(map[string]string),
		Verify:     CommandConfig{Command: []string{verifier}},
		Attempts:   3,
		BaseDir:    base,
	}
	config.setDefaults()
	return config
}

func writeCandidateEntrypoint(t *testing.T, candidate, content string) {
	t.Helper()
	directory := filepath.Join(candidate, ".alo")
	mustMkdir(t, directory)
	if err := os.WriteFile(filepath.Join(directory, "run"), []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
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
