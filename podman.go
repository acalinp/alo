package alo

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
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
		exists, err := p.objectExists(ctx, "image", image)
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
	console := p.console()
	fmt.Fprintf(console, "building managed agent toolbox %s\n", image)
	command := exec.CommandContext(ctx, p.binary(), "build", "--pull=missing", "--tag", image, directory)
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

func (p *PodmanAgent) Exercise(ctx context.Context, turn ExerciseTurn) (ExerciseResult, error) {
	if turn.Config == nil || turn.Store == nil {
		return ExerciseResult{}, errors.New("Podman exercise turn is incomplete")
	}
	logFile, err := os.OpenFile(turn.LogPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return ExerciseResult{}, fmt.Errorf("create exercise log: %w", err)
	}
	defer logFile.Close()
	target := io.MultiWriter(logFile, writerOrDiscard(turn.Console))

	cidPath := filepath.Join(filepath.Dir(turn.LogPath), "exercise.cid")
	name := exerciseContainerName(turn.Store, turn.Attempt)
	if err := p.removeStaleContainer(ctx, cidPath, name); err != nil {
		return ExerciseResult{}, err
	}
	args, err := p.exerciseArguments(turn, cidPath, name)
	if err != nil {
		return ExerciseResult{}, err
	}
	exitCode, runErr := p.runEphemeral(ctx, args, cidPath, name, target)
	return ExerciseResult{ExitCode: exitCode}, runErr
}

func (p *PodmanAgent) exerciseArguments(turn ExerciseTurn, cidPath, name string) ([]string, error) {
	args := []string{
		"run", "--rm", "--pull=never", "--http-proxy=false",
		"--name", name,
		"--cidfile", cidPath,
		"--userns=keep-id",
	}
	deviceArgs, err := p.deviceArguments(turn.Config.Sandbox.Devices)
	if err != nil {
		return nil, err
	}
	args = append(args, deviceArgs...)
	args = append(args,
		"--mount", podmanMount(turn.Config.Candidate, "/work/candidate", false),
		"--workdir", "/work/candidate",
	)
	args = append(args, referenceMounts(turn.Config.References)...)
	args = append(args, exerciseEnvironment(turn.Config, turn.Attempt)...)
	args = append(args, turn.ImageID, "/work/candidate/.alo/run")
	return args, nil
}

func exerciseEnvironment(config *Config, attempt int) []string {
	args := []string{
		"--env", fmt.Sprintf("ALO_ATTEMPT=%d", attempt),
		"--env", "ALO_CANDIDATE=/work/candidate",
	}
	for _, name := range sortedKeys(config.Parameters) {
		args = append(args, "--env", "ALO_PARAMETER_"+environmentName(name)+"="+config.Parameters[name])
	}
	for _, name := range sortedKeys(config.References) {
		args = append(args, "--env", "ALO_REFERENCE_"+environmentName(name)+"=/refs/"+name)
	}
	return args
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
	name := workshopContainerName(turn.Store)
	if err := p.ensureWorkshop(ctx, turn, name); err != nil {
		return AgentResult{}, err
	}
	request := agentRequest(turn.Config, turn.Store, turn.Attempt)
	if err := writeAgentRequest(turn.RequestPath, request); err != nil {
		return AgentResult{}, fmt.Errorf("write retained agent request: %w", err)
	}
	stableRequest := filepath.Join(turn.Store.SessionDir, "request.json")
	if err := writeWorkshopRequest(stableRequest, request); err != nil {
		return AgentResult{}, fmt.Errorf("write workshop request: %w", err)
	}
	environmentPath := filepath.Join(turn.Store.SessionDir, "agent-env.json")
	if err := writeAgentEnvironment(environmentPath, turn.Config.Agent.PassEnv); err != nil {
		return AgentResult{}, err
	}
	defer os.Remove(environmentPath)
	logFile, err := os.OpenFile(turn.LogPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return AgentResult{}, fmt.Errorf("create agent log: %w", err)
	}
	defer logFile.Close()
	console := turn.Console
	if console == nil {
		console = p.console()
	}
	deadline, _ := ctx.Deadline()
	progress := newTerminalProgress(console, turn.Store.ID, deadline)
	tail := &tailBuffer{maximum: 64 << 10}
	target := io.MultiWriter(logFile, progress, tail)
	var secrets []string
	for _, variable := range turn.Config.Agent.PassEnv {
		secrets = append(secrets, os.Getenv(variable))
	}
	redacted := newSecretRedactor(target, secrets)
	stdoutStream := newAgentStreamWriter(redacted, progress)
	stderrStream := newAgentStreamWriter(redacted, progress)

	command := exec.Command(p.binary(), "start", "--attach", name)
	command.Env = os.Environ()
	command.Stdout = stdoutStream
	command.Stderr = stderrStream
	if err := command.Start(); err != nil {
		return AgentResult{}, fmt.Errorf("start agent workshop: %w", err)
	}
	progress.Start()
	defer progress.Stop()
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	var waitErr error
	select {
	case waitErr = <-waited:
	case <-ctx.Done():
		cleanupContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		cleanupErr := p.stopContainer(cleanupContext, name)
		cancel()
		select {
		case waitErr = <-waited:
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			waitErr = <-waited
		}
		_ = flushAgentOutput(redacted, stdoutStream, stderrStream)
		return AgentResult{}, errors.Join(ctx.Err(), cleanupErr, normalizeExitError(waitErr))
	}
	flushErr := flushAgentOutput(redacted, stdoutStream, stderrStream)
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
	return AgentResult{}, errors.Join(fmt.Errorf("agent workshop failed: %w", waitErr), flushErr)
}

