// Package catalogtest builds small theme, plugin and demo zips for tests.
package catalogtest

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
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
		req = `["` + strings.Join(requires, `","`) + `"]`
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
 {"path":"theme_mods.contact_email","label":"Email","role":"contact.email"},
 {"path":"pages.9.title","label":"missing"}],
 "logo":{"theme_mod":"brand_logo","label":"Logo"}}`
	}
	return Zip(t, dir, theme+"-rich-demo.zip", files)
}

// RichDemoV2 returns a demo package with a contract v2 customize.json: sized image slots of every
// standard ratio (two of them product photos) and two products.
func RichDemoV2(t testing.TB, dir, theme string) string {
	demo := `{"version":1,"theme":"` + theme + `","requires":[],
 "images":[{"id":"hero","file":"images/hero.jpg"},{"id":"about","file":"images/about.jpg"},{"id":"team","file":"images/team.jpg"},
  {"id":"side","file":"images/side.jpg"},{"id":"p-mug","file":"images/p-mug.jpg"},{"id":"p-tee","file":"images/p-tee.jpg"},{"id":"unlisted","file":"images/x.jpg"}],
 "pages":[{"slug":"home","title":"Home","content":"<p>We build apps.</p>"}],
 "products":[{"slug":"mug","name":"Code Mug","short_description":"A mug for coders.","description":"<p>Ceramic mug.</p>","regular_price":"19","sku":"MUG-1"},
  {"slug":"tee","name":"Dev Tee","short_description":"A shirt.","description":"<p>Cotton tee.</p>","regular_price":"25","sku":"TEE-1"}],
 "theme_mods":{"hero_title":"Software that ships","hero_sub":"We write code.","about_text":"A software studio.","svc":"Web apps","contact_title":"Say hi"},
 "options":{"blogname":"Demo Studio"}}`
	spec := `{"contract":"wpauto-customize","version":2,"theme":"` + theme + `",
 "products":{"post_type":"product","default_mode":"keep"},
 "images":[
  {"id":"hero","label":"Hero","ratio":"16:9","width":160,"height":90,"priority":1},
  {"id":"about","label":"About","ratio":"4:3","width":120,"height":90,"priority":2},
  {"id":"team","label":"Team","ratio":"1:1","width":80,"height":80,"priority":3},
  {"id":"side","label":"Side","ratio":"3:4","width":90,"height":120,"priority":4},
  {"id":"p-mug","label":"Mug photo","ratio":"1:1","width":80,"height":80,"priority":5,"group":"products"},
  {"id":"p-tee","label":"Tee photo","ratio":"1:1","width":80,"height":80,"priority":6,"group":"products"}],
 "fields":[
  {"path":"options.blogname","label":"Site","role":"site.name"},
  {"path":"theme_mods.hero_title","label":"Hero","role":"hero.title"},
  {"path":"theme_mods.hero_sub","label":"Sub","role":"hero.subtitle"},
  {"path":"theme_mods.about_text","label":"About","role":"about.text"},
  {"path":"theme_mods.svc","label":"Service","role":"services.item.title"},
  {"path":"theme_mods.contact_title","label":"Contact","role":"contact.title"},
  {"path":"products[slug=mug].name","label":"Mug","role":"product.item.title"},
  {"path":"products[slug=mug].short_description","label":"Mug short","role":"product.item.short"},
  {"path":"products[slug=mug].description","label":"Mug text","role":"product.item.description","kind":"html"},
  {"path":"products[slug=mug].regular_price","label":"Mug price","role":"product.item.price"},
  {"path":"products[slug=tee].name","label":"Tee","role":"product.item.title"},
  {"path":"products[slug=tee].sku","label":"Tee SKU","role":"product.item.sku"}]}`
	files := map[string]string{
		"manifest.json":  `{"theme":"` + theme + `","version":"1.0.0","schema":1,"demo":"demo.json"}`,
		"demo.json":      demo,
		"customize.json": spec,
	}
	for _, n := range []string{"hero", "about", "team", "side", "p-mug", "p-tee", "x"} {
		files["images/"+n+".jpg"] = "\xff\xd8\xff\xe0 demo " + n
	}
	return Zip(t, dir, theme+"-v2-demo.zip", files)
}
