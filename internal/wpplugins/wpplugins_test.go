package wpplugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltinCatalog(t *testing.T) {
	list, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"woocommerce", "persian-woocommerce", "contact-form-7", "wp-parsidate", "wordpress-seo",
		"litespeed-cache", "updraftplus", "wordfence", "wp-mail-smtp", "classic-editor"}
	if len(list) != len(want) {
		t.Fatalf("%d plugins", len(list))
	}
	for i, s := range want {
		if list[i].Slug != s || list[i].Desc == "" || list[i].Name == "" || list[i].Icon == "" {
			t.Errorf("entry %d: %+v", i, list[i])
		}
	}
	if p, _ := Find(list, "litespeed-cache"); !strings.Contains(p.Desc, "LiteSpeed") {
		t.Errorf("litespeed note missing: %q", p.Desc)
	}
}

func TestOverlay(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, OverlayFile), []byte("plugins:\n  - slug: akismet\n    desc: ضد اسپم\n  - slug: classic-editor\n    hidden: true\n  - slug: wordfence\n    name: WF\n    desc: x\n"), 0o600)
	list, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := Find(list, "classic-editor"); ok {
		t.Error("hidden entry still listed")
	}
	if p, ok := Find(list, "akismet"); !ok || p.Name != "akismet" || p.Icon == "" {
		t.Errorf("added entry: %+v", p)
	}
	if p, _ := Find(list, "wordfence"); p.Name != "WF" {
		t.Errorf("replaced entry: %+v", p)
	}
	_ = os.WriteFile(filepath.Join(dir, OverlayFile), []byte("plugins:\n  - slug: ../bad\n"), 0o600)
	if _, err := Load(dir); err == nil {
		t.Error("bad slug accepted")
	}
}