func (p *PodmanAgent) ensureWorkshop(ctx context.Context, turn AgentTurn, name string) error {
	exists, err := p.objectExists(ctx, "container", name)
	if err != nil {
		return err
	}
	if exists {
		image, err := p.output(ctx, "container", "inspect", "--format", "{{.Image}}", name)
		if err != nil {
			return err
		}
		if normalizeImageID(image) != normalizeImageID(turn.ImageID) {
			return fmt.Errorf("agent workshop %q uses a different image", name)
		}
		return p.stopContainer(ctx, name)
	}
	args, err := p.workshopCreateArguments(turn, name)
	if err != nil {
		return err
	}
	if output, err := p.output(ctx, args...); err != nil {
		return fmt.Errorf("create agent workshop: %w", err)
	} else if strings.TrimSpace(output) == "" {
		return errors.New("Podman returned an empty workshop container ID")
	}
	return nil
}

func (p *PodmanAgent) workshopCreateArguments(turn AgentTurn, name string) ([]string, error) {
	args := []string{
		"create", "--pull=never", "--http-proxy=false",
		"--name", name,
		"--userns=keep-id",
	}
	deviceArgs, err := p.deviceArguments(turn.Config.Sandbox.Devices)
	if err != nil {
		return nil, err
	}
	args = append(args, deviceArgs...)
	args = append(args,
		"--mount", podmanMount(turn.Config.Candidate, "/work/candidate", false),
		"--mount", podmanMount(turn.Store.AttemptsDir, "/evidence", true),
		"--mount", podmanMount(turn.Store.CacheDir, "/cache", false),
		"--mount", podmanMount(turn.Store.SessionDir, "/session", false),
		"--workdir", "/work/candidate",
	)
	args = append(args, referenceMounts(turn.Config.References)...)
	args = append(args, turn.ImageID)
	command := p.commandOverride
	if len(command) == 0 {
		command = []string{"/usr/local/bin/alo-agent"}
	}
	args = append(args, command...)
	args = append(args, "/session/request.json", "/session/agent-env.json")
	return args, nil
}

func writeAgentEnvironment(path string, names []string) error {
	values := make(map[string]string, len(names))
	for _, name := range names {
		values[name] = os.Getenv(name)
	}
	data, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("encode agent environment: %w", err)
	}
	data = append(data, '\n')
	if err := replaceUntrustedFile(path, data); err != nil {
		return fmt.Errorf("write agent environment: %w", err)
	}
	return nil
}

// The workshop controls its session directory. Stop it before calling this,
// remove any symlink it may have left, then create the file exclusively.
func replaceUntrustedFile(path string, data []byte) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	return errors.Join(writeErr, closeErr)
}

func (p *PodmanAgent) RemoveWorkshop(ctx context.Context, store *RunStore) error {
	if store == nil {
		return nil
	}
	return p.removeNamedContainer(ctx, workshopContainerName(store))
}

