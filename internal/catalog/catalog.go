// Package catalog keeps the themes wpauto can install. Each theme is a folder
// <config>/catalog/themes/<slug>/ with manifest.yaml, the theme zip, an optional Saayeh demo
// package and a screenshot. Plugin zips shared by themes live in <config>/catalog/plugins/<slug>.zip;
// plugins not found there are downloaded from wordpress.org on the host.
package catalog

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ImporterSlug is the Saayeh Demo Importer plugin that imports demo packages.
const ImporterSlug = "saayeh-demo-importer"

// Plugin is one plugin a theme needs.
type Plugin struct {
	Slug string `yaml:"slug" json:"slug"`
	// Source is "library" (zip in the panel's plugin library) or "wporg" (downloaded from
	// wordpress.org). Empty means library when the zip exists, else wporg.
	Source string `yaml:"source,omitempty" json:"source,omitempty"`
}

// Theme is a catalogue entry (manifest.yaml).
type Theme struct {
	Slug        string   `yaml:"slug"`
	Name        string   `yaml:"name"`
	Version     string   `yaml:"version"`
	Description string   `yaml:"description,omitempty"`
	ThemeZip    string   `yaml:"theme_zip"`
	DemoZip     string   `yaml:"demo_zip,omitempty"`
	DemoVersion string   `yaml:"demo_version,omitempty"`
	Screenshot  string   `yaml:"screenshot,omitempty"`
	Plugins     []Plugin `yaml:"plugins,omitempty"`
	// DemoGroups limits the Saayeh option groups imported (empty = all).
	DemoGroups []string `yaml:"demo_groups,omitempty"`
	Dir        string   `yaml:"-"`
}

// HasDemo reports whether the theme ships a demo package.
func (t *Theme) HasDemo() bool { return t.DemoZip != "" }

// Path returns the absolute path of a file in the theme's folder.
func (t *Theme) Path(name string) string { return filepath.Join(t.Dir, filepath.Base(name)) }

