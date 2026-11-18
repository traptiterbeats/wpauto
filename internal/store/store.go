// Package store keeps the list of cPanel hosts in <config dir>/hosts.json. Secrets are stored
// encrypted with secretbox; the plain secret never touches disk.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/traptiterbeats/wpauto/internal/config"
	"github.com/traptiterbeats/wpauto/internal/cpanel"
)

// Host is one cPanel account the panel can work on.
type Host struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	ProfileID    string         `json:"profile_id"`
	URL          string         `json:"url"`
	Username     string         `json:"username"`
	SealedSecret string         `json:"sealed_secret"`
	CreatedAt    time.Time      `json:"created_at"`
	FailedLogins int            `json:"failed_logins"`
	LastReport   *cpanel.Report `json:"last_report,omitempty"`
}

// Locked reports whether automatic logins are paused after failures.
func (h *Host) Locked(max int) bool { return h.FailedLogins >= max }

// Store is a JSON-file backed host list.
type Store struct {
	mu    sync.Mutex
	path  string
	hosts []*Host
}

// Open loads hosts.json from dir.
func Open(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, "hosts.json")}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.hosts); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) save() error {
	b, err := json.MarshalIndent(s.hosts, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// List returns a copy of all hosts sorted by name.
func (s *Store) List() []Host {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Host, len(s.hosts))
	for i, h := range s.hosts {
		out[i] = *h
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Get returns a copy of one host.
func (s *Store) Get(id string) (Host, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range s.hosts {
		if h.ID == id {
			return *h, true
		}
	}
	return Host{}, false
}

// Add inserts a new host and returns it.
func (s *Store) Add(h Host) (Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h.ID = config.RandString(10)
	h.CreatedAt = time.Now()
	s.hosts = append(s.hosts, &h)
	return h, s.save()
}

// Update applies fn to the stored host.
func (s *Store) Update(id string, fn func(*Host)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range s.hosts {
		if h.ID == id {
			fn(h)
			return s.save()
		}
	}
	return errors.New("host not found")
}

// Delete removes a host.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, h := range s.hosts {
		if h.ID == id {
			s.hosts = append(s.hosts[:i], s.hosts[i+1:]...)
			return s.save()
		}
	}
	return errors.New("host not found")
}
