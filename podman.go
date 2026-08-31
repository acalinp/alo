package alo

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type PodmanAgent struct {
	Binary  string
	Console io.Writer

	imageOverride   string
	commandOverride []string
}

// These files define Alo's implementation-owned agent toolbox. Their content
// hash names the local image, so source changes produce a new image tag.
//
//go:embed internal/agent/Containerfile
var managedContainerfile []byte

//go:embed internal/agent/run-agent
var managedAgentRunner []byte

//go:embed internal/agent/instructions.md
var managedAgentInstructions []byte

func NewPodmanAgent(console io.Writer) *PodmanAgent {
	return &PodmanAgent{Binary: "podman", Console: console}
}

func (p *PodmanAgent) ResolveImage(ctx context.Context) (string, error) {
	if err := p.preflight(ctx); err != nil {
		return "", err
	}
	image := p.imageOverride
	if image == "" {
		image = managedAgentImage()
		exists, err := p.imageExists(ctx, image)
		if err != nil {
			return "", err
		}
		if !exists {
			if err := p.buildManagedImage(ctx, image); err != nil {
				return "", err
			}
		}
	}
	output, err := p.output(ctx, "image", "inspect", "--format", "{{.Id}}", image)
	if err != nil {
		return "", fmt.Errorf("resolve agent image %q: %w", image, err)
	}
	resolved := strings.TrimSpace(output)
	if resolved == "" {
		return "", fmt.Errorf("podman returned an empty ID for image %q", image)
	}
	return resolved, nil
}

func managedAgentImage() string {
	hash := sha256.New()
	_, _ = hash.Write(managedContainerfile)
	_, _ = hash.Write(managedAgentRunner)
	_, _ = hash.Write(managedAgentInstructions)
	digest := hash.Sum(nil)
	return "localhost/alo-agent:" + hex.EncodeToString(digest[:12])
}

func (p *PodmanAgent) imageExists(ctx context.Context, image string) (bool, error) {
	command := exec.CommandContext(ctx, p.binary(), "image", "exists", image)
	err := command.Run()
	if err == nil {
		return true, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("inspect managed agent image: %w", err)
}

func (p *PodmanAgent) buildManagedImage(ctx context.Context, image string) error {
	directory, err := os.MkdirTemp("", "alo-agent-")
	if err != nil {
		return fmt.Errorf("create managed agent build context: %w", err)
	}
	defer os.RemoveAll(directory)
	for name, asset := range map[string][]byte{
		"Containerfile":   managedContainerfile,
		"run-agent":       managedAgentRunner,
		"instructions.md": managedAgentInstructions,
	} {
		mode := os.FileMode(0o644)
		if name == "run-agent" {
			mode = 0o755
		}
		if err := os.WriteFile(filepath.Join(directory, name), asset, mode); err != nil {
			return fmt.Errorf("write managed agent asset %q: %w", name, err)
		}
	}
	console := p.Console
	if console == nil {
		console = io.Discard
	}
	fmt.Fprintf(console, "building managed agent toolbox %s\n", image)
	command := exec.CommandContext(
		ctx,
		p.binary(),
		"build", "--pull=missing", "--tag", image, directory,
	)
	command.Env = os.Environ()
	command.Stdout = console
	command.Stderr = console
	if err := command.Run(); err != nil {
		return fmt.Errorf("build managed agent toolbox: %w", err)
	}
	return nil
}

func (p *PodmanAgent) preflight(ctx context.Context) error {
	version, err := p.output(ctx, "version", "--format", "{{.Client.Version}}")
	if err != nil {
		return fmt.Errorf("run podman version: %w", err)
	}
	major, minor, err := parsePodmanVersion(strings.TrimSpace(version))
	if err != nil {
		return err
	}
	if major < 4 || (major == 4 && minor < 9) {
		return fmt.Errorf("Podman 4.9 or newer is required, found %s", strings.TrimSpace(version))
	}
	rootless, err := p.output(ctx, "info", "--format", "{{.Host.Security.Rootless}}")
	if err != nil {
		return fmt.Errorf("inspect Podman rootless mode: %w", err)
	}
	if strings.TrimSpace(rootless) != "true" {
		return errors.New("Alo requires rootless Podman")
	}
	return nil
}

func parsePodmanVersion(value string) (int, int, error) {
	parts := strings.SplitN(value, ".", 3)
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("cannot parse Podman version %q", value)
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("cannot parse Podman version %q", value)
	}
	minorText := parts[1]
	for index, character := range minorText {
		if character < '0' || character > '9' {
			minorText = minorText[:index]
			break
		}
	}
	minor, err := strconv.Atoi(minorText)
	if err != nil {
		return 0, 0, fmt.Errorf("cannot parse Podman version %q", value)
	}
	return major, minor, nil
}