func (p *PodmanAgent) runEphemeral(
	ctx context.Context,
	args []string,
	cidPath, name string,
	output io.Writer,
) (int, error) {
	command := exec.Command(p.binary(), args...)
	command.Env = os.Environ()
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		return -1, fmt.Errorf("start rootless Podman container: %w", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	var waitErr error
	select {
	case waitErr = <-waited:
	case <-ctx.Done():
		published, processDone, cidErr := waitForContainerID(cidPath, waited, &waitErr)
		cleanupErr := cidErr
		if published {
			cleanupContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			cleanupErr = errors.Join(cleanupErr, p.removeContainer(cleanupContext, cidPath))
			cancel()
		} else if !processDone {
			_ = command.Process.Kill()
			waitErr = <-waited
			processDone = true
		}
		if !processDone {
			select {
			case waitErr = <-waited:
			case <-time.After(5 * time.Second):
				_ = command.Process.Kill()
				waitErr = <-waited
			}
		}
		cleanupContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		cleanupErr = errors.Join(cleanupErr, p.removeNamedContainer(cleanupContext, name))
		cancel()
		_ = os.Remove(cidPath)
		return exitStatus(waitErr), errors.Join(ctx.Err(), cleanupErr, normalizeExitError(waitErr))
	}
	_ = os.Remove(cidPath)
	var exitError *exec.ExitError
	if waitErr == nil {
		return 0, nil
	}
	if errors.As(waitErr, &exitError) {
		return exitError.ExitCode(), nil
	}
	return -1, waitErr
}

func (p *PodmanAgent) removeStaleContainer(ctx context.Context, cidPath, name string) error {
	cleanupErr := errors.Join(p.removeContainer(ctx, cidPath), p.removeNamedContainer(ctx, name))
	if cleanupErr != nil {
		return fmt.Errorf("remove stale container: %w", cleanupErr)
	}
	if err := os.Remove(cidPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale container ID: %w", err)
	}
	return nil
}

func (p *PodmanAgent) deviceArguments(devices []string) ([]string, error) {
	var args []string
	if len(devices) > 0 {
		args = append(args, "--group-add=keep-groups")
	}
	for _, path := range devices {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, fmt.Errorf("resolve device %q: %w", path, err)
		}
		if !pathWithin(resolved, "/dev") {
			return nil, fmt.Errorf("device %q resolves outside /dev", path)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return nil, fmt.Errorf("inspect device %q: %w", path, err)
		}
		switch {
		case info.IsDir():
			args = append(args, "--mount", podmanMount(resolved, path, false))
		case info.Mode()&os.ModeCharDevice != 0:
			args = append(args, "--device", resolved+":"+path+":rw")
		default:
			return nil, fmt.Errorf("device %q is neither a character device nor a directory", path)
		}
	}
	return args, nil
}

func referenceMounts(references map[string]string) []string {
	var args []string
	for _, name := range sortedKeys(references) {
		args = append(args, "--mount", podmanMount(references[name], "/refs/"+name, true))
	}
	return args
}

func podmanMount(source, target string, readOnly bool) string {
	mode := "rw"
	if readOnly {
		mode = "ro"
	}
	return "type=bind,src=" + source + ",target=" + target + "," + mode
}

func agentRequest(config *Config, store *RunStore, attempt int) AgentRequest {
	references := make(map[string]string, len(config.References))
	for name := range config.References {
		references[name] = "/refs/" + name
	}
	return AgentRequest{
		Goal:       config.Goal,
		Attempt:    attempt,
		Provider:   config.Agent.Provider,
		Model:      config.Agent.Model,
		Thinking:   config.Agent.Thinking,
		ZDR:        config.Agent.ZDR,
		Parameters: config.Parameters,
		Candidate:  "/work/candidate",
		References: references,
		Devices:    append([]string(nil), config.Sandbox.Devices...),
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

func workshopContainerName(store *RunStore) string {
	return "alo-" + store.ID + "-workshop"
}

func exerciseContainerName(store *RunStore, attempt int) string {
	return fmt.Sprintf("alo-%s-%04d-exercise", store.ID, attempt)
}

func (p *PodmanAgent) objectExists(ctx context.Context, kind, name string) (bool, error) {
	command := exec.CommandContext(ctx, p.binary(), kind, "exists", name)
	err := command.Run()
	if err == nil {
		return true, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("inspect Podman %s %q: %w", kind, name, err)
}

func (p *PodmanAgent) stopContainer(ctx context.Context, name string) error {
	exists, err := p.objectExists(ctx, "container", name)
	if err != nil || !exists {
		return err
	}
	running, err := p.output(ctx, "container", "inspect", "--format", "{{.State.Running}}", name)
	if err != nil {
		return err
	}
	if strings.TrimSpace(running) != "true" {
		return nil
	}
	if _, err := p.output(ctx, "stop", "--time", "2", name); err != nil {
		return fmt.Errorf("stop container %q: %w", name, err)
	}
	return nil
}

func (p *PodmanAgent) removeContainer(ctx context.Context, cidPath string) error {
	if _, err := os.Stat(cidPath); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect container ID: %w", err)
	}
	command := exec.CommandContext(ctx, p.binary(), "rm", "--force", "--ignore", "--cidfile", cidPath)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		return fmt.Errorf("remove container: %w: %s", err, strings.TrimSpace(output.String()))
	}
	return nil
}

func (p *PodmanAgent) removeNamedContainer(ctx context.Context, name string) error {
	command := exec.CommandContext(ctx, p.binary(), "rm", "--force", "--ignore", name)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		return fmt.Errorf("remove container %q: %w: %s", name, err, strings.TrimSpace(output.String()))
	}
	return nil
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
			return false, false, errors.New("container did not publish its container ID")
		case <-ticker.C:
		}
	}
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

func exitStatus(err error) int {
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	return -1
}

func normalizeImageID(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "sha256:")
}

func writerOrDiscard(writer io.Writer) io.Writer {
	if writer == nil {
		return io.Discard
	}
	return writer
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

func (p *PodmanAgent) console() io.Writer {
	if p.Console == nil {
		return io.Discard
	}
	return p.Console
}

func (p *PodmanAgent) binary() string {
	if p.Binary == "" {
		return "podman"
	}
	return p.Binary
}