// LibPlugin is a plugin zip in the shared library.
type LibPlugin struct {
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Catalog is the on-disk catalogue.
type Catalog struct{ Dir string }

// Open returns the catalogue under dir (created on demand).
func Open(dir string) *Catalog { return &Catalog{Dir: dir} }

func (c *Catalog) themesDir() string  { return filepath.Join(c.Dir, "themes") }
func (c *Catalog) pluginsDir() string { return filepath.Join(c.Dir, "plugins") }

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ValidSlug reports whether s is a safe theme or plugin slug.
func ValidSlug(s string) bool { return slugRe.MatchString(s) }

// List returns all themes, sorted by name.
func (c *Catalog) List() []*Theme {
	entries, _ := os.ReadDir(c.themesDir())
	var out []*Theme
	for _, e := range entries {
		if t, err := c.Get(e.Name()); err == nil {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Get loads one theme.
func (c *Catalog) Get(slug string) (*Theme, error) {
	if !ValidSlug(slug) {
		return nil, fmt.Errorf("invalid theme slug %q", slug)
	}
	dir := filepath.Join(c.themesDir(), slug)
	b, err := os.ReadFile(filepath.Join(dir, "manifest.yaml"))
	if err != nil {
		return nil, fmt.Errorf("theme %s is not in the catalogue", slug)
	}
	var t Theme
	if err := yaml.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("theme %s: %w", slug, err)
	}
	if t.Slug != slug {
		return nil, fmt.Errorf("theme %s: manifest slug mismatch", slug)
	}
	t.Dir = dir
	return &t, nil
}

// Delete removes a theme from the catalogue.
func (c *Catalog) Delete(slug string) error {
	if !ValidSlug(slug) {
		return fmt.Errorf("invalid slug")
	}
	return os.RemoveAll(filepath.Join(c.themesDir(), slug))
}

// LibraryPath returns the library zip of a plugin, or "" when it is not in the library.
func (c *Catalog) LibraryPath(slug string) string {
	if !ValidSlug(slug) {
		return ""
	}
	p := filepath.Join(c.pluginsDir(), slug+".zip")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// Library lists the plugin zips in the library.
func (c *Catalog) Library() []LibPlugin {
	files, _ := filepath.Glob(filepath.Join(c.pluginsDir(), "*.zip"))
	var out []LibPlugin
	for _, f := range files {
		info, err := InspectPlugin(f)
		if err != nil {
			continue
		}
		out = append(out, *info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}

// DeletePlugin removes a plugin zip from the library.
func (c *Catalog) DeletePlugin(slug string) error {
	if !ValidSlug(slug) {
		return fmt.Errorf("invalid slug")
	}
	return os.Remove(filepath.Join(c.pluginsDir(), slug+".zip"))
}

// AddPlugin stores a plugin zip (read from src) in the library and returns its info.
func (c *Catalog) AddPlugin(src string) (*LibPlugin, error) {
	info, err := InspectPlugin(src)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(c.pluginsDir(), 0o700); err != nil {
		return nil, err
	}
	if err := copyFile(src, filepath.Join(c.pluginsDir(), info.Slug+".zip")); err != nil {
		return nil, err
	}
	return info, nil
}

// AddInput is what the upload form provides for a new theme.
type AddInput struct {
	ThemeZip   string   // path to the uploaded theme zip (required)
	DemoZip    string   // optional Saayeh demo package
	WPOrg      []string // extra wordpress.org plugin slugs
	Library    []string // extra library plugin slugs
	DemoGroups []string
}

// Add inspects the uploads and creates (or replaces) a catalogue entry.
func (c *Catalog) Add(in AddInput) (*Theme, error) {
	ti, shot, err := InspectTheme(in.ThemeZip)
	if err != nil {
		return nil, err
	}
	t := &Theme{Slug: ti.Slug, Name: ti.Name, Version: ti.Version, Description: ti.Description, ThemeZip: "theme.zip", DemoGroups: in.DemoGroups}
	var plugins []Plugin
	add := func(p Plugin) {
		for _, x := range plugins {
			if x.Slug == p.Slug {
				return
			}
		}
		plugins = append(plugins, p)
	}
	if in.DemoZip != "" {
		di, err := InspectDemo(in.DemoZip)
		if err != nil {
			return nil, err
		}
		if di.Theme != "" && di.Theme != t.Slug {
			return nil, fmt.Errorf("بسته‌ی دمو برای پوسته‌ی %s است، نه %s", di.Theme, t.Slug)
		}
		t.DemoZip, t.DemoVersion = "demo.zip", di.Version
		// Plugins the demo requires come first (e.g. WooCommerce before products are imported).
		for _, r := range di.Requires {
			add(Plugin{Slug: r})
		}
		add(Plugin{Slug: ImporterSlug, Source: "library"})
		if c.LibraryPath(ImporterSlug) == "" {
			return nil, errors.New("برای درون‌ریزی دمو، اول zip افزونه‌ی «درون‌ریز دمو سایه» را در کتابخانه‌ی افزونه‌ها بارگذاری کنید")
		}
	}
	for _, s := range in.Library {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if c.LibraryPath(s) == "" {
			return nil, fmt.Errorf("افزونه‌ی %s در کتابخانه نیست", s)
		}
		add(Plugin{Slug: s, Source: "library"})
	}
	for _, s := range in.WPOrg {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		if !ValidSlug(s) {
			return nil, fmt.Errorf("نامک افزونه‌ی «%s» معتبر نیست", s)
		}
		add(Plugin{Slug: s, Source: "wporg"})
	}
	t.Plugins = plugins

	dir := filepath.Join(c.themesDir(), t.Slug)
	tmp := dir + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return nil, err
	}
	fail := func(err error) (*Theme, error) { _ = os.RemoveAll(tmp); return nil, err }
	if err := copyFile(in.ThemeZip, filepath.Join(tmp, "theme.zip")); err != nil {
		return fail(err)
	}
	if in.DemoZip != "" {
		if err := copyFile(in.DemoZip, filepath.Join(tmp, "demo.zip")); err != nil {
			return fail(err)
		}
	}
	if shot != nil {
		t.Screenshot = "screenshot" + shot.ext
		if err := os.WriteFile(filepath.Join(tmp, t.Screenshot), shot.data, 0o600); err != nil {
			return fail(err)
		}
	}
	b, _ := yaml.Marshal(t)
	if err := os.WriteFile(filepath.Join(tmp, "manifest.yaml"), b, 0o600); err != nil {
		return fail(err)
	}
	_ = os.RemoveAll(dir)
	if err := os.Rename(tmp, dir); err != nil {
		return fail(err)
	}
	t.Dir = dir
	return t, nil
}

// ThemeInfo is read from a theme zip's style.css.
type ThemeInfo struct {
	Slug, Name, Version, Description string
}

type image struct {
	ext  string
	data []byte
}

// topFolder returns the single top-level folder of a zip, or an error.
func topFolder(files []*zip.File) (string, error) {
	top := ""
	for _, f := range files {
		name := strings.TrimPrefix(strings.ReplaceAll(f.Name, "\\", "/"), "./")
		if strings.HasPrefix(name, "__MACOSX/") || name == "" {
			continue
		}
		if strings.Contains(name, "..") || strings.HasPrefix(name, "/") {
			return "", fmt.Errorf("unsafe path in zip: %s", f.Name)
		}
		first, _, ok := strings.Cut(name, "/")
		if !ok {
			return "", fmt.Errorf("zip must contain one folder (found file %s at the top)", name)
		}
		if top == "" {
			top = first
		} else if top != first {
			return "", fmt.Errorf("zip must contain exactly one top folder (%s, %s)", top, first)
		}
	}
	if top == "" {
		return "", errors.New("zip is empty")
	}
	return top, nil
}

func readZipFile(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, limit))
}

// header reads "Key: value" lines from the first comment block of a PHP/CSS file.
func header(b []byte, keys ...string) map[string]string {
	if len(b) > 8192 {
		b = b[:8192]
	}
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(sc.Text()), "*#/ "))
		for _, k := range keys {
			if _, ok := out[k]; ok {
				continue
			}
			if v, ok := strings.CutPrefix(line, k+":"); ok {
				out[k] = strings.TrimSpace(v)
			}
		}
	}
	return out
}

