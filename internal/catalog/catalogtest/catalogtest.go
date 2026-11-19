// Package catalogtest builds small theme, plugin and demo zips for tests.
package catalogtest

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

// Zip writes files (name → content) into dir/name.zip and returns its path.
func Zip(t testing.TB, dir, name string, files map[string]string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for n, c := range files {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(c)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return p
}

// Theme returns a minimal valid theme zip for slug.
func Theme(t testing.TB, dir, slug string) string {
	return Zip(t, dir, slug+".zip", map[string]string{
		slug + "/style.css": "/*\nTheme Name: Test " + slug + "\nVersion: 1.2.3\nDescription: test theme\n*/\n",
		slug + "/index.php": "<?php\n?><html><head><?php wp_head(); ?></head><body><link rel=\"x\" href=\"<?php echo esc_url(get_stylesheet_directory_uri()); ?>/style.css\">ok</body></html>\n",
		slug + "/screenshot.png": "\x89PNG fake",
	})
}

// Plugin returns a plugin zip with the given PHP body.
func Plugin(t testing.TB, dir, slug, body string) string {
	return Zip(t, dir, slug+".zip", map[string]string{
		slug + "/" + slug + ".php": "<?php\n/**\n * Plugin Name: Test " + slug + "\n * Version: 0.9\n */\n" + body,
	})
}

// Demo returns a Saayeh demo package for theme.
func Demo(t testing.TB, dir, theme string, requires ...string) string {
	req := "[]"
	if len(requires) > 0 {
		req = `["` + requires[0] + `"]`
	}
	return Zip(t, dir, theme+"-demo.zip", map[string]string{
		"manifest.json": `{"theme":"` + theme + `","version":"1.0.0","schema":1,"demo":"demo.json"}`,
		"demo.json":     `{"version":1,"theme":"` + theme + `","requires":` + req + `,"pages":[{"slug":"home","title":"Home"}]}`,
	})
}