func (p *PodmanAgent) Turn(ctx context.Context, turn AgentTurn) (AgentResult, error) {
	if turn.Config == nil || turn.Store == nil {
		return AgentResult{}, errors.New("Podman agent turn is incomplete")
	}
	for _, name := range turn.Config.Agent.PassEnv {
		if _, exists := os.LookupEnv(name); !exists {
			return AgentResult{}, fmt.Errorf("agent environment variable %s is not set", name)
		}
	}
	request := agentRequest(turn.Config, turn.Store, turn.Attempt)
	if err := writeAgentRequest(turn.RequestPath, request); err != nil {
		return AgentResult{}, fmt.Errorf("write agent request: %w", err)
	}

	logFile, err := os.OpenFile(turn.LogPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return AgentResult{}, fmt.Errorf("create agent log: %w", err)
	}
	defer logFile.Close()

	console := turn.Console
	if console == nil {
		console = p.Console
	}
	if console == nil {
		console = io.Discard
	}
	tail := &tailBuffer{maximum: 64 << 10}
	target := io.MultiWriter(logFile, console, tail)
	var secrets []string
	for _, name := range turn.Config.Agent.PassEnv {
		secrets = append(secrets, os.Getenv(name))
	}
	redacted := newSecretRedactor(target, secrets)

	cidPath := filepath.Join(filepath.Dir(turn.LogPath), "agent.cid")
	containerName := agentContainerName(turn)
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
	cleanupErr := errors.Join(
		p.removeContainer(cleanupCtx, cidPath),
		p.removeNamedContainer(cleanupCtx, containerName),
	)
	cleanupCancel()
	if cleanupErr != nil {
		return AgentResult{}, fmt.Errorf("remove stale agent container: %w", cleanupErr)
	}
	if err := os.Remove(cidPath); err != nil && !os.IsNotExist(err) {
		return AgentResult{}, fmt.Errorf("remove stale container ID: %w", err)
	}
	args, err := p.runArguments(turn, cidPath, containerName)
	if err != nil {
		return AgentResult{}, err
	}
	cmd := exec.Command(p.binary(), args...)
	cmd.Env = os.Environ()
	cmd.Stdout = redacted
	cmd.Stderr = redacted
	if err := cmd.Start(); err != nil {
		return AgentResult{}, fmt.Errorf("start rootless Podman agent: %w", err)
	}

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-waited:
	case <-ctx.Done():
		published, processDone, cidErr := waitForContainerID(cidPath, waited, &waitErr)
		cleanupErr := cidErr
		if published {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			cleanupErr = errors.Join(cleanupErr, p.removeContainer(cleanupCtx, cidPath))
			cancel()
		} else if !processDone {
			_ = cmd.Process.Kill()
			waitErr = <-waited
			processDone = true
		}
		if !processDone {
			select {
			case waitErr = <-waited:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				waitErr = <-waited
			}
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		cleanupErr = errors.Join(cleanupErr, p.removeNamedContainer(cleanupCtx, containerName))
		cancel()
		_ = redacted.Flush()
		_ = os.Remove(cidPath)
		return AgentResult{}, errors.Join(ctx.Err(), cleanupErr, normalizeExitError(waitErr))
	}
	flushErr := redacted.Flush()
	_ = os.Remove(cidPath)
	if waitErr == nil && flushErr == nil {
		if reason := parseBlockedReason(tail.String()); reason != "" {
			return AgentResult{BlockedReason: reason}, nil
		}
		return AgentResult{}, nil
	}
	var exitError *exec.ExitError
	if errors.As(waitErr, &exitError) && exitError.ExitCode() == 3 {
		reason := parseBlockedReason(tail.String())
		if reason == "" {
			reason = "agent reported a blocker"
		}
		return AgentResult{BlockedReason: reason}, flushErr
	}
	return AgentResult{}, errors.Join(
		fmt.Errorf("agent container failed: %w", waitErr),
		flushErr,
	)
}

