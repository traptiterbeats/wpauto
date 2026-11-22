package engine

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/traptiterbeats/wpauto/internal/cpanel/cpaneltest"
)

// pluginZip builds a small wordpress.org-style plugin zip (slug/slug.php).
func pluginZip(slug string) []byte {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	w, _ := z.Create(slug + "/" + slug + ".php")
	_, _ = w.Write([]byte("<?php /* Plugin Name: " + slug + " */"))
	_ = z.Close()
	return b.Bytes()
}

// fakeWPOrg serves plugin zips like downloads.wordpress.org and counts downloads.
func fakeWPOrg(t *testing.T) *int32 {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		slug := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/plugin/"), ".latest-stable.zip")
		_, _ = w.Write(pluginZip(slug))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(SetWPOrgZipURL(func(slug string) string { return srv.URL + "/plugin/" + slug + ".latest-stable.zip" }))
	return &n
}

func runPlugins(t *testing.T, wfID string, wpf *fakeWP, inputs map[string]string) (Run, *cpaneltest.Fake) {
	t.Helper()
	t.Setenv("WPAUTO_DIR", t.TempDir())
	fake := cpaneltest.New(t, "kfncspot", "secret")
	t.Cleanup(fake.Close)
	site := wpf.server(t, fake)
	t.Cleanup(site.Close)
	r, _ := NewRunner(t.TempDir())
	list, err := LoadWorkflows("")
	if err != nil {
		t.Fatal(err)
	}
	wf, ok := FindWorkflow(list, wfID)
	if !ok {
		t.Fatalf("workflow %s missing", wfID)
	}
	rej := 0
	tg := target(t, fake.URL, "secret", &rej)
	tg.Profile.Quirks = map[string]string{"site_addr": strings.TrimPrefix(site.URL, "http://")}
	id, err := r.Start(wf, tg, inputs)
	if err != nil {
		t.Fatal(err)
	}
	return wait(t, r, id), fake
}

func stepOut(run Run, step, key string) string {
	for _, s := range run.Steps {
		if s.ID == step {
			return s.Outputs[key]
		}
	}
	return ""
}

func runLog(run Run) string {
	var l []string
	for _, x := range run.Log {
		l = append(l, x.Msg)
	}
	return strings.Join(l, "\n")
}

func TestPluginsInstallAndActivate(t *testing.T) {
	downloads := fakeWPOrg(t)
	wpf := &fakeWP{plugins: map[string]any{
		"wordfence":      map[string]any{"version": "8", "active": false}, // installed, inactive
		"classic-editor": map[string]any{"version": "1.6", "active": true},
	}}
	run, fake := runPlugins(t, "plugins-install", wpf, map[string]string{"domain": "example.com", "plugins": "woocommerce,wordfence,classic-editor"})
	if run.Status != StatusSuccess {
		t.Fatalf("status %s: %s\n%s", run.Status, run.Error, runLog(run))
	}
	want := "ping wp_info plugin_install:woocommerce plugin_activate:woocommerce plugin_activate:wordfence cleanup"
	if got := wpf.seen(); got != want {
		t.Fatalf("actions:\n got %s\nwant %s", got, want)
	}
	if !fake.Find("/woocommerce.zip", "") || fake.Find("/wordfence.zip", "") {
		t.Error("only the missing plugin is uploaded")
	}
	if *downloads != 1 {
		t.Errorf("%d downloads, want 1", *downloads)
	}
	var st map[string]string
	_ = json.Unmarshal([]byte(stepOut(run, "ensure", "status")), &st)
	if st["woocommerce"] != "active" || st["wordfence"] != "active" || st["classic-editor"] != "active" || st["updraftplus"] != "missing" {
		t.Fatalf("states: %v", st)
	}
	if !strings.Contains(runLog(run), "فایل‌هایش دست نمی‌خورد") {
		t.Error("existing plugin note missing from the log")
	}
}

func TestPluginsInstallRollsBack(t *testing.T) {
	fakeWPOrg(t)
	wpf := &fakeWP{failAt: "plugin_activate", plugins: map[string]any{}}
	run, _ := runPlugins(t, "plugins-install", wpf, map[string]string{"domain": "example.com", "plugins": "contact-form-7"})
	if run.Status != StatusFailed {
		t.Fatalf("status %s", run.Status)
	}
	if got, want := wpf.seen(), "plugin_install:contact-form-7 plugin_activate:contact-form-7 plugin_remove:contact-form-7:delete cleanup"; !strings.HasSuffix(got, want) {
		t.Fatalf("rollback:\n got %s\nwant …%s", got, want)
	}
}

func TestPluginsInstallRejectsUnknown(t *testing.T) {
	fakeWPOrg(t)
	wpf := &fakeWP{plugins: map[string]any{}}
	run, _ := runPlugins(t, "plugins-install", wpf, map[string]string{"domain": "example.com", "plugins": "evil-plugin"})
	if run.Status != StatusFailed || strings.Contains(wpf.seen(), "plugin_install") {
		t.Fatalf("status %s actions %s", run.Status, wpf.seen())
	}
}

func TestPluginsStatus(t *testing.T) {
	wpf := &fakeWP{plugins: map[string]any{"woocommerce": map[string]any{"version": "9", "active": true},
		"wordpress-seo": map[string]any{"version": "25", "active": false}}}
	run, _ := runPlugins(t, "plugins-status", wpf, map[string]string{"domain": "example.com"})
	if run.Status != StatusSuccess {
		t.Fatalf("status %s: %s", run.Status, run.Error)
	}
	if got := wpf.seen(); got != "ping wp_info cleanup" {
		t.Fatalf("status check is not read-only: %s", got)
	}
	var st map[string]string
	_ = json.Unmarshal([]byte(stepOut(run, "status", "status")), &st)
	if st["woocommerce"] != "active" || st["wordpress-seo"] != "inactive" || st["wp-mail-smtp"] != "missing" || len(st) != 10 {
		t.Fatalf("states: %v", st)
	}
}
