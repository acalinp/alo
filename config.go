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
	defaultAttempts      = 10
	defaultAgentTimeout  = 15 * time.Minute
	defaultVerifyTimeout = 30 * time.Minute
)

var (
	resourceNamePattern    = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	parameterNamePattern   = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type Config struct {
	Version    int               `yaml:"version"`
	Name       string            `yaml:"name"`
	Goal       string            `yaml:"goal"`
	Parameters map[string]string `yaml:"parameters,omitempty"`
	Candidates map[string]string `yaml:"candidates"`
	References map[string]string `yaml:"references,omitempty"`
	Agent      AgentConfig       `yaml:"agent"`
	Verify     VerifyConfig      `yaml:"verify"`
	Attempts   int               `yaml:"attempts,omitempty"`

	Path    string `yaml:"-"`
	BaseDir string `yaml:"-"`
}

type AgentConfig struct {
	PassEnv []string `yaml:"pass_env,omitempty"`
	Devices []string `yaml:"devices,omitempty"`
	Timeout Duration `yaml:"timeout,omitempty"`
}

type VerifyConfig struct {
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
	if c.Candidates == nil {
		c.Candidates = make(map[string]string)
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
	foundAgentKey := false
	for _, name := range c.Agent.PassEnv {
		if name == "OPENROUTER_API_KEY" {
			foundAgentKey = true
			break
		}
	}
	if !foundAgentKey {
		c.Agent.PassEnv = append(c.Agent.PassEnv, "OPENROUTER_API_KEY")
	}
	if c.Verify.Timeout == 0 {
		c.Verify.Timeout = Duration(defaultVerifyTimeout)
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

	for _, resources := range []map[string]string{c.Candidates, c.References} {
		for name, value := range resources {
			if !filepath.IsAbs(value) {
				value = filepath.Join(c.BaseDir, value)
			}
			absolute, err := filepath.Abs(value)
			if err != nil {
				return fmt.Errorf("resolve resource %q: %w", name, err)
			}
			resolved, err := filepath.EvalSymlinks(absolute)
			if err != nil {
				return fmt.Errorf("resolve resource %q: %w", name, err)
			}
			resources[name] = filepath.Clean(resolved)
		}
	}
	if len(c.Verify.Command) > 0 {
		command := c.Verify.Command[0]
		if strings.ContainsRune(command, filepath.Separator) {
			if !filepath.IsAbs(command) {
				command = filepath.Join(c.BaseDir, command)
			}
		} else if command != "" {
			found, err := exec.LookPath(command)
			if err != nil {
				return fmt.Errorf("resolve verifier executable %q: %w", command, err)
			}
			command = found
		}
		absolute, err := filepath.Abs(command)
		if err != nil {
			return fmt.Errorf("resolve verifier executable: %w", err)
		}
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return fmt.Errorf("resolve verifier executable %q: %w", command, err)
		}
		c.Verify.Command[0] = filepath.Clean(resolved)
	}
	return nil
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
	if len(c.Candidates) == 0 {
		return errors.New("at least one candidate is required")
	}
	if len(c.Verify.Command) == 0 || strings.TrimSpace(c.Verify.Command[0]) == "" {
		return errors.New("verify.command requires at least one argument")
	}
	if time.Duration(c.Agent.Timeout) <= 0 || time.Duration(c.Verify.Timeout) <= 0 {
		return errors.New("agent and verify timeouts must be positive")
	}

	for name := range c.Parameters {
		if !parameterNamePattern.MatchString(name) {
			return fmt.Errorf("parameter name %q must match %s", name, parameterNamePattern)
		}
	}
	for label, names := range map[string][]string{
		"agent.pass_env":  c.Agent.PassEnv,
		"verify.pass_env": c.Verify.PassEnv,
	} {
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
	}
	seenDevices := make(map[string]bool)
	for _, path := range c.Agent.Devices {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("agent device path %q must be absolute", path)
		}
		if strings.ContainsAny(path, ",:") {
			return fmt.Errorf("agent device path %q may not contain a comma or colon", path)
		}
		path = filepath.Clean(path)
		if path == "/dev" || !pathWithin(path, "/dev") {
			return fmt.Errorf("agent device path %q must name a specific path beneath /dev", path)
		}
		if seenDevices[path] {
			return fmt.Errorf("agent.devices contains duplicate %q", path)
		}
		seenDevices[path] = true
	}

	for kind, resources := range map[string]map[string]string{
		"candidate": c.Candidates,
		"reference": c.References,
	} {
		environmentNames := make(map[string]string)
		for name, value := range resources {
			if !resourceNamePattern.MatchString(name) {
				return fmt.Errorf("%s name %q must match %s", kind, name, resourceNamePattern)
			}
			info, err := os.Stat(value)
			if err != nil {
				return fmt.Errorf("%s %q: %w", kind, name, err)
			}
			if !info.IsDir() {
				return fmt.Errorf("%s %q path %q is not a directory", kind, name, value)
			}
			if strings.Contains(value, ",") {
				return fmt.Errorf("%s %q path may not contain a comma", kind, name)
			}
			environment := environmentName(name)
			if previous, exists := environmentNames[environment]; exists {
				return fmt.Errorf("%s names %q and %q collide in verifier environment", kind, previous, name)
			}
			environmentNames[environment] = name
		}
	}

	type namedPath struct {
		kind string
		name string
		path string
	}
	var paths []namedPath
	for name, value := range c.Candidates {
		paths = append(paths, namedPath{kind: "candidate", name: name, path: value})
	}
	for name, value := range c.References {
		paths = append(paths, namedPath{kind: "reference", name: name, path: value})
	}
	for first := 0; first < len(paths); first++ {
		for second := first + 1; second < len(paths); second++ {
			if pathsOverlap(paths[first].path, paths[second].path) {
				return fmt.Errorf(
					"%s %q overlaps %s %q",
					paths[first].kind, paths[first].name,
					paths[second].kind, paths[second].name,
				)
			}
		}
	}
	if strings.ContainsRune(c.Verify.Command[0], filepath.Separator) {
		verifier := c.Verify.Command[0]
		if !filepath.IsAbs(verifier) {
			verifier = filepath.Join(c.BaseDir, verifier)
		}
		resolved, err := filepath.EvalSymlinks(verifier)
		if err != nil {
			return fmt.Errorf("resolve verifier executable %q: %w", verifier, err)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return fmt.Errorf("inspect verifier executable: %w", err)
		}
		if !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
			return fmt.Errorf("verifier executable %q is not an executable file", verifier)
		}
		for name, candidate := range c.Candidates {
			if pathWithin(resolved, candidate) {
				return fmt.Errorf("verifier executable is inside candidate %q", name)
			}
		}
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
