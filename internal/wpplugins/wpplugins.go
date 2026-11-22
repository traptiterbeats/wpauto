// Package wpplugins is the curated list of common WordPress plugins a project can install and
// activate from its «افزونه‌های لازم» page. The list is an embedded YAML file; more entries can be
// added (or built-in ones replaced/hidden) in <config dir>/recommended-plugins.yaml.
package wpplugins

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/traptiterbeats/wpauto/internal/config"
)

//go:embed recommended.yaml
var builtin []byte

// OverlayFile is the name of the optional extra list in the config dir.
const OverlayFile = "recommended-plugins.yaml"

// Plugin is one recommended plugin.
type Plugin struct {
	Slug   string `yaml:"slug"` // wordpress.org slug (also its folder in wp-content/plugins)
	Name   string `yaml:"name"`
	Desc   string `yaml:"desc"` // one-line Persian description
	Icon   string `yaml:"icon"` // an emoji shown on the card
	Hidden bool   `yaml:"hidden"`
}

type file struct {
	Plugins []Plugin `yaml:"plugins"`
}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidSlug reports whether s looks like a wordpress.org plugin slug.
func ValidSlug(s string) bool { return slugRe.MatchString(s) }

func parse(b []byte, src string) ([]Plugin, error) {
	var f file
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", src, err)
	}
	for i := range f.Plugins {
		p := &f.Plugins[i]
		p.Slug = strings.ToLower(strings.TrimSpace(p.Slug))
		if !ValidSlug(p.Slug) {
			return nil, fmt.Errorf("%s: invalid plugin slug %q", src, p.Slug)
		}
		if p.Name == "" {
			p.Name = p.Slug
		}
		if p.Icon == "" {
			p.Icon = "🧩"
		}
	}
	return f.Plugins, nil
}

// Load returns the built-in list overlaid with dir/recommended-plugins.yaml (when present).
func Load(dir string) ([]Plugin, error) {
	list, err := parse(builtin, "builtin/recommended.yaml")
	if err != nil {
		return nil, err
	}
	if dir != "" {
		b, err := os.ReadFile(filepath.Join(dir, OverlayFile))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err == nil {
			extra, err := parse(b, OverlayFile)
			if err != nil {
				return nil, err
			}
			for _, e := range extra {
				replaced := false
				for i := range list {
					if list[i].Slug == e.Slug {
						list[i], replaced = e, true
					}
				}
				if !replaced {
					list = append(list, e)
				}
			}
		}
	}
	out := list[:0]
	for _, p := range list {
		if !p.Hidden {
			out = append(out, p)
		}
	}
	return out, nil
}

// List is Load on the panel's config dir; a broken overlay falls back to the built-in list.
func List() []Plugin {
	list, err := Load(config.Dir())
	if err != nil {
		list, _ = Load("")
	}
	return list
}

// Find returns the plugin with slug.
func Find(list []Plugin, slug string) (Plugin, bool) {
	for _, p := range list {
		if p.Slug == slug {
			return p, true
		}
	}
	return Plugin{}, false
}

// Plugin states as reported by a status check or an install run.
const (
	StateActive   = "active"
	StateInactive = "inactive"
	StateMissing  = "missing"
)
