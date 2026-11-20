// Package projects keeps the client sites being built: one project follows a site from the host
// check to the installed, customised theme, and the panel lights each stage as its runs succeed.
package projects

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/traptiterbeats/wpauto/internal/config"
)

// Project is one client site.
type Project struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	HostID  string `json:"host_id,omitempty"`
	Domain  string `json:"domain,omitempty"` // site domain or subdomain
	Folder  string `json:"folder,omitempty"` // when WordPress is in a folder of Domain
	DraftID string `json:"draft_id,omitempty"`
	// Marked holds stages the user ticked as done by hand (e.g. WordPress was already there).
	Marked  map[string]bool `json:"marked,omitempty"`
	Created time.Time       `json:"created"`
}

// Site is the address shown to the user (domain/folder).
func (p Project) Site() string {
	if p.Folder != "" {
		return p.Domain + "/" + p.Folder
	}
	return p.Domain
}

type file struct {
	Active   string     `json:"active,omitempty"`
	Projects []*Project `json:"projects"`
}

// Store saves projects in <dir>/projects.json.
type Store struct {
	mu   sync.Mutex
	path string
	f    file
}

// ErrNotFound is returned for an unknown project id.
var ErrNotFound = errors.New("project not found")

// Open loads (or starts) the store.
func Open(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, "projects.json")}
	b, err := os.ReadFile(s.path)
	if err == nil {
		err = json.Unmarshal(b, &s.f)
	} else if os.IsNotExist(err) {
		err = nil
	}
	return s, err
}

func (s *Store) save() error {
	b, _ := json.MarshalIndent(s.f, "", " ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// List returns the projects, newest first.
func (s *Store) List() []Project {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Project, 0, len(s.f.Projects))
	for _, p := range s.f.Projects {
		out = append(out, *p)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// Get returns one project.
func (s *Store) Get(id string) (Project, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.f.Projects {
		if p.ID == id {
			return *p, true
		}
	}
	return Project{}, false
}

// Active returns the project the panel is working on.
func (s *Store) Active() (Project, bool) {
	s.mu.Lock()
	id := s.f.Active
	s.mu.Unlock()
	if id == "" {
		return Project{}, false
	}
	return s.Get(id)
}

// Add creates a project and makes it active.
func (s *Store) Add(p Project) (Project, error) {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return p, errors.New("نام پروژه لازم است")
	}
	p.ID = time.Now().Format("20060102") + "-" + config.RandString(5)
	p.Created = time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.f.Projects = append(s.f.Projects, &p)
	s.f.Active = p.ID
	return p, s.save()
}

// SetActive switches the active project ("" = none).
func (s *Store) SetActive(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != "" {
		found := false
		for _, p := range s.f.Projects {
			found = found || p.ID == id
		}
		if !found {
			return ErrNotFound
		}
	}
	s.f.Active = id
	return s.save()
}

// Update changes a project.
func (s *Store) Update(id string, fn func(*Project)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.f.Projects {
		if p.ID == id {
			fn(p)
			p.ID = id
			return s.save()
		}
	}
	return ErrNotFound
}

// Delete removes a project (its runs and drafts stay).
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.f.Projects {
		if p.ID == id {
			s.f.Projects = append(s.f.Projects[:i], s.f.Projects[i+1:]...)
			if s.f.Active == id {
				s.f.Active = ""
			}
			return s.save()
		}
	}
	return ErrNotFound
}
