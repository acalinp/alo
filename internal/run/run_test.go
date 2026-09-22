package run

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"alo/internal/config"
)

type fakeRuntime struct {
	turns     int
	exercises int
	cleanups  int
	turn      func(context.Context, AgentTurn) (AgentResult, error)
}

type cancellingRuntime struct {
	fakeRuntime *fakeRuntime
	cancel      context.CancelFunc
}

func (r *cancellingRuntime) ResolveImage(ctx context.Context) (string, error) {
	return r.fakeRuntime.ResolveImage(ctx)
}

func (r *cancellingRuntime) Exercise(ctx context.Context, turn ExerciseTurn) (ExerciseResult, error) {
	r.cancel()
	<-ctx.Done()
	return ExerciseResult{}, ctx.Err()
}

func (r *cancellingRuntime) Turn(ctx context.Context, turn AgentTurn) (AgentResult, error) {
	return r.fakeRuntime.Turn(ctx, turn)
}

func (r *cancellingRuntime) RemoveWorkshop(ctx context.Context, store *RunStore) error {
	return r.fakeRuntime.RemoveWorkshop(ctx, store)
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
	configuration := testConfig(root, candidate, verifier)
	configuration.Prepare = &config.CommandConfig{Command: []string{prepare}, Timeout: config.Duration(30 * time.Minute)}
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

	result, err := Start(context.Background(), configuration, RunOptions{
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

func TestRunSummarizesEvidenceBeforeAgentTurn(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "candidate")
	mustMkdir(t, candidate)
	prepare := writeExecutable(t, root, "prepare", `#!/bin/sh
set -eu
printf serial-data > "$ALO_EVIDENCE_DIR/serial.log"
: > "$ALO_EVIDENCE_DIR/empty.log"
`)
	verifier := writeExecutable(t, root, "verify", "#!/bin/sh\nexit 1\n")
	configuration := testConfig(root, candidate, verifier)
	configuration.Prepare = &config.CommandConfig{Command: []string{prepare}, Timeout: config.Duration(30 * time.Minute)}
	configuration.Attempts = 2
	var output bytes.Buffer
	runtime := &fakeRuntime{turn: func(context.Context, AgentTurn) (AgentResult, error) {
		return AgentResult{BlockedReason: "stop after evidence summary"}, nil
	}}

	result, err := Start(context.Background(), configuration, RunOptions{
		StateDir: filepath.Join(root, "state"),
		Stdout:   &output,
		Runtime:  runtime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 3 {
		t.Fatalf("exit code = %d", result.ExitCode)
	}
	for _, want := range []string{
		"evidence: empty.log (empty)",
		"evidence: prepare.log (empty)",
		"evidence: serial.log (11 bytes)",
		"evidence: exercise.log (",
		"evidence: verify.log (empty)",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output does not contain %q:\n%s", want, output.String())
		}
	}
}

func TestCaptureSpansPrepareAndReplayAndStopsBeforeVerify(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "candidate")
	mustMkdir(t, candidate)
	events := filepath.Join(root, "events")
	capture := writeExecutable(t, root, "capture", `#!/bin/sh
set -eu
events=$1
trap 'printf "stopped\n" >> "$events"; exit 0' TERM
printf "started\n" >> "$events"
printf . >&3
exec 3>&-
while :; do printf "captured\n"; sleep 0.01; done
`)
	prepare := writeExecutable(t, root, "prepare", `#!/bin/sh
set -eu
grep -q started "`+events+`"
printf "prepared\n" >> "`+events+`"
`)
	writeCandidateEntrypoint(t, candidate, `#!/bin/sh
set -eu
grep -q started "`+events+`"
printf "replayed\n" >> "`+events+`"
`)
	verifier := writeExecutable(t, root, "verify", `#!/bin/sh
set -eu
grep -q stopped "`+events+`"
`)
	configuration := testConfig(root, candidate, verifier)
	configuration.Capture = &config.CaptureConfig{Command: []string{capture, events}}
	configuration.Prepare = &config.CommandConfig{Command: []string{prepare}, Timeout: config.Duration(time.Minute)}
	configuration.Attempts = 1

	result, err := Start(context.Background(), configuration, RunOptions{
		StateDir: filepath.Join(root, "state"),
		Runtime:  &fakeRuntime{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.State.Status != StatusSucceeded {
		t.Fatalf("result = code %d, status %s", result.ExitCode, result.State.Status)
	}
	data, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	want := "started\nprepared\nreplayed\nstopped\n"
	if string(data) != want {
		t.Fatalf("events = %q, want %q", data, want)
	}
	captureLog := filepath.Join(root, "state", "runs", result.ID, "attempts", "0001", "capture.log")
	if info, err := os.Stat(captureLog); err != nil || info.Size() == 0 {
		t.Fatalf("capture log = %#v, %v", info, err)
	}
}

func TestCapturePrematureExitStopsRun(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "candidate")
	mustMkdir(t, candidate)
	capture := writeExecutable(t, root, "capture", "#!/bin/sh\nprintf . >&3\nexec 3>&-\necho capture failed\nexit 7\n")
	prepare := writeExecutable(t, root, "prepare", "#!/bin/sh\nsleep 10\n")
	writeCandidateEntrypoint(t, candidate, "#!/bin/sh\nexit 0\n")
	verifier := writeExecutable(t, root, "verify", "#!/bin/sh\nexit 0\n")
	configuration := testConfig(root, candidate, verifier)
	configuration.Capture = &config.CaptureConfig{Command: []string{capture}}
	configuration.Prepare = &config.CommandConfig{Command: []string{prepare}, Timeout: config.Duration(time.Minute)}
	runtime := &fakeRuntime{}

	started := time.Now()
	result, err := Start(context.Background(), configuration, RunOptions{
		StateDir: filepath.Join(root, "state"),
		Runtime:  runtime,
	})
	if err == nil || !strings.Contains(err.Error(), "capture command exited before replay completed with status 7") {
		t.Fatalf("error = %v", err)
	}
	if result.ExitCode != 2 || runtime.exercises != 0 {
		t.Fatalf("result code = %d, exercises = %d", result.ExitCode, runtime.exercises)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("capture failure took %s to cancel prepare", elapsed)
	}
}

func TestCaptureRequiresReadinessSignal(t *testing.T) {
	previous := captureReadyTimeout
	captureReadyTimeout = 50 * time.Millisecond
	t.Cleanup(func() { captureReadyTimeout = previous })
	root := t.TempDir()
	candidate := filepath.Join(root, "candidate")
	mustMkdir(t, candidate)
	capture := writeExecutable(t, root, "capture", "#!/bin/sh\nsleep 10\n")
	verifier := writeExecutable(t, root, "verify", "#!/bin/sh\nexit 0\n")
	configuration := testConfig(root, candidate, verifier)
	configuration.Capture = &config.CaptureConfig{Command: []string{capture}}
	store := NewRunStore(filepath.Join(root, "state"), "capture-ready")
	if err := store.Create(); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	_, err := StartCapture(context.Background(), configuration, store, 1)
	if err == nil || !strings.Contains(err.Error(), "did not become ready") {
		t.Fatalf("error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("readiness timeout took %s", elapsed)
	}
}

func TestCaptureEnvironmentIsRestricted(t *testing.T) {
	configuration := &config.Config{
		Candidate:  "/candidate",
		Parameters: map[string]string{"serial": "/dev/example"},
		References: map[string]string{"facts": "/facts"},
	}
	environment := strings.Join(captureEnvironment(configuration, "/evidence"), "\n")
	for _, want := range []string{"ALO_EVIDENCE_DIR=/evidence", "ALO_PARAMETER_SERIAL=/dev/example"} {
		if !strings.Contains(environment, want) {
			t.Fatalf("capture environment does not contain %q: %q", want, environment)
		}
	}
	for _, forbidden := range []string{"ALO_CANDIDATE=", "ALO_REFERENCE_", "ALO_RESULT="} {
		if strings.Contains(environment, forbidden) {
			t.Fatalf("capture environment contains %q: %q", forbidden, environment)
		}
	}
}

func TestResumeRewindsInterruptedReplayToPrepare(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "candidate")
	mustMkdir(t, candidate)
	prepareCount := filepath.Join(root, "prepare-count")
	prepare := writeExecutable(t, root, "prepare", `#!/bin/sh
printf prepared\n >> "`+prepareCount+`"
`)
	capture := writeExecutable(t, root, "capture", `#!/bin/sh
printf . >&3
exec 3>&-
while :; do sleep 1; done
`)
	writeCandidateEntrypoint(t, candidate, "#!/bin/sh\nexit 0\n")
	verifier := writeExecutable(t, root, "verify", "#!/bin/sh\nexit 0\n")
	configuration := testConfig(root, candidate, verifier)
	configuration.Capture = &config.CaptureConfig{Command: []string{capture}}
	configuration.Prepare = &config.CommandConfig{Command: []string{prepare}, Timeout: config.Duration(time.Minute)}
	ctx, cancel := context.WithCancel(context.Background())
	firstRuntime := &fakeRuntime{}
	cancelRuntime := &cancellingRuntime{fakeRuntime: firstRuntime, cancel: cancel}
	first, err := Start(ctx, configuration, RunOptions{StateDir: filepath.Join(root, "state"), Runtime: cancelRuntime})
	if err != nil {
		t.Fatal(err)
	}
	if first.ExitCode != 130 || first.State.Phase != PhaseExercise {
		t.Fatalf("first = code %d, phase %s", first.ExitCode, first.State.Phase)
	}
	resumed, err := Resume(context.Background(), first.ID, RunOptions{
		StateDir: filepath.Join(root, "state"), Runtime: &fakeRuntime{},
	})
	if err != nil || resumed.ExitCode != 0 {
		t.Fatalf("resume = %#v, %v", resumed, err)
	}
	data, err := os.ReadFile(prepareCount)
	if err != nil || strings.Count(string(data), "prepared") != 2 {
		t.Fatalf("prepare count = %q, %v", data, err)
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
			result, err := Start(context.Background(), config, RunOptions{
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
	configuration := testConfig(root, candidate, verifier)
	configuration.Agent.Provider = "anthropic"
	configuration.Agent.Model = "claude-sonnet-4-5"
	configuration.Agent.Thinking = "medium"
	configuration.Agent.PassEnv = []string{"ANTHROPIC_API_KEY"}
	configuration.Agent.BootstrapTimeout = config.Duration(45 * time.Minute)
	configuration.Agent.Timeout = config.Duration(5 * time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	firstRuntime := &fakeRuntime{turn: func(ctx context.Context, turn AgentTurn) (AgentResult, error) {
		if err := os.WriteFile(filepath.Join(turn.Config.Candidate, "partial"), []byte("partial"), 0o644); err != nil {
			t.Fatal(err)
		}
		cancel()
		<-ctx.Done()
		return AgentResult{}, ctx.Err()
	}}
	first, err := Start(ctx, configuration, RunOptions{
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
		if turn.Config.Agent.Provider != "anthropic" ||
			turn.Config.Agent.Model != "claude-sonnet-4-5" ||
			turn.Config.Agent.Thinking != "medium" ||
			len(turn.Config.Agent.PassEnv) != 1 || turn.Config.Agent.PassEnv[0] != "ANTHROPIC_API_KEY" ||
			agentTimeout(turn.Config, turn.Attempt) != 45*time.Minute {
			t.Fatalf("resumed agent config = %#v", turn.Config.Agent)
		}
		if _, err := os.Stat(filepath.Join(turn.Config.Candidate, "partial")); err != nil {
			t.Fatalf("partial edit was not retained: %v", err)
		}
		return AgentResult{}, os.WriteFile(filepath.Join(turn.Config.Candidate, "complete"), []byte("complete"), 0o644)
	}}
	resumed, err := Resume(context.Background(), first.ID, RunOptions{
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

func TestAgentTimeoutUsesBootstrapBudgetOnlyForFirstAttempt(t *testing.T) {
	configuration := &config.Config{Agent: config.AgentConfig{
		BootstrapTimeout: config.Duration(30 * time.Minute),
		Timeout:          config.Duration(5 * time.Minute),
	}}
	if got := agentTimeout(configuration, 1); got != 30*time.Minute {
		t.Fatalf("bootstrap timeout = %s", got)
	}
	if got := agentTimeout(configuration, 2); got != 5*time.Minute {
		t.Fatalf("repair timeout = %s", got)
	}
}

func TestAgentBootstrapTimeoutDefaultsToRepairTimeout(t *testing.T) {
	configuration := &config.Config{Agent: config.AgentConfig{Timeout: config.Duration(7 * time.Minute)}}
	configuration.SetDefaults()
	if configuration.Agent.BootstrapTimeout != configuration.Agent.Timeout {
		t.Fatalf("bootstrap = %s, timeout = %s", time.Duration(configuration.Agent.BootstrapTimeout), time.Duration(configuration.Agent.Timeout))
	}
}

func TestAgentTimeoutsAreUnlimitedWhenOmitted(t *testing.T) {
	configuration := &config.Config{}
	configuration.SetDefaults()
	if got := agentTimeout(configuration, 1); got != 0 {
		t.Fatalf("bootstrap timeout = %s", got)
	}
	if got := agentTimeout(configuration, 2); got != 0 {
		t.Fatalf("repair timeout = %s", got)
	}
	ctx, cancel := agentContext(context.Background(), 0)
	defer cancel()
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		t.Fatal("unlimited agent context has a deadline")
	}
}

func TestConfigRejectsUnsafeLayouts(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "candidate")
	mustMkdir(t, candidate)
	verifier := writeExecutable(t, candidate, "verify", "#!/bin/sh\nexit 0\n")
	configuration := testConfig(root, candidate, verifier)
	if err := configuration.Validate(); err == nil || !strings.Contains(err.Error(), "inside candidate") {
		t.Fatalf("unsafe verifier error = %v", err)
	}

	outsideVerifier := writeExecutable(t, root, "safe-verify", "#!/bin/sh\nexit 0\n")
	configuration = testConfig(root, candidate, outsideVerifier)
	configuration.References["nested"] = filepath.Join(candidate, "reference")
	mustMkdir(t, configuration.References["nested"])
	if err := configuration.Validate(); err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("overlap error = %v", err)
	}

	linkedCandidate := filepath.Join(root, "linked-candidate")
	if err := os.Symlink(root, linkedCandidate); err != nil {
		t.Fatal(err)
	}
	configuration = testConfig(root, linkedCandidate, outsideVerifier)
	if err := configuration.Validate(); err == nil || !strings.Contains(err.Error(), "inside candidate") {
		t.Fatalf("symlinked candidate error = %v", err)
	}

	configuration = testConfig(root, candidate, outsideVerifier)
	configuration.Sandbox.Devices = []string{root}
	if err := configuration.Validate(); err == nil || !strings.Contains(err.Error(), "beneath /dev") {
		t.Fatalf("unsafe device error = %v", err)
	}

	capture := writeExecutable(t, candidate, "capture", "#!/bin/sh\nexit 0\n")
	configuration = testConfig(root, candidate, outsideVerifier)
	configuration.Capture = &config.CaptureConfig{Command: []string{capture}}
	if err := configuration.Validate(); err == nil || !strings.Contains(err.Error(), "capture executable is inside candidate") {
		t.Fatalf("unsafe capture error = %v", err)
	}
}

func TestAgentZDRConfiguration(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "candidate")
	mustMkdir(t, candidate)
	verifier := writeExecutable(t, root, "verify", "#!/bin/sh\nexit 0\n")
	config := testConfig(root, candidate, verifier)
	config.Agent.ZDR = true
	if err := config.Validate(); err != nil {
		t.Fatalf("validate OpenRouter ZDR config: %v", err)
	}

	config.Agent.Provider = "anthropic"
	config.Agent.Model = "claude-sonnet-4-5"
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "requires agent.provider to be openrouter") {
		t.Fatalf("non-OpenRouter ZDR error = %v", err)
	}
}

func TestTrustedEnvironmentOmitsAttempt(t *testing.T) {
	configuration := &config.Config{
		Candidate:  "/candidate",
		Parameters: map[string]string{"serial": "/dev/example"},
		References: map[string]string{"facts": "/facts"},
	}
	environment := trustedEnvironment(configuration, nil, "/evidence", "/result")
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "ALO_ATTEMPT=") {
		t.Fatalf("attempt leaked into trusted environment: %q", environment)
	}
	for _, want := range []string{
		"ALO_CANDIDATE=/candidate",
		"ALO_EVIDENCE_DIR=/evidence",
		"ALO_RESULT=/result",
		"ALO_PARAMETER_SERIAL=/dev/example",
		"ALO_REFERENCE_FACTS=/facts",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("environment does not contain %q: %q", want, environment)
		}
	}
}

func testConfig(base, candidate, verifier string) *config.Config {
	config := &config.Config{
		Version:    1,
		Name:       "test-loop",
		Goal:       "Produce the requested artifact.",
		Candidate:  candidate,
		References: make(map[string]string),
		Verify:     config.CommandConfig{Command: []string{verifier}},
		Attempts:   3,
		BaseDir:    base,
	}
	config.SetDefaults()
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
