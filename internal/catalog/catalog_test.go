package catalog_test

import (
	"os"
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
	if strings.Join(slugs, ",") != "woocommerce:wporg,saayeh-demo-importer:library,contact-form-7:wporg" {
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

// A demo require whose zip is in the library (a companion plugin like a theme's core) is
// installed from the library; others come from wordpress.org. The demo's order is kept.
func TestAddDemoRequiresSource(t *testing.T) {
	src := t.TempDir()
	c := catalog.Open(t.TempDir())
	theme := catalogtest.Theme(t, src, "shop")
	demo := catalogtest.Demo(t, src, "shop", "woocommerce", "shop-core")
	core := catalogtest.Plugin(t, src, "shop-core", "")
	imp := catalogtest.Plugin(t, src, catalog.ImporterSlug, "")

	// invalid zip in the batch: nothing is stored
	bad := catalogtest.Zip(t, src, "bad.zip", map[string]string{"x/readme.txt": "no header"})
	if _, _, err := c.AddPlugins([]string{core, bad}); err == nil || len(c.Library()) != 0 {
		t.Fatalf("invalid batch must store nothing: %v %v", err, c.Library())
	}

	plugins := func(th *catalog.Theme) string {
		var out []string
		for _, p := range th.Plugins {
			out = append(out, p.Slug+":"+p.Source)
		}
		return strings.Join(out, ",")
	}
	if _, _, err := c.AddPlugins([]string{imp}); err != nil {
		t.Fatal(err)
	}
	got, err := c.Add(catalog.AddInput{ThemeZip: theme, DemoZip: demo})
	if err != nil {
		t.Fatal(err)
	}
	if p := plugins(got); p != "woocommerce:wporg,shop-core:wporg,saayeh-demo-importer:library" {
		t.Fatalf("without core in the library: %s", p)
	}

	infos, undo, err := c.AddPlugins([]string{core})
	if err != nil || len(infos) != 1 || infos[0].Slug != "shop-core" {
		t.Fatalf("add core: %v %v", infos, err)
	}
	got, err = c.Add(catalog.AddInput{ThemeZip: theme, DemoZip: demo, Companions: []string{"shop-core", catalog.ImporterSlug}})
	if err != nil {
		t.Fatal(err)
	}
	if p := plugins(got); p != "woocommerce:wporg,shop-core:library,saayeh-demo-importer:library" {
		t.Fatalf("with core in the library: %s", p)
	}
	// undo removes a zip that was not in the library before, keeps one that was
	undo()
	if c.LibraryPath("shop-core") != "" || c.LibraryPath(catalog.ImporterSlug) == "" {
		t.Fatal("undo")
	}
	// a companion without a demo is still listed (from the library)
	_, _, _ = c.AddPlugins([]string{core})
	got, err = c.Add(catalog.AddInput{ThemeZip: theme, Companions: []string{"shop-core", catalog.ImporterSlug}})
	if err != nil || plugins(got) != "shop-core:library" {
		t.Fatalf("companion without demo: %v %v", got, err)
	}
	// undo of a replaced zip puts the old one back
	old, _ := os.ReadFile(c.LibraryPath("shop-core"))
	core2 := catalogtest.Plugin(t, t.TempDir(), "shop-core", "// v2")
	_, undo, err = c.AddPlugins([]string{core2})
	if err != nil {
		t.Fatal(err)
	}
	undo()
	if now, _ := os.ReadFile(c.LibraryPath("shop-core")); string(now) != string(old) {
		t.Fatal("undo must restore the replaced zip")
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.3.0", "1.2.0", 1}, {"1.2.0", "1.3.0", -1}, {"1.3", "1.3.0", 0}, {"1.10.0", "1.9.9", 1},
		{"2", "1.99", 1}, {"1.3.0-beta", "1.3.0", -1}, {"1.3.0", "1.3.0-rc1", 1}, {"v1.3.0", "1.3.0", 0},
		{"1.3.0-beta", "1.3.0-rc", -1},
	}
	for _, c := range cases {
		if got := catalog.CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("%s vs %s = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	if catalog.ValidVersion("") || catalog.ValidVersion("abc") || !catalog.ValidVersion("1.0") {
		t.Error("ValidVersion")
	}
}

func TestInspectDemoMinImporter(t *testing.T) {
	di, err := catalog.InspectDemo(catalogtest.DemoMinImporter(t, t.TempDir(), "mytheme", "1.3.0"))
	if err != nil || di.MinImporter != "1.3.0" {
		t.Fatalf("%+v %v", di, err)
	}
}
