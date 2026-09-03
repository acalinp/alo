package alo

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultAttempts        = 10
	defaultAgentTimeout    = 15 * time.Minute
	defaultExerciseTimeout = 30 * time.Minute
	defaultCommandTimeout  = 30 * time.Minute
	defaultAgentProvider   = "openrouter"
	defaultAgentModel      = "openai/gpt-5.6-sol"
	defaultAgentThinking   = "high"
)

var (
	resourceNamePattern    = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	parameterNamePattern   = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	agentValuePattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@:+-]*$`)
)

type Config struct {
	Version    int               `yaml:"version"`
	Name       string            `yaml:"name"`
	Goal       string            `yaml:"goal"`
	Parameters map[string]string `yaml:"parameters,omitempty"`
	Candidate  string            `yaml:"candidate"`
	References map[string]string `yaml:"references,omitempty"`
	Sandbox    SandboxConfig     `yaml:"sandbox,omitempty"`
	Agent      AgentConfig       `yaml:"agent,omitempty"`
	Prepare    *CommandConfig    `yaml:"prepare,omitempty"`
	Exercise   ExerciseConfig    `yaml:"exercise,omitempty"`
	Verify     CommandConfig     `yaml:"verify"`
	Attempts   int               `yaml:"attempts,omitempty"`

	Path    string `yaml:"-"`
	BaseDir string `yaml:"-"`
}

type AgentConfig struct {
	Provider string   `yaml:"provider,omitempty"`
	Model    string   `yaml:"model,omitempty"`
	Thinking string   `yaml:"thinking,omitempty"`
	ZDR      bool     `yaml:"zdr,omitempty"`
	PassEnv  []string `yaml:"pass_env,omitempty"`
	Timeout  Duration `yaml:"timeout,omitempty"`
}

type SandboxConfig struct {
	Devices []string `yaml:"devices,omitempty"`
}

type ExerciseConfig struct {
	Timeout Duration `yaml:"timeout,omitempty"`
}

type CommandConfig struct {
	Command []string `yaml:"command"`
	PassEnv []string `yaml:"pass_env,omitempty"`
	Timeout Duration `yaml:"timeout,omitempty"`
}

type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return errors.New("duration must be a string")
	}
	value, err := time.ParseDuration(node.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", node.Value, err)
	}
	if value <= 0 {
		return errors.New("duration must be positive")
	}
	*d = Duration(value)
	return nil
}

func (d Duration) MarshalYAML() (any, error) {
	return time.Duration(d).String(), nil
}

func LoadConfig(path string) (*Config, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve configuration path: %w", err)
	}
	file, err := os.Open(absolute)
	if err != nil {
		return nil, fmt.Errorf("open configuration %q: %w", absolute, err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("decode configuration %q: %w", absolute, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("configuration contains multiple YAML documents")
		}
		return nil, fmt.Errorf("decode trailing YAML: %w", err)
	}

	config.Path = absolute
	config.BaseDir = filepath.Dir(absolute)
	config.setDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &config, nil
}

func (c *Config) setDefaults() {
	if c.Parameters == nil {
		c.Parameters = make(map[string]string)
	}
	if c.References == nil {
		c.References = make(map[string]string)
	}
	if c.Attempts == 0 {
		c.Attempts = defaultAttempts
	}
	if c.Agent.Timeout == 0 {
		c.Agent.Timeout = Duration(defaultAgentTimeout)
	}
	if c.Agent.Provider == "" && c.Agent.Model == "" {
		c.Agent.Provider = defaultAgentProvider
		c.Agent.Model = defaultAgentModel
	}
	if c.Agent.Thinking == "" {
		c.Agent.Thinking = defaultAgentThinking
	}
	if c.Exercise.Timeout == 0 {
		c.Exercise.Timeout = Duration(defaultExerciseTimeout)
	}
	if c.Verify.Timeout == 0 {
		c.Verify.Timeout = Duration(defaultCommandTimeout)
	}
	if c.Prepare != nil && c.Prepare.Timeout == 0 {
		c.Prepare.Timeout = Duration(defaultCommandTimeout)
	}
}

func (c *Config) resolvePaths() error {
	if c.BaseDir == "" {
		current, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve configuration directory: %w", err)
		}
		c.BaseDir = current
	}
	base, err := filepath.Abs(c.BaseDir)
	if err != nil {
		return fmt.Errorf("resolve configuration directory: %w", err)
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		return fmt.Errorf("resolve configuration directory: %w", err)
	}
	c.BaseDir = base

	if c.Candidate != "" {
		resolved, err := resolveResource(c.BaseDir, c.Candidate)
		if err != nil {
			return fmt.Errorf("resolve candidate: %w", err)
		}
		c.Candidate = resolved
	}
	for name, value := range c.References {
		resolved, err := resolveResource(c.BaseDir, value)
		if err != nil {
			return fmt.Errorf("resolve reference %q: %w", name, err)
		}
		c.References[name] = resolved
	}
	for label, command := range map[string]*CommandConfig{
		"prepare": c.Prepare,
		"verify":  &c.Verify,
	} {
		if command == nil || len(command.Command) == 0 {
			continue
		}
		resolved, err := resolveExecutable(c.BaseDir, command.Command[0])
		if err != nil {
			return fmt.Errorf("resolve %s executable: %w", label, err)
		}
		command.Command[0] = resolved
	}
	return nil
}

func resolveResource(base, path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func resolveExecutable(base, command string) (string, error) {
	if strings.ContainsRune(command, filepath.Separator) {
		if !filepath.IsAbs(command) {
			command = filepath.Join(base, command)
		}
	} else if command != "" {
		found, err := exec.LookPath(command)
		if err != nil {
			return "", err
		}
		command = found
	}
	absolute, err := filepath.Abs(command)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func (c *Config) Validate() error {
	if err := c.resolvePaths(); err != nil {
		return err
	}
	if c.Version != 1 {
		return fmt.Errorf("version must be 1, got %d", c.Version)
	}
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("name is required")
	}
	if strings.TrimSpace(c.Goal) == "" {
		return errors.New("goal is required")
	}
	if c.Attempts < 1 {
		return errors.New("attempts must be at least 1")
	}
	if c.Candidate == "" {
		return errors.New("candidate is required")
	}
	if err := validateDirectory("candidate", c.Candidate); err != nil {
		return err
	}
	if len(c.Verify.Command) == 0 || strings.TrimSpace(c.Verify.Command[0]) == "" {
		return errors.New("verify.command requires at least one argument")
	}
	if c.Prepare != nil && (len(c.Prepare.Command) == 0 || strings.TrimSpace(c.Prepare.Command[0]) == "") {
		return errors.New("prepare.command requires at least one argument")
	}
	if time.Duration(c.Agent.Timeout) <= 0 || time.Duration(c.Exercise.Timeout) <= 0 || time.Duration(c.Verify.Timeout) <= 0 {
		return errors.New("agent, exercise, and verify timeouts must be positive")
	}
	if err := validateAgentValue("agent.provider", c.Agent.Provider); err != nil {
		return err
	}
	if err := validateAgentValue("agent.model", c.Agent.Model); err != nil {
		return err
	}
	if c.Agent.ZDR && c.Agent.Provider != "openrouter" {
		return errors.New("agent.zdr requires agent.provider to be openrouter")
	}
	switch c.Agent.Thinking {
	case "off", "minimal", "low", "medium", "high", "xhigh", "max":
	default:
		return fmt.Errorf("agent.thinking %q must be one of off, minimal, low, medium, high, xhigh, or max", c.Agent.Thinking)
	}
	if c.Prepare != nil && time.Duration(c.Prepare.Timeout) <= 0 {
		return errors.New("prepare timeout must be positive")
	}

	for name := range c.Parameters {
		if !parameterNamePattern.MatchString(name) {
			return fmt.Errorf("parameter name %q must match %s", name, parameterNamePattern)
		}
	}
	environments := map[string][]string{
		"agent.pass_env":  c.Agent.PassEnv,
		"verify.pass_env": c.Verify.PassEnv,
	}
	if c.Prepare != nil {
		environments["prepare.pass_env"] = c.Prepare.PassEnv
	}
	for label, names := range environments {
		if err := validateEnvironmentNames(label, names); err != nil {
			return err
		}
	}
	if err := validateDevices(c.Sandbox.Devices); err != nil {
		return err
	}

	referenceEnvironmentNames := make(map[string]string)
	for name, value := range c.References {
		if !resourceNamePattern.MatchString(name) {
			return fmt.Errorf("reference name %q must match %s", name, resourceNamePattern)
		}
		if err := validateDirectory(fmt.Sprintf("reference %q", name), value); err != nil {
			return err
		}
		if pathsOverlap(c.Candidate, value) {
			return fmt.Errorf("candidate overlaps reference %q", name)
		}
		environment := environmentName(name)
		if previous, exists := referenceEnvironmentNames[environment]; exists {
			return fmt.Errorf("reference names %q and %q collide in verifier environment", previous, name)
		}
		referenceEnvironmentNames[environment] = name
	}
	referencePaths := sortedKeys(c.References)
	for first := 0; first < len(referencePaths); first++ {
		for second := first + 1; second < len(referencePaths); second++ {
			if pathsOverlap(c.References[referencePaths[first]], c.References[referencePaths[second]]) {
				return fmt.Errorf("references %q and %q overlap", referencePaths[first], referencePaths[second])
			}
		}
	}
	for label, command := range map[string]*CommandConfig{"prepare": c.Prepare, "verify": &c.Verify} {
		if command == nil {
			continue
		}
		if err := validateTrustedExecutable(label, command.Command[0], c.Candidate); err != nil {
			return err
		}
	}
	return nil
}

func validateAgentValue(label, value string) error {
	if !agentValuePattern.MatchString(value) {
		return fmt.Errorf("%s %q must be a single provider or model identifier", label, value)
	}
	return nil
}

func validateEnvironmentNames(label string, names []string) error {
	seen := make(map[string]bool)
	for _, name := range names {
		if !environmentNamePattern.MatchString(name) {
			return fmt.Errorf("%s value %q is not an environment-variable name", label, name)
		}
		if seen[name] {
			return fmt.Errorf("%s contains duplicate %q", label, name)
		}
		if strings.HasPrefix(name, "ALO_") {
			return fmt.Errorf("%s may not pass reserved ALO_ variable %q", label, name)
		}
		seen[name] = true
	}
	return nil
}

func validateDevices(devices []string) error {
	seen := make(map[string]bool)
	for _, path := range devices {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("sandbox device path %q must be absolute", path)
		}
		if strings.ContainsAny(path, ",:") {
			return fmt.Errorf("sandbox device path %q may not contain a comma or colon", path)
		}
		path = filepath.Clean(path)
		if path == "/dev" || !pathWithin(path, "/dev") {
			return fmt.Errorf("sandbox device path %q must name a specific path beneath /dev", path)
		}
		if seen[path] {
			return fmt.Errorf("sandbox.devices contains duplicate %q", path)
		}
		seen[path] = true
	}
	return nil
}

func validateDirectory(label, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s path %q is not a directory", label, path)
	}
	if strings.Contains(path, ",") {
		return fmt.Errorf("%s path may not contain a comma", label)
	}
	return nil
}

func validateTrustedExecutable(label, executable, candidate string) error {
	info, err := os.Stat(executable)
	if err != nil {
		return fmt.Errorf("inspect %s executable: %w", label, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return fmt.Errorf("%s executable %q is not an executable file", label, executable)
	}
	if pathWithin(executable, candidate) {
		return fmt.Errorf("%s executable is inside candidate", label)
	}
	return nil
}

func pathsOverlap(first, second string) bool {
	first = filepath.Clean(first)
	second = filepath.Clean(second)
	return first == second ||
		strings.HasPrefix(first, second+string(filepath.Separator)) ||
		strings.HasPrefix(second, first+string(filepath.Separator))
}

func pathWithin(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

func environmentName(name string) string {
	return strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}
