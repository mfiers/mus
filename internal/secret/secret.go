// Package secret stores credentials in the OS keyring when available, with a
// fall-back to an age-encrypted file at ~/.config/mus/secrets.age. The
// fall-back is required for HPC compute nodes that have no Secret Service.
//
// The selected backend is sticky for the process: the first successful Get /
// Set decides which backend is used. Environment variable MUS_SECRET_BACKEND
// can override (values: "keyring", "age").
package secret

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"filippo.io/age"
	keyring "github.com/zalando/go-keyring"
)

// Service is the keyring service name used for all mus secrets.
const Service = "mus"

// ErrNotFound indicates the requested secret is not stored.
var ErrNotFound = errors.New("secret not found")

// Store is the secret backend interface.
type Store interface {
	Get(name string) (string, error)
	Set(name, value string) error
	Delete(name string) error
	List() ([]string, error)
	Backend() string
}

// Open selects a backend. The keyring is preferred; if a probe fails (no
// Secret Service running, for example), the age-encrypted file is used.
func Open() (Store, error) {
	switch strings.ToLower(os.Getenv("MUS_SECRET_BACKEND")) {
	case "keyring":
		return openKeyring()
	case "age":
		return openAge()
	}
	if s, err := openKeyring(); err == nil {
		return s, nil
	}
	return openAge()
}

// --- keyring backend ---------------------------------------------------------

type keyringStore struct {
	// index of stored secret names, kept in a single entry so List() works.
	indexMu sync.Mutex
}

func openKeyring() (*keyringStore, error) {
	// Probe: try to set and immediately get a sentinel. If the OS has no
	// Secret Service, this fails fast.
	const probe = "__mus_probe__"
	if err := keyring.Set(Service, probe, "ok"); err != nil {
		return nil, fmt.Errorf("keyring probe failed: %w", err)
	}
	if _, err := keyring.Get(Service, probe); err != nil {
		return nil, fmt.Errorf("keyring probe get failed: %w", err)
	}
	_ = keyring.Delete(Service, probe)
	return &keyringStore{}, nil
}

func (k *keyringStore) Backend() string { return "keyring" }

func (k *keyringStore) Get(name string) (string, error) {
	v, err := keyring.Get(Service, name)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", ErrNotFound
		}
		return "", err
	}
	return v, nil
}

func (k *keyringStore) Set(name, value string) error {
	if err := keyring.Set(Service, name, value); err != nil {
		return err
	}
	k.indexAdd(name)
	return nil
}

func (k *keyringStore) Delete(name string) error {
	if err := keyring.Delete(Service, name); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	k.indexRemove(name)
	return nil
}

// List uses an internal index entry (`__mus_index__`) holding newline-separated
// names. The keyring API has no list, so we maintain this ourselves.
const indexKey = "__mus_index__"

func (k *keyringStore) List() ([]string, error) {
	k.indexMu.Lock()
	defer k.indexMu.Unlock()
	raw, err := keyring.Get(Service, indexKey)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

func (k *keyringStore) indexAdd(name string) {
	k.indexMu.Lock()
	defer k.indexMu.Unlock()
	cur, _ := keyring.Get(Service, indexKey)
	set := map[string]bool{}
	for _, l := range strings.Split(cur, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			set[l] = true
		}
	}
	set[name] = true
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	_ = keyring.Set(Service, indexKey, strings.Join(names, "\n"))
}

func (k *keyringStore) indexRemove(name string) {
	k.indexMu.Lock()
	defer k.indexMu.Unlock()
	cur, _ := keyring.Get(Service, indexKey)
	var kept []string
	for _, l := range strings.Split(cur, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || l == name {
			continue
		}
		kept = append(kept, l)
	}
	_ = keyring.Set(Service, indexKey, strings.Join(kept, "\n"))
}

// --- age-encrypted-file backend ---------------------------------------------

type ageStore struct {
	id   *age.X25519Identity
	path string
	mu   sync.Mutex
}

func openAge() (*ageStore, error) {
	dir, err := configDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	keyPath := filepath.Join(dir, "secrets.key")
	id, err := loadOrCreateAgeKey(keyPath)
	if err != nil {
		return nil, err
	}
	return &ageStore{
		id:   id,
		path: filepath.Join(dir, "secrets.age"),
	}, nil
}

func loadOrCreateAgeKey(path string) (*age.X25519Identity, error) {
	if raw, err := os.ReadFile(path); err == nil {
		s := strings.TrimSpace(string(raw))
		return age.ParseX25519Identity(s)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(id.String()+"\n"), 0o600); err != nil {
		return nil, err
	}
	return id, nil
}

func (a *ageStore) Backend() string { return "age" }

func (a *ageStore) readAll() (map[string]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f, err := os.Open(a.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	defer f.Close()
	r, err := age.Decrypt(f, a.id)
	if err != nil {
		return nil, fmt.Errorf("decrypt %s: %w", a.path, err)
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx <= 0 {
			continue
		}
		out[line[:idx]] = line[idx+1:]
	}
	return out, nil
}

func (a *ageStore) writeAll(values map[string]string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	tmp, err := os.CreateTemp(filepath.Dir(a.path), "secrets.age.tmp.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	w, err := age.Encrypt(tmp, a.id.Recipient())
	if err != nil {
		tmp.Close()
		return err
	}
	// stable ordering for diff-ability of the (encrypted) artifact
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	// note: order matters only for deterministic encryption, which age is not;
	// still helps with reasoning when debugging
	for _, k := range keys {
		v := values[k]
		// disallow newlines / "=" in keys to keep the line format intact
		if strings.ContainsAny(k, "=\n") {
			w.Close()
			tmp.Close()
			return fmt.Errorf("invalid secret name %q", k)
		}
		if strings.Contains(v, "\n") {
			w.Close()
			tmp.Close()
			return fmt.Errorf("secret %q contains newline", k)
		}
		if _, err := fmt.Fprintf(w, "%s=%s\n", k, v); err != nil {
			w.Close()
			tmp.Close()
			return err
		}
	}
	if err := w.Close(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, a.path)
}

func (a *ageStore) Get(name string) (string, error) {
	values, err := a.readAll()
	if err != nil {
		return "", err
	}
	v, ok := values[name]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (a *ageStore) Set(name, value string) error {
	values, err := a.readAll()
	if err != nil {
		return err
	}
	values[name] = value
	return a.writeAll(values)
}

func (a *ageStore) Delete(name string) error {
	values, err := a.readAll()
	if err != nil {
		return err
	}
	if _, ok := values[name]; !ok {
		return nil
	}
	delete(values, name)
	return a.writeAll(values)
}

func (a *ageStore) List() ([]string, error) {
	values, err := a.readAll()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(values))
	for k := range values {
		names = append(names, k)
	}
	return names, nil
}

func configDir() (string, error) {
	if d := os.Getenv("MUS_CONFIG_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "mus"), nil
}
