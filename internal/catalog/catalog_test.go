package catalog_test

import (
	"strings"
	"testing"

	"github.com/traptiterbeats/wpauto/internal/catalog"
	"github.com/traptiterbeats/wpauto/internal/catalog/catalogtest"
)

func TestAddTheme(t *testing.T) {
	src := t.TempDir()
	c := catalog.Open(t.TempDir())
	theme := catalogtest.Theme(t, src, "mytheme")
	demo := catalogtest.Demo(t, src, "mytheme", "woocommerce")

	if _, err := c.Add(catalog.AddInput{ThemeZip: theme, DemoZip: demo}); err == nil || !strings.Contains(err.Error(), "درون‌ریز") {
		t.Fatalf("demo without importer in the library must fail, got %v", err)
	}
	if _, err := c.AddPlugin(catalogtest.Plugin(t, src, catalog.ImporterSlug, "")); err != nil {
		t.Fatal(err)
	}
	got, err := c.Add(catalog.AddInput{ThemeZip: theme, DemoZip: demo, WPOrg: []string{"contact-form-7"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Test mytheme" || got.Version != "1.2.3" || !got.HasDemo() || got.Screenshot != "screenshot.png" {
		t.Fatalf("unexpected theme: %+v", got)
	}
	var slugs []string
	for _, p := range got.Plugins {
		slugs = append(slugs, p.Slug+":"+p.Source)
	}
	if strings.Join(slugs, ",") != "woocommerce:,saayeh-demo-importer:library,contact-form-7:wporg" {
		t.Fatalf("plugins: %v", slugs)
	}
	again, err := c.Get("mytheme")
	if err != nil || again.DemoVersion != "1.0.0" {
		t.Fatalf("get: %+v %v", again, err)
	}
	if len(c.List()) != 1 || len(c.Library()) != 1 {
		t.Fatal("list/library")
	}
	if _, err := c.Add(catalog.AddInput{ThemeZip: theme, DemoZip: catalogtest.Demo(t, src, "other")}); err == nil {
		t.Fatal("demo for another theme must fail")
	}
	bad := catalogtest.Zip(t, src, "bad.zip", map[string]string{"a/style.css": "x", "b/x.php": "y"})
	if _, err := c.Add(catalog.AddInput{ThemeZip: bad}); err == nil {
		t.Fatal("two top folders must fail")
	}
	evil := catalogtest.Zip(t, src, "evil.zip", map[string]string{"../x/style.css": "Theme Name: x"})
	if _, err := c.Add(catalog.AddInput{ThemeZip: evil}); err == nil {
		t.Fatal("path traversal must fail")
	}
	if err := c.Delete("mytheme"); err != nil || len(c.List()) != 0 {
		t.Fatal("delete")
	}
}