func (p *PodmanAgent) runArguments(turn AgentTurn, cidPath, containerName string) ([]string, error) {
	args := []string{
		"run", "--rm", "--pull=never",
		"--http-proxy=false",
		"--name", containerName,
		"--cidfile", cidPath,
		"--userns=keep-id",
	}
	if len(turn.Config.Agent.Devices) > 0 {
		args = append(args, "--group-add=keep-groups")
	}
	for _, path := range turn.Config.Agent.Devices {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, fmt.Errorf("resolve agent device %q: %w", path, err)
		}
		if !pathWithin(resolved, "/dev") {
			return nil, fmt.Errorf("agent device %q resolves outside /dev", path)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return nil, fmt.Errorf("inspect agent device %q: %w", path, err)
		}
		switch {
		case info.IsDir():
			args = append(args, "--mount", podmanMount(resolved, path, false))
		case info.Mode()&os.ModeCharDevice != 0:
			args = append(args, "--device", resolved+":"+path+":rw")
		default:
			return nil, fmt.Errorf("agent device %q is neither a character device nor a directory", path)
		}
	}
	for _, name := range sortedKeys(turn.Config.Candidates) {
		args = append(args, "--mount", podmanMount(
			turn.Config.Candidates[name], "/work/"+name, false,
		))
	}
	for _, name := range sortedKeys(turn.Config.References) {
		args = append(args, "--mount", podmanMount(
			turn.Config.References[name], "/refs/"+name, true,
		))
	}
	args = append(args,
		"--mount", podmanMount(turn.Store.AttemptsDir, "/evidence", true),
		"--mount", podmanMount(turn.Store.CacheDir, "/cache", false),
		"--mount", podmanMount(turn.Store.SessionDir, "/session", false),
		"--workdir", "/work/"+sortedKeys(turn.Config.Candidates)[0],
	)
	requestPath := "/evidence/" + filepath.Base(turn.Store.AttemptDir(turn.Attempt)) + "/" + filepath.Base(turn.RequestPath)
	args = append(args, "--env", "ALO_REQUEST="+requestPath)
	for _, name := range turn.Config.Agent.PassEnv {
		args = append(args, "--env", name)
	}
	args = append(args, turn.ImageID)
	command := p.commandOverride
	if len(command) == 0 {
		command = []string{"/usr/local/bin/alo-agent"}
	}
	args = append(args, command...)
	args = append(args, requestPath)
	return args, nil
}

func agentContainerName(turn AgentTurn) string {
	return fmt.Sprintf("alo-%s-%04d", turn.Store.ID, turn.Attempt)
}

func waitForContainerID(cidPath string, waited <-chan error, waitErr *error) (bool, bool, error) {
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(cidPath); err == nil {
			return true, false, nil
		} else if !os.IsNotExist(err) {
			return false, false, fmt.Errorf("inspect cancelled container ID: %w", err)
		}
		select {
		case *waitErr = <-waited:
			return false, true, nil
		case <-timer.C:
			return false, false, errors.New("agent container did not publish its container ID")
		case <-ticker.C:
		}
	}
}

func podmanMount(source, target string, readOnly bool) string {
	mode := "rw"
	if readOnly {
		mode = "ro"
	}
	return "type=bind,src=" + source + ",target=" + target + "," + mode
}

func agentRequest(config *Config, store *RunStore, attempt int) AgentRequest {
	candidates := make(map[string]string, len(config.Candidates))
	for name := range config.Candidates {
		candidates[name] = "/work/" + name
	}
	references := make(map[string]string, len(config.References))
	for name := range config.References {
		references[name] = "/refs/" + name
	}
	return AgentRequest{
		Goal:       config.Goal,
		Attempt:    attempt,
		Parameters: config.Parameters,
		Candidates: candidates,
		References: references,
		Devices:    append([]string(nil), config.Agent.Devices...),
		Evidence:   "/evidence/" + filepath.Base(store.AttemptDir(attempt)),
		Cache:      "/cache",
		Session:    "/session",
	}
}

func parseBlockedReason(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(lines[index])
		if strings.HasPrefix(line, agentBlockedPrefix) {
			reason := strings.TrimSpace(strings.TrimPrefix(line, agentBlockedPrefix))
			if reason != "" {
				return reason
			}
		}
	}
	return ""
}

func (p *PodmanAgent) removeContainer(ctx context.Context, cidPath string) error {
	if _, err := os.Stat(cidPath); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect cancelled container ID: %w", err)
	}
	command := exec.CommandContext(
		ctx,
		p.binary(),
		"rm", "--force", "--ignore", "--cidfile", cidPath,
	)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		return fmt.Errorf("remove cancelled agent container: %w: %s", err, strings.TrimSpace(output.String()))
	}
	return nil
}

func (p *PodmanAgent) removeNamedContainer(ctx context.Context, name string) error {
	command := exec.CommandContext(ctx, p.binary(), "rm", "--force", "--ignore", name)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		return fmt.Errorf("remove agent container %q: %w: %s", name, err, strings.TrimSpace(output.String()))
	}
	return nil
}

func normalizeExitError(err error) error {
	if err == nil {
		return nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return nil
	}
	return err
}

func (p *PodmanAgent) output(ctx context.Context, args ...string) (string, error) {
	command := exec.CommandContext(ctx, p.binary(), args...)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("podman %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(output.String()))
	}
	return output.String(), nil
}

func (p *PodmanAgent) binary() string {
	if p.Binary == "" {
		return "podman"
	}
	return p.Binary
}
