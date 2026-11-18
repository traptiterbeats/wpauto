// Package profile loads host profiles: small YAML files that describe how one family of cPanel
// hosts differs from the generic engine (port, auth method, preferred PHP, probes, quirks).
// Built-in profiles are embedded; extra ones are read from <config dir>/profiles/*.yaml and
// override built-ins with the same id.
package profile

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed builtin/*.yaml
var builtinFS embed.FS

// Profile describes a host family.
type Profile struct {
	ID               string   `yaml:"id"`
	Name             string   `yaml:"name"`
	Description      string   `yaml:"description"`
	Port             int      `yaml:"port"`
	Auth             string   `yaml:"auth"` // session | token
	InsecureTLS      bool     `yaml:"insecure_tls"`
	MaxLoginAttempts int      `yaml:"max_login_attempts"`
	PreferredPHP     []string `yaml:"preferred_php"`
	Probes           []string `yaml:"probes"`
	// Quirks are free-form switches read by later phases (e.g. helper_upload: fileman).
	Quirks map[string]string `yaml:"quirks"`
	Source string            `yaml:"-"`
}

func (p *Profile) defaults() {
	if p.Port == 0 {
		p.Port = 2083
	}
	if p.Auth == "" {
		p.Auth = "session"
	}
	if p.MaxLoginAttempts <= 0 {
		p.MaxLoginAttempts = 1
	}
	if len(p.PreferredPHP) == 0 {
		p.PreferredPHP = []string{"8.3", "8.4", "8.2"}
	}
	if p.Quirks == nil {
		p.Quirks = map[string]string{}
	}
}

// Validate checks required fields.
func (p *Profile) Validate() error {
	if p.ID == "" || strings.ContainsAny(p.ID, " /\\") {
		return fmt.Errorf("profile id is missing or invalid")
	}
	if p.Auth != "session" && p.Auth != "token" {
		return fmt.Errorf("profile %s: auth must be session or token", p.ID)
	}
	return nil
}

// Parse reads one profile from YAML.
func Parse(b []byte, source string) (*Profile, error) {
	var p Profile
	if err := yaml.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	p.defaults()
	p.Source = source
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	return &p, nil
}

// LoadAll returns built-in profiles overlaid with the ones in dir (if it exists), sorted by id
// with "generic" first.
func LoadAll(dir string) ([]*Profile, error) {
	byID := map[string]*Profile{}
	entries, _ := builtinFS.ReadDir("builtin")
	for _, e := range entries {
		b, _ := builtinFS.ReadFile("builtin/" + e.Name())
		p, err := Parse(b, "builtin/"+e.Name())
		if err != nil {
			return nil, err
		}
		byID[p.ID] = p
	}
	if dir != "" {
		files, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				return nil, err
			}
			p, err := Parse(b, f)
			if err != nil {
				return nil, err
			}
			byID[p.ID] = p
		}
	}
	out := make([]*Profile, 0, len(byID))
	for _, p := range byID {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID == "generic" {
			return true
		}
		if out[j].ID == "generic" {
			return false
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Find returns the profile with the given id.
func Find(list []*Profile, id string) (*Profile, bool) {
	for _, p := range list {
		if p.ID == id {
			return p, true
		}
	}
	return nil, false
}
