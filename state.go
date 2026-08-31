package alo

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"
)

type Status string

const (
	StatusPreparing Status = "preparing"
	StatusRunning   Status = "running"
	StatusBlocked   Status = "blocked"
	StatusFailed    Status = "failed"
	StatusSucceeded Status = "succeeded"
	StatusStopped   Status = "stopped"
)

type Phase string

const (
	PhaseVerify Phase = "verify"
	PhaseAgent  Phase = "agent"
)

type OutputRecord struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type RunState struct {
	ID            string                  `json:"id"`
	ConfigDir     string                  `json:"config_dir"`
	Status        Status                  `json:"status"`
	Attempt       int                     `json:"attempt"`
	Phase         Phase                   `json:"phase"`
	ImageID       string                  `json:"image_id"`
	Failure       string                  `json:"failure,omitempty"`
	BlockedReason string                  `json:"blocked_reason,omitempty"`
	Outputs       map[string]OutputRecord `json:"outputs,omitempty"`
	UpdatedAt     time.Time               `json:"updated_at"`
}

var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type RunStore struct {
	Root        string
	ID          string
	Dir         string
	ConfigPath  string
	StatePath   string
	AttemptsDir string
	CacheDir    string
	SessionDir  string

	lock *os.File
}

func NewRunStore(root, id string) *RunStore {
	dir := filepath.Join(root, "runs", id)
	return &RunStore{
		Root:        root,
		ID:          id,
		Dir:         dir,
		ConfigPath:  filepath.Join(dir, "config.yaml"),
		StatePath:   filepath.Join(dir, "state.json"),
		AttemptsDir: filepath.Join(dir, "attempts"),
		CacheDir:    filepath.Join(dir, "cache"),
		SessionDir:  filepath.Join(dir, "session"),
	}
}

func DefaultStateRoot() string {
	if value := os.Getenv("ALO_STATE_DIR"); value != "" {
		return value
	}
	if value := os.Getenv("XDG_STATE_HOME"); value != "" {
		return filepath.Join(value, "alo")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".alo-state"
	}
	return filepath.Join(home, ".local", "state", "alo")
}

func NewRunID(now time.Time) (string, error) {
	random := make([]byte, 3)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("create run ID: %w", err)
	}
	return now.UTC().Format("20060102-150405") + "-" + hex.EncodeToString(random), nil
}

func validateRunID(id string) error {
	if !runIDPattern.MatchString(id) || id == "." || id == ".." {
		return fmt.Errorf("invalid run ID %q", id)
	}
	return nil
}

func (s *RunStore) Create() error {
	if err := os.MkdirAll(filepath.Dir(s.Dir), 0o700); err != nil {
		return fmt.Errorf("create runs directory: %w", err)
	}
	if err := os.Mkdir(s.Dir, 0o700); err != nil {
		return fmt.Errorf("create run directory: %w", err)
	}
	for _, directory := range []string{s.AttemptsDir, s.CacheDir, s.SessionDir} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return fmt.Errorf("create run directory %q: %w", directory, err)
		}
	}
	return nil
}

func (s *RunStore) AttemptDir(attempt int) string {
	return filepath.Join(s.AttemptsDir, fmt.Sprintf("%04d", attempt))
}

func (s *RunStore) EnsureAttempt(attempt int) (string, error) {
	directory := s.AttemptDir(attempt)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create attempt directory: %w", err)
	}
	return directory, nil
}

func (s *RunStore) Lock() error {
	if s.lock != nil {
		return nil
	}
	path := filepath.Join(s.Dir, ".lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open run lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return fmt.Errorf("run %s is already active", s.ID)
		}
		return fmt.Errorf("lock run: %w", err)
	}
	s.lock = file
	return nil
}

func (s *RunStore) Unlock() error {
	if s.lock == nil {
		return nil
	}
	file := s.lock
	s.lock = nil
	unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	closeErr := file.Close()
	return errors.Join(unlockErr, closeErr)
}

func (s *RunStore) Save(state *RunState) error {
	state.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode run state: %w", err)
	}
	data = append(data, '\n')
	return atomicWrite(s.StatePath, data, 0o600)
}

func (s *RunStore) Load() (*RunState, error) {
	data, err := os.ReadFile(s.StatePath)
	if err != nil {
		return nil, fmt.Errorf("read run state: %w", err)
	}
	var state RunState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode run state: %w", err)
	}
	if state.ID != s.ID {
		return nil, fmt.Errorf("run state ID %q does not match %q", state.ID, s.ID)
	}
	if state.Outputs == nil {
		state.Outputs = make(map[string]OutputRecord)
	}
	return &state, nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, mode); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	return nil
}
