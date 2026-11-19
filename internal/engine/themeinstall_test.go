package engine

import (
	"archive/zip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/traptiterbeats/wpauto/internal/catalog"
	"github.com/traptiterbeats/wpauto/internal/catalog/catalogtest"
	"github.com/traptiterbeats/wpauto/internal/cpanel/cpaneltest"
)

// fakeWP emulates the helper on an existing WordPress and records the actions it receives.
type fakeWP struct {
	mu      sync.Mutex
	actions []string
	steps   int
	failAt  string // action that fails
}

func (f *fakeWP) seen() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.actions, " ")
}

func (f *fakeWP) server(t *testing.T, fake *cpaneltest.Fake) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "wpauto-") {
			if r.Method != "HEAD" {
				_, _ = w.Write([]byte(`<html><link href="/wp-content/themes/mytheme/style.css"></html>`))
			}
			return
		}
		if !fake.Find(r.URL.Path, "'"+r.URL.Query().Get("t")+"'") {
			w.WriteHeader(404)
			return
		}
		q := r.URL.Query()
		a := q.Get("a")
		f.mu.Lock()
		rec := a
		if s := q.Get("slug"); s != "" {
			rec += ":" + s
		}
		if q.Get("delete") != "" {
			rec += ":delete"
		}
		f.actions = append(f.actions, rec)
		f.mu.Unlock()
		out := map[string]any{"ok": true}
		switch a {
		case "wp_info":
			out = map[string]any{"ok": true, "version": "6.8.3", "url": "http://example.com/", "stylesheet": "twentytwentyfive",
				"themes": map[string]any{"twentytwentyfive": "1.0"}, "php": "8.3",
				"plugins": map[string]any{"akismet": map[string]any{"version": "5", "active": false}}}
		case "plugin_install", "theme_install":
			out["created"] = true
		case "plugin_activate":
			out["already"] = false
		case "theme_activate":
			out["previous"] = "twentytwentyfive"
		case "demo_prepare":
			out["version"] = "1.0.0"
		case "demo_start":
			out["state"] = map[string]any{"done": false, "label": "تصاویر", "percent": 0}
		case "demo_step":
			f.mu.Lock()
			f.steps++
			n := f.steps
			f.mu.Unlock()
			out["state"] = map[string]any{"done": n >= 3, "label": "برگه‌ها", "percent": n * 30,
				"log": []any{map[string]any{"level": "info", "message": "step " + string(rune('0'+n))}}}
		case "demo_remove":
			out["state"] = map[string]any{"done": true}
		}
		if a == f.failAt {
			out = map[string]any{"ok": false, "error": "boom"}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
}

func runThemeInstall(t *testing.T, failAt string, prep ...func(inputs map[string]string)) (Run, *fakeWP, *cpaneltest.Fake) {
	t.Setenv("WPAUTO_DIR", t.TempDir())
	src := t.TempDir()
	cat := Catalog()
	if _, err := cat.AddPlugin(catalogtest.Plugin(t, src, catalog.ImporterSlug, "")); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.Add(catalog.AddInput{ThemeZip: catalogtest.Theme(t, src, "mytheme"), DemoZip: catalogtest.Demo(t, src, "mytheme")}); err != nil {
		t.Fatal(err)
	}
	demoPause = 0
	fake := cpaneltest.New(t, "kfncspot", "secret")
	t.Cleanup(fake.Close)
	wpf := &fakeWP{failAt: failAt}
	site := wpf.server(t, fake)
	t.Cleanup(site.Close)
	r, _ := NewRunner(t.TempDir())
	list, err := LoadWorkflows("")
	if err != nil {
		t.Fatal(err)
	}
	wf, _ := FindWorkflow(list, "theme-install")
	rej := 0
	tg := target(t, fake.URL, "secret", &rej)
	tg.Profile.Quirks = map[string]string{"site_addr": strings.TrimPrefix(site.URL, "http://")}
	inputs := map[string]string{"domain": "example.com", "theme": "mytheme", "activate": "true", "import_demo": "true"}
	for _, p := range prep {
		p(inputs)
	}
	id, err := r.Start(wf, tg, inputs)
	if err != nil {
		t.Fatal(err)
	}
	return wait(t, r, id), wpf, fake
}

func TestThemeInstall(t *testing.T) {
	run, wpf, fake := runThemeInstall(t, "")
	if run.Status != StatusSuccess {
		var logs []string
		for _, l := range run.Log {
			logs = append(logs, l.Msg)
		}
		t.Fatalf("status %s: %s\n%s", run.Status, run.Error, strings.Join(logs, "\n"))
	}
	want := "ping wp_info plugin_install:saayeh-demo-importer plugin_activate:saayeh-demo-importer theme_install:mytheme " +
		"theme_activate:mytheme demo_prepare:mytheme demo_start:mytheme demo_step demo_step demo_step cleanup"
	if got := wpf.seen(); got != want {
		t.Fatalf("actions:\n got %s\nwant %s", got, want)
	}
	for _, f := range []string{"saayeh-demo-importer.zip", "mytheme-theme.zip", "mytheme-demo.zip"} {
		if !fake.Find("/"+f, "") {
			t.Errorf("%s not uploaded", f)
		}
	}
}

func TestThemeInstallRollsBack(t *testing.T) {
	run, wpf, _ := runThemeInstall(t, "demo_step")
	if run.Status != StatusFailed {
		t.Fatalf("status %s", run.Status)
	}
	got := wpf.seen()
	want := "demo_step demo_cancel demo_remove:mytheme theme_activate:twentytwentyfive theme_remove:mytheme plugin_remove:saayeh-demo-importer:delete cleanup"
	if !strings.HasSuffix(got, want) {
		t.Fatalf("rollback actions:\n got %s\nwant …%s", got, want)
	}
}

func TestThemeInstallWithDraft(t *testing.T) {
	run, _, fake := runThemeInstall(t, "", func(in map[string]string) {
		d, err := Drafts().New("mytheme", "Client")
		if err != nil {
			t.Fatal(err)
		}
		d.Values["options.blogdescription"] = "CLIENT TAGLINE"
		d.Values["options.blogname"] = "Client Site"
		if err := Drafts().Save(d); err != nil {
			t.Fatal(err)
		}
		in["draft"] = d.ID
	})
	if run.Status != StatusSuccess {
		t.Fatalf("status %s: %s", run.Status, run.Error)
	}
	var data string
	for p, c := range fake.Files {
		if strings.HasSuffix(p, "/mytheme-demo.zip") {
			data = c
		}
	}
	zr, err := zip.NewReader(strings.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Name == "demo.json" {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			if !strings.Contains(string(b), `"blogname": "Client"`) {
				t.Fatalf("uploaded demo is not the customised package: %s", b)
			}
			return
		}
	}
	t.Fatal("demo.json missing")
}
