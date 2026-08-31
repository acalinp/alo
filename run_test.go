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

type fakeAgent struct {
	turns int
	turn  func(context.Context, AgentTurn) (AgentResult, error)
}

func (a *fakeAgent) ResolveImage(context.Context) (string, error) {
	return "sha256:test-agent", nil
}

func (a *fakeAgent) Turn(ctx context.Context, turn AgentTurn) (AgentResult, error) {
	a.turns++
	if a.turn == nil {
		return AgentResult{}, nil
	}
	return a.turn(ctx, turn)
}

func TestRunRepairsArtifactAndSucceeds(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "candidate")
	mustMkdir(t, candidate)
	verifier := writeExecutable(t, root, "verify", `#!/bin/sh
set -eu
artifact="$ALO_CANDIDATE_PROJECT/out/firmware.bin"
if [ ! -s "$artifact" ]; then
    echo "firmware artifact is missing"
    exit 1
fi
printf '{"outputs":{"firmware":"%s"}}\n' "$artifact" > "$ALO_RESULT"
`)
	config := testConfig(root, candidate, verifier)
	agent := &fakeAgent{turn: func(_ context.Context, turn AgentTurn) (AgentResult, error) {
		artifact := filepath.Join(turn.Config.Candidates["project"], "out", "firmware.bin")
		mustMkdir(t, filepath.Dir(artifact))
		if err := os.WriteFile(artifact, []byte("agent-built-firmware"), 0o644); err != nil {
			t.Fatal(err)
		}
		return AgentResult{}, nil
	}}

	result, err := StartRun(context.Background(), config, RunOptions{
		StateDir: filepath.Join(root, "state"),
		Agent:    agent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.State.Status != StatusSucceeded {
		t.Fatalf("result = code %d, status %s", result.ExitCode, result.State.Status)
	}
	if agent.turns != 1 || result.State.Attempt != 2 {
		t.Fatalf("turns = %d, attempt = %d", agent.turns, result.State.Attempt)
	}
	output := result.State.Outputs["firmware"]
	wantDigest := sha256.Sum256([]byte("agent-built-firmware"))
	if output.SHA256 != hex.EncodeToString(wantDigest[:]) || output.Size != 20 {
		t.Fatalf("output = %#v", output)
	}
	firstLog := filepath.Join(root, "state", "runs", result.ID, "attempts", "0001", "verify.log")
	data, err := os.ReadFile(firstLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "firmware artifact is missing") {
		t.Fatalf("first evidence = %q", data)
	}
}

func TestRunTerminalOutcomes(t *testing.T) {
	tests := []struct {
		name        string
		verifyExit  int
		attempts    int
		agentResult AgentResult
		wantCode    int
		wantStatus  Status
		wantTurns   int
	}{
		{name: "verifier infrastructure", verifyExit: 2, attempts: 2, wantCode: 2, wantStatus: StatusStopped},
		{name: "agent blocked", verifyExit: 1, attempts: 2, agentResult: AgentResult{BlockedReason: "missing board data"}, wantCode: 3, wantStatus: StatusBlocked, wantTurns: 1},
		{name: "attempts exhausted", verifyExit: 1, attempts: 1, wantCode: 1, wantStatus: StatusFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			candidate := filepath.Join(root, "candidate")
			mustMkdir(t, candidate)
			verifier := writeExecutable(t, root, "verify", "#!/bin/sh\nexit "+string(rune('0'+test.verifyExit))+"\n")
			config := testConfig(root, candidate, verifier)
			config.Attempts = test.attempts
			agent := &fakeAgent{turn: func(context.Context, AgentTurn) (AgentResult, error) {
				return test.agentResult, nil
			}}
			result, err := StartRun(context.Background(), config, RunOptions{
				StateDir: filepath.Join(root, "state"),
				Agent:    agent,
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.ExitCode != test.wantCode || result.State.Status != test.wantStatus {
				t.Fatalf("result = code %d, status %s", result.ExitCode, result.State.Status)
			}
			if agent.turns != test.wantTurns {
				t.Fatalf("agent turns = %d, want %d", agent.turns, test.wantTurns)
			}
		})
	}
}

func TestResumeInterruptedAgentTurn(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "candidate")
	mustMkdir(t, candidate)
	verifier := writeExecutable(t, root, "verify", `#!/bin/sh
test -f "$ALO_CANDIDATE_PROJECT/complete" || exit 1
`)
	config := testConfig(root, candidate, verifier)
	ctx, cancel := context.WithCancel(context.Background())
	firstAgent := &fakeAgent{turn: func(ctx context.Context, turn AgentTurn) (AgentResult, error) {
		if err := os.WriteFile(filepath.Join(turn.Config.Candidates["project"], "partial"), []byte("partial"), 0o644); err != nil {
			t.Fatal(err)
		}
		cancel()
		<-ctx.Done()
		return AgentResult{}, ctx.Err()
	}}
	first, err := StartRun(ctx, config, RunOptions{
		StateDir: filepath.Join(root, "state"),
		Agent:    firstAgent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ExitCode != 130 || first.State.Phase != PhaseAgent {
		t.Fatalf("interrupted result = code %d, phase %s", first.ExitCode, first.State.Phase)
	}
	secondAgent := &fakeAgent{turn: func(_ context.Context, turn AgentTurn) (AgentResult, error) {
		if _, err := os.Stat(filepath.Join(turn.Config.Candidates["project"], "partial")); err != nil {
			t.Fatalf("partial edit was not retained: %v", err)
		}
		return AgentResult{}, os.WriteFile(
			filepath.Join(turn.Config.Candidates["project"], "complete"),
			[]byte("complete"),
			0o644,
		)
	}}
	resumed, err := ResumeRun(context.Background(), first.ID, RunOptions{
		StateDir: filepath.Join(root, "state"),
		Agent:    secondAgent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ExitCode != 0 || resumed.State.Status != StatusSucceeded || secondAgent.turns != 1 {
		t.Fatalf("resumed = code %d, status %s, turns %d", resumed.ExitCode, resumed.State.Status, secondAgent.turns)
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
	config.Agent.Devices = []string{root}
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "beneath /dev") {
		t.Fatalf("unsafe device error = %v", err)
	}
}

func testConfig(base, candidate, verifier string) *Config {
	config := &Config{
		Version:    1,
		Name:       "test-loop",
		Goal:       "Produce the requested artifact.",
		Candidates: map[string]string{"project": candidate},
		References: make(map[string]string),
		Agent:      AgentConfig{},
		Verify:     VerifyConfig{Command: []string{verifier}},
		Attempts:   3,
		BaseDir:    base,
	}
	config.setDefaults()
	return config
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