// InspectTheme validates a WordPress theme zip and reads its header and screenshot.
func InspectTheme(p string) (*ThemeInfo, *image, error) {
	z, err := zip.OpenReader(p)
	if err != nil {
		return nil, nil, fmt.Errorf("فایل قالب zip معتبر نیست: %w", err)
	}
	defer z.Close()
	top, err := topFolder(z.File)
	if err != nil {
		return nil, nil, fmt.Errorf("فایل قالب: %w", err)
	}
	if !ValidSlug(top) {
		return nil, nil, fmt.Errorf("نام پوشه‌ی قالب (%s) معتبر نیست", top)
	}
	var info *ThemeInfo
	var shot *image
	for _, f := range z.File {
		switch f.Name {
		case top + "/style.css":
			b, err := readZipFile(f, 64<<10)
			if err != nil {
				return nil, nil, err
			}
			h := header(b, "Theme Name", "Version", "Description")
			if h["Theme Name"] == "" {
				return nil, nil, errors.New("style.css قالب «Theme Name» ندارد")
			}
			info = &ThemeInfo{Slug: top, Name: h["Theme Name"], Version: h["Version"], Description: h["Description"]}
		case top + "/screenshot.png", top + "/screenshot.jpg", top + "/screenshot.webp":
			if f.UncompressedSize64 < 8<<20 {
				if b, err := readZipFile(f, 8<<20); err == nil {
					shot = &image{ext: path.Ext(f.Name), data: b}
				}
			}
		}
	}
	if info == nil {
		return nil, nil, errors.New("این zip قالب وردپرس نیست (style.css پیدا نشد)")
	}
	return info, shot, nil
}

// DemoInfo is read from a Saayeh demo package.
type DemoInfo struct {
	Theme, Version string
	Requires       []string // wordpress.org slugs
}

// InspectDemo validates a Saayeh demo package (manifest.json optional, demo.json required).
func InspectDemo(p string) (*DemoInfo, error) {
	z, err := zip.OpenReader(p)
	if err != nil {
		return nil, fmt.Errorf("بسته‌ی دمو zip معتبر نیست: %w", err)
	}
	defer z.Close()
	var demo, manifest *zip.File
	for _, f := range z.File {
		switch path.Base(f.Name) {
		case "demo.json":
			if demo == nil || len(f.Name) < len(demo.Name) {
				demo = f
			}
		case "manifest.json":
			if manifest == nil || len(f.Name) < len(manifest.Name) {
				manifest = f
			}
		}
	}
	if demo == nil {
		return nil, errors.New("بسته‌ی دمو demo.json ندارد (بسته‌ی درون‌ریز سایه نیست)")
	}
	info := &DemoInfo{}
	if manifest != nil {
		b, err := readZipFile(manifest, 1<<20)
		if err != nil {
			return nil, err
		}
		var m struct {
			Theme   string `json:"theme"`
			Version string `json:"version"`
		}
		if json.Unmarshal(b, &m) != nil {
			return nil, errors.New("manifest.json بسته‌ی دمو معتبر نیست")
		}
		info.Theme, info.Version = m.Theme, m.Version
	}
	b, err := readZipFile(demo, 64<<20)
	if err != nil {
		return nil, err
	}
	var d struct {
		Version  int      `json:"version"`
		Theme    string   `json:"theme"`
		Requires []string `json:"requires"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("demo.json معتبر نیست: %w", err)
	}
	if d.Version != 1 {
		return nil, fmt.Errorf("نسخه‌ی طرح demo.json (%d) پشتیبانی نمی‌شود", d.Version)
	}
	if info.Theme == "" {
		info.Theme = d.Theme
	}
	for _, r := range d.Requires {
		slug, _, _ := strings.Cut(strings.TrimSpace(r), "/")
		if ValidSlug(slug) {
			info.Requires = append(info.Requires, slug)
		}
	}
	return info, nil
}

// InspectPlugin validates a plugin zip and reads its header.
func InspectPlugin(p string) (*LibPlugin, error) {
	z, err := zip.OpenReader(p)
	if err != nil {
		return nil, fmt.Errorf("فایل افزونه zip معتبر نیست: %w", err)
	}
	defer z.Close()
	top, err := topFolder(z.File)
	if err != nil {
		return nil, fmt.Errorf("فایل افزونه: %w", err)
	}
	if !ValidSlug(top) {
		return nil, fmt.Errorf("نام پوشه‌ی افزونه (%s) معتبر نیست", top)
	}
	for _, f := range z.File {
		rest, ok := strings.CutPrefix(f.Name, top+"/")
		if !ok || strings.Contains(rest, "/") || !strings.HasSuffix(rest, ".php") {
			continue
		}
		b, err := readZipFile(f, 64<<10)
		if err != nil {
			continue
		}
		h := header(b, "Plugin Name", "Version")
		if h["Plugin Name"] != "" {
			return &LibPlugin{Slug: top, Name: h["Plugin Name"], Version: h["Version"]}, nil
		}
	}
	return nil, errors.New("این zip افزونه‌ی وردپرس نیست (سربرگ Plugin Name پیدا نشد)")
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
