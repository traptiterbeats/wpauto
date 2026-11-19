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
		slug + "/style.css":      "/*\nTheme Name: Test " + slug + "\nVersion: 1.2.3\nDescription: test theme\n*/\n",
		slug + "/index.php":      "<?php\n?><html><head><?php wp_head(); ?></head><body><link rel=\"x\" href=\"<?php echo esc_url(get_stylesheet_directory_uri()); ?>/style.css\">ok</body></html>\n",
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

// RichDemo returns a demo package with pages, posts, a menu, theme mods, options and one image.
func RichDemo(t testing.TB, dir, theme string, withSpec bool) string {
	demo := `{"version":1,"theme":"` + theme + `","requires":[],
 "images":[{"id":"hero","file":"images/hero.png","title":"Hero"}],
 "pages":[{"slug":"home","title":"Home","role":"front","content":"<h2>Welcome</h2><p>We bake bread.</p>"}],
 "posts":[{"post_type":"project","slug":"first","title":"First project","excerpt":"A short summary of the work.","content":"<p>Long text about the project.</p>","meta":{"chip":"Design / Web","visual":"v1"}}],
 "menus":[{"location":"primary","name":"Main","items":[{"type":"custom","title":"About us","url":"/#about"}]}],
 "theme_mods":{"brand_text":"ACME STUDIO","contact_email":"hello@acme.test","accent":"#ff0000","count":4},
 "options":{"blogdescription":"Ideas into products.","posts_per_page":8}}`
	files := map[string]string{
		"manifest.json":   `{"theme":"` + theme + `","version":"1.0.0","schema":1,"demo":"demo.json"}`,
		"demo.json":       demo,
		"images/hero.png": "\x89PNG\r\n\x1a\nfake",
	}
	if withSpec {
		files["customize.json"] = `{"version":1,"fields":[
 {"path":"theme_mods.brand_text","label":"Brand","kind":"short"},
 {"path":"posts[slug=first].title","label":"Project title"},
 {"path":"theme_mods.contact_email","label":"Email","from":"email"},
 {"path":"pages.9.title","label":"missing"}],
 "logo":{"theme_mod":"brand_logo","label":"Logo"}}`
	}
	return Zip(t, dir, theme+"-rich-demo.zip", files)
}
