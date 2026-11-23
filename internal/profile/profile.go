// Package profile loads host profiles: small YAML files that describe how one family of cPanel
// hosts differs from the generic engine (port, auth method, preferred PHP, probes, quirks).
// Built-in profiles are embedded; extra ones are read from <config dir>/profiles/*.yaml and
// override built-ins with the same id.
package profile

import (
	"embed"
	"fmt"
	"net"
	"net/url"
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
	// Match lists the cPanel host names this profile is picked for automatically: an entry
	// matches the host itself or any name under it ("autopilo.ir" fits autopilo.ir and
	// srv1.autopilo.ir); an entry starting with "." (or "*.") fits only names under it.
	// Projects never choose a profile by hand: the longest matching entry wins, else generic.
	Match []string `yaml:"match"`
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
	var m []string
	for _, e := range p.Match {
		e = strings.ToLower(strings.TrimSpace(e))
		e = strings.TrimSuffix(strings.TrimPrefix(e, "*"), ".")
		if e != "" && e != "." {
			m = append(m, e)
		}
	}
	p.Match = m
}

// matchLen is the length of the longest Match entry that fits host (0 when none does).
func (p *Profile) matchLen(host string) int {
	best := 0
	for _, e := range p.Match {
		ok := false
		if strings.HasPrefix(e, ".") {
			ok = strings.HasSuffix(host, e)
		} else {
			ok = host == e || strings.HasSuffix(host, "."+e)
		}
		if ok && len(e) > best {
			best = len(e)
		}
	}
	return best
}

// HostOf returns the lower-case host name of a typed cPanel address ("https://x.com:2083/",
// "x.com:2083", "x.com"); "" when there is none.
func HostOf(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if !strings.Contains(addr, "://") {
		addr = "https://" + addr
	}
	u, err := url.Parse(addr)
	if err != nil {
		return ""
	}
	h := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if ip := net.ParseIP(h); ip != nil {
		return ip.String()
	}
	return h
}

// ForAddr picks the profile for a cPanel address: the one whose match entry fits its host
// (the longest entry wins; equal lengths go to the first in list order), else "generic", else
// the first profile. Nil only for an empty list.
func ForAddr(list []*Profile, addr string) *Profile {
	host := HostOf(addr)
	var best *Profile
	bestLen := 0
	if host != "" {
		for _, p := range list {
			if n := p.matchLen(host); n > bestLen {
				best, bestLen = p, n
			}
		}
	}
	if best != nil {
		return best
	}
	if g, ok := Find(list, "generic"); ok {
		return g
	}
	if len(list) > 0 {
		return list[0]
	}
	return nil
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
