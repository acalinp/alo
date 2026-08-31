package alo

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestRootlessPodmanAgentBoundary(t *testing.T) {
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
	store := NewRunStore(filepath.Join(root, "state"), "podman-test")
	if err := store.Create(); err != nil {
		t.Fatal(err)
	}
	attemptDir, err := store.EnsureAttempt(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attemptDir, "verify.log"), []byte("failure"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := `set -eu
test -c /dev/null
test "$(cat /refs/facts/fact)" = reference
test "$(cat /evidence/0001/verify.log)" = failure
if touch /refs/facts/forbidden 2>/dev/null; then exit 41; fi
if touch /evidence/forbidden 2>/dev/null; then exit 42; fi
mkdir -p /work/project/out
printf artifact > /work/project/out/result.bin
printf cache > /cache/build-cache
printf session > /session/agent-session
test -f "$1"`
	config := &Config{
		Version:    1,
		Name:       "podman-boundary",
		Goal:       "Build an artifact.",
		Candidates: map[string]string{"project": candidate},
		References: map[string]string{"facts": reference},
		Agent:      AgentConfig{Devices: []string{"/dev/null"}},
		Verify:     VerifyConfig{Command: []string{"true"}},
		Attempts:   2,
		BaseDir:    root,
	}
	config.setDefaults()
	agent := NewPodmanAgent(io.Discard)
	agent.imageOverride = image
	agent.commandOverride = []string{"sh", "-c", script, "alo-test"}
	imageID, err := agent.ResolveImage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = agent.Turn(context.Background(), AgentTurn{
		Config:      config,
		Store:       store,
		Attempt:     1,
		ImageID:     imageID,
		LogPath:     filepath.Join(attemptDir, "agent.log"),
		RequestPath: filepath.Join(attemptDir, "request.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(candidate, "out", "result.bin"),
		filepath.Join(store.CacheDir, "build-cache"),
		filepath.Join(store.SessionDir, "agent-session"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("persistent file %s: %v", path, err)
		}
	}
}
