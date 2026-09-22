package auth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	keyring "github.com/zalando/go-keyring"
)

const Service = "alo"

var ErrNotFound = errors.New("credential not found")

type Store interface {
	Set(provider, secret string) error
	Get(provider string) (string, error)
	Delete(provider string) error
}

type KeyringStore struct{}

func (KeyringStore) Set(provider, secret string) error {
	if err := keyring.Set(Service, provider, secret); err != nil {
		return fmt.Errorf("store %s credential in OS keyring: %w", provider, err)
	}
	return nil
}

func (KeyringStore) Get(provider string) (string, error) {
	secret, err := keyring.Get(Service, provider)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read %s credential from OS keyring: %w", provider, err)
	}
	return secret, nil
}

func (KeyringStore) Delete(provider string) error {
	err := keyring.Delete(Service, provider)
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("delete %s credential from OS keyring: %w", provider, err)
	}
	return nil
}

type FileStore struct {
	Root string
}

func (s FileStore) Set(provider, secret string) error {
	root, err := s.root()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create credential directory: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return fmt.Errorf("secure credential directory: %w", err)
	}
	path := filepath.Join(root, provider)
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("credential path %q is not a regular file", path)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	temporary, err := os.CreateTemp(root, ".credential-*")
	if err != nil {
		return fmt.Errorf("create temporary credential: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	_, writeErr := temporary.WriteString(secret)
	closeErr := temporary.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("write credential: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("store credential: %w", err)
	}
	return nil
}

func (s FileStore) Get(provider string) (string, error) {
	path, err := s.path(provider)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("credential path %q is not a regular file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("credential file %q must have mode 0600", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read credential file: %w", err)
	}
	secret := strings.TrimSpace(string(data))
	if secret == "" {
		return "", fmt.Errorf("credential file %q is empty", path)
	}
	return secret, nil
}

func (s FileStore) Delete(provider string) error {
	path, err := s.path(provider)
	if err != nil {
		return err
	}
	if err := os.Remove(path); os.IsNotExist(err) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("delete credential file: %w", err)
	}
	return nil
}

func (s FileStore) path(provider string) (string, error) {
	root, err := s.root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, provider), nil
}

func (s FileStore) root() (string, error) {
	if s.Root != "" {
		return s.Root, nil
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user configuration directory: %w", err)
	}
	return filepath.Join(root, "alo", "credentials"), nil
}

type DefaultStore struct {
	Keyring Store
	File    Store
}

func (s DefaultStore) Set(provider, secret string) error {
	if err := s.keyring().Set(provider, secret); err != nil {
		return fmt.Errorf("%w; on a headless system run `alo auth set %s --file`", err, provider)
	}
	return nil
}

func (s DefaultStore) Get(provider string) (string, error) {
	secret, keyringErr := s.keyring().Get(provider)
	if keyringErr == nil {
		return secret, nil
	}
	secret, fileErr := s.file().Get(provider)
	if fileErr == nil {
		return secret, nil
	}
	if errors.Is(keyringErr, ErrNotFound) && errors.Is(fileErr, ErrNotFound) {
		return "", ErrNotFound
	}
	if !errors.Is(fileErr, ErrNotFound) {
		return "", fileErr
	}
	return "", fmt.Errorf("%w; on a headless system run `alo auth set %s --file`", keyringErr, provider)
}

func (s DefaultStore) Delete(provider string) error {
	if err := s.keyring().Delete(provider); err != nil {
		return fmt.Errorf("%w; to remove a file credential run `alo auth delete %s --file`", err, provider)
	}
	return nil
}

func (s DefaultStore) keyring() Store {
	if s.Keyring != nil {
		return s.Keyring
	}
	return KeyringStore{}
}

func (s DefaultStore) file() Store {
	if s.File != nil {
		return s.File
	}
	return FileStore{}
}
