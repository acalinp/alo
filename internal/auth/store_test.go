package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileStoreRoundTripUsesPrivatePermissions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	store := FileStore{Root: root}
	if err := store.Set("openrouter", "secret-key"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, "openrouter"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode = %o", info.Mode().Perm())
	}
	secret, err := store.Get("openrouter")
	if err != nil || secret != "secret-key" {
		t.Fatalf("secret = %q, error = %v", secret, err)
	}
	if err := store.Delete("openrouter"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("openrouter"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted credential error = %v", err)
	}
}

func TestFileStoreRejectsLoosePermissionsAndSymlinks(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "openrouter")
	if err := os.WriteFile(path, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := FileStore{Root: root}
	if _, err := store.Get("openrouter"); err == nil || !strings.Contains(err.Error(), "mode 0600") {
		t.Fatalf("permission error = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("openrouter"); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestDefaultStoreExplainsHeadlessFallback(t *testing.T) {
	store := DefaultStore{
		Keyring: stubStore{err: errors.New("collection is locked")},
		File:    stubStore{err: ErrNotFound},
	}
	_, err := store.Get("openrouter")
	if err == nil || !strings.Contains(err.Error(), "alo auth set openrouter --file") {
		t.Fatalf("error = %v", err)
	}
}

func TestDefaultStoreExplainsFileOptionWhenKeyringSetFails(t *testing.T) {
	store := DefaultStore{Keyring: stubStore{err: errors.New("collection is locked")}}
	err := store.Set("openrouter", "secret")
	if err == nil || !strings.Contains(err.Error(), "alo auth set openrouter --file") {
		t.Fatalf("error = %v", err)
	}
}

type stubStore struct {
	secret string
	err    error
}

func (s stubStore) Set(string, string) error   { return s.err }
func (s stubStore) Get(string) (string, error) { return s.secret, s.err }
func (s stubStore) Delete(string) error        { return s.err }
