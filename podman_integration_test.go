package alo

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestRootlessPodmanReplayAndPersistentWorkshop(t *testing.T) {
	image := os.Getenv("ALO_PODMAN_TEST_IMAGE")
	if image == "" {
		t.Skip("set ALO_PODMAN_TEST_IMAGE to a local image containing sh")
	}
	t.Setenv("OPENROUTER_API_KEY", "podman-boundary-test")
	root := t.TempDir()
	candidate := filepath.Join(root, "candidate")
	reference := filepath.Join(root, "reference")
	mustMkdir(t, candidate)
	mustMkdir(t, reference)
	if err := os.WriteFile(filepath.Join(reference, "fact"), []byte("reference"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeCandidateEntrypoint(t, candidate, `#!/bin/sh
set -eu
test -c /dev/null
test "$(cat /refs/facts/fact)" = reference
if touch /refs/facts/forbidden 2>/dev/null; then exit 41; fi
mkdir -p out
printf replayed > out/replayed
`)
	store := NewRunStore(filepath.Join(root, "state"), "podman-test")
	if err := store.Create(); err != nil {
		t.Fatal(err)
	}
	attemptOne, err := store.EnsureAttempt(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attemptOne, "verify.log"), []byte("failure"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := &Config{
		Version:    1,
		Name:       "podman-boundary",
		Goal:       "Build an artifact.",
		Candidate:  candidate,
		References: map[string]string{"facts": reference},
		Sandbox:    SandboxConfig{Devices: []string{"/dev/null"}},
		Verify:     CommandConfig{Command: []string{"true"}},
		Attempts:   3,
		BaseDir:    root,
	}
	config.setDefaults()
	workshopScript := `set -eu
test -c /dev/null
test "$(cat /refs/facts/fact)" = reference
test "$(cat /evidence/0001/verify.log)" = failure
if touch /refs/facts/forbidden 2>/dev/null; then exit 42; fi
if touch /evidence/forbidden 2>/dev/null; then exit 43; fi
test -f "$1"
test -f "$2"
if [ ! -f /tmp/workshop-root ]; then
    touch /tmp/workshop-root
    printf first > /work/candidate/out/first-turn
    printf cache > /cache/build-cache
    printf session > /session/agent-session
else
    printf second > /work/candidate/out/second-turn
fi`
	runtime := NewPodmanAgent(io.Discard)
	runtime.imageOverride = image
	runtime.commandOverride = []string{"sh", "-c", workshopScript, "alo-test"}
	imageID, err := runtime.ResolveImage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Exercise(context.Background(), ExerciseTurn{
		Config: config, Store: store, Attempt: 1, ImageID: imageID,
		LogPath: filepath.Join(attemptOne, "exercise.log"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(candidate, "out", "replayed")); err != nil {
		t.Fatalf("fresh replay artifact: %v", err)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		attemptDir, err := store.EnsureAttempt(attempt)
		if err != nil {
			t.Fatal(err)
		}
		_, err = runtime.Turn(context.Background(), AgentTurn{
			Config: config, Store: store, Attempt: attempt, ImageID: imageID,
			LogPath:     filepath.Join(attemptDir, "agent.log"),
			RequestPath: filepath.Join(attemptDir, "request.json"),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{
		filepath.Join(candidate, "out", "first-turn"),
		filepath.Join(candidate, "out", "second-turn"),
		filepath.Join(store.CacheDir, "build-cache"),
		filepath.Join(store.SessionDir, "agent-session"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("persistent file %s: %v", path, err)
		}
	}
	if err := runtime.RemoveWorkshop(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	exists, err := runtime.objectExists(context.Background(), "container", workshopContainerName(store))
	if err != nil || exists {
		t.Fatalf("workshop still exists: exists=%v err=%v", exists, err)
	}
}
