package auth

import (
	"errors"
	"fmt"

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
