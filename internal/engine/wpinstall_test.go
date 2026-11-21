package engine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/traptiterbeats/wpauto/internal/cpanel/cpaneltest"
	"github.com/traptiterbeats/wpauto/internal/wp"
)

// siteOpts changes how the fake helper behaves.
type siteOpts struct {
	failInstall bool
	entries     int         // entries in the fake zip (extract_chunk); default 700
	chunkFails  map[int]int // extract_chunk from → how many times it fails before working
	setup       func(*cpaneltest.Fake)
	calls       *[]string // helper actions in order
}

// fakeSite emulates the helper script and a freshly installed WordPress. Files the helper would
// write on the host are written into the fake cPanel's file tree.
func fakeSite(t *testing.T, fake *cpaneltest.Fake, o siteOpts) *httptest.Server {
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".php") && strings.Contains(r.URL.Path, "wpauto-") {
			// The helper must exist (written via cPanel) and the token must match.
			if !fake.Find(r.URL.Path, "'"+r.URL.Query().Get("t")+"'") {
				w.WriteHeader(404)
				return
			}
			helperPath, _ := fake.Path(r.URL.Path)
			docroot := path.Dir(helperPath)
			xdir := docroot + "/.wpauto-x"
			a := r.URL.Query().Get("a")
			mu.Lock()
			if o.calls != nil {
				*o.calls = append(*o.calls, a)
			}
			mu.Unlock()
			out := map[string]any{"ok": true}
			switch a {
			case "ping":
				out = map[string]any{"ok": true, "php": "8.3.20", "zip": true, "curl": true, "mysqli": true, "free_mb": 700, "memory_limit": "128M"}
			case "download":
				out = map[string]any{"ok": false, "error": "download failed: HTTP 0 blocked"}
			case "extract_chunk":
				from, _ := strconv.Atoi(r.URL.Query().Get("from"))
				n, _ := strconv.Atoi(r.URL.Query().Get("n"))
				mu.Lock()
				fail := o.chunkFails[from] > 0
				if fail {
					o.chunkFails[from]--
				}
				mu.Unlock()
				if fail {
					w.WriteHeader(500) // the host killed the request
					return
				}
				total := o.entries
				if total == 0 {
					total = 700
				}
				if from == 0 {
					fake.Delete(xdir)
				}
				end := min(total, from+n)
				for i := from; i < end; i++ {
					name := fmt.Sprintf("wordpress/wp-content/f%d.php", i)
					if i < len(cpaneltest.DefaultExtracted) {
						name = cpaneltest.DefaultExtracted[i]
					}
					fake.Put(xdir+"/"+name, "x")
				}
				out = map[string]any{"ok": true, "next": end, "total": total, "done": end >= total}
			case "place":
				src := xdir + "/wordpress"
				files := fake.Under(src)
				if len(files) == 0 {
					out = map[string]any{"ok": false, "error": "unexpected zip layout", "redo": true}
					break
				}
				top := map[string]bool{}
				for _, f := range files {
					name, _, _ := strings.Cut(strings.TrimPrefix(f, src+"/"), "/")
					top[name] = true
				}
				for name := range top {
					if len(fake.Under(docroot+"/"+name)) > 0 {
						out = map[string]any{"ok": false, "error": "files already exist: " + name}
					}
				}
				if out["ok"] == false {
					break
				}
				for name := range top {
					fake.Move(src+"/"+name, docroot+"/"+name)
				}
				fake.Delete(xdir)
				fake.Delete(docroot + "/.wpauto-wp.zip")
				out["moved"] = len(top)
			case "install":
				if o.failInstall {
					out = map[string]any{"ok": false, "error": "Error establishing a database connection"}
				} else {
					out["version"] = "6.9"
				}
			}
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		if r.Method == "HEAD" {
			return
		}
		_, _ = w.Write([]byte(`<html><link href="/wp-content/themes/x/style.css"></html>`))
	}))
}

func installInputs() map[string]string {
	return map[string]string{"mode": "subdomain", "domain": "example.com", "name": "test", "title": "سایت تست",
		"locale": "fa_IR", "admin_user": "boss", "admin_email": "a@example.com", "admin_pass": "a-very-long-pass-123"}
}

func runInstall(t *testing.T, failInstall bool, inputs map[string]string) (Run, *cpaneltest.Fake) {
	return runInstallWith(t, inputs, siteOpts{failInstall: failInstall})
}

func runInstallWith(t *testing.T, inputs map[string]string, o siteOpts) (Run, *cpaneltest.Fake) {
	t.Setenv("WPAUTO_DIR", t.TempDir())
	oldWaits := wp.ChunkRetryWaits
	wp.ChunkRetryWaits = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { wp.ChunkRetryWaits = oldWaits })
	fake := cpaneltest.New(t, "kfncspot", "secret")
	t.Cleanup(fake.Close)
	if o.setup != nil {
		o.setup(fake)
	}
	site := fakeSite(t, fake, o)
	t.Cleanup(site.Close)
	// The panel falls back to downloading the zip itself; serve a small fake zip.
	zip := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("Z", 2<<20)))
	}))
	t.Cleanup(zip.Close)
	zipURLOverride = zip.URL + "/latest-fa_IR.zip"
	t.Cleanup(func() { zipURLOverride = "" })

	r, _ := NewRunner(t.TempDir())
	list, err := LoadWorkflows("")
	if err != nil {
		t.Fatal(err)
	}
	wf, _ := FindWorkflow(list, "wp-install")
	rej := 0
	tg := target(t, fake.URL, "secret", &rej)
	tg.Profile.Quirks = map[string]string{"site_addr": strings.TrimPrefix(site.URL, "http://")}
	id, err := r.Start(wf, tg, inputs)
	if err != nil {
		t.Fatal(err)
	}
	return wait(t, r, id), fake
}

func runLogs(run Run) string {
	logs := ""
	for _, l := range run.Log {
		logs += l.Level + " " + l.Msg + "\n"
	}
	return logs
}

func TestWPInstallSubdomain(t *testing.T) {
	run, fake := runInstall(t, false, installInputs())
	logs := ""
	for _, l := range run.Log {
		logs += l.Msg + "\n"
	}
	if run.Status != StatusSuccess {
		t.Fatalf("status %s: %s\n%s", run.Status, run.Error, logs)
	}
	for _, c := range []string{"SubDomain/addsubdomain", "LangPHP/php_set_vhost_versions", "Mysql/create_database",
		"Mysql/set_privileges_on_database", "Fileman/upload_files"} {
		if !fake.Called(c) {
			t.Errorf("%s not called", c)
		}
	}
	cfg, ok := fake.File("/home/kfncspot/test.example.com/wp-config.php")
	if !ok || !strings.Contains(cfg, "kfncspot_wp") || !strings.Contains(cfg, "AUTH_KEY") {
		t.Fatalf("wp-config not written properly:\n%s", cfg)
	}
	if ht, _ := fake.File("/home/kfncspot/test.example.com/.htaccess"); !strings.Contains(ht, "RewriteBase /\n") {
		t.Fatalf("htaccess: %q", ht)
	}
	if fake.Called("Mysql/delete_database") {
		t.Fatal("rollback ran on success")
	}
	for _, l := range run.Log {
		if strings.Contains(l.Msg, "a-very-long-pass-123") {
			t.Fatal("admin password leaked into the log")
		}
	}
	if run.Inputs["admin_pass"] != "••••••" {
		t.Fatal("password not masked in run record")
	}
	if !strings.Contains(logs, "http://test.example.com/wp-admin/") {
		t.Fatalf("admin url not reported:\n%s", logs)
	}
}

func TestWPInstallRollsBackOnFailure(t *testing.T) {
	run, fake := runInstall(t, true, installInputs())
	if run.Status != StatusFailed {
		t.Fatalf("status %s", run.Status)
	}
	for _, c := range []string{"Mysql/delete_user", "Mysql/delete_database", "api2:SubDomain/delsubdomain"} {
		if !fake.Called(c) {
			t.Errorf("rollback %s not called", c)
		}
	}
}

func TestWPInstallRefusesExistingSite(t *testing.T) {
	in := installInputs()
	in["mode"], in["name"] = "subfolder", "blog"
	run, fake := runInstall(t, false, in)
	_ = run
	// example.com/blog does not exist in the fake → allowed; now try the subdomain that exists.
	in = installInputs()
	in["name"] = "blog"
	run, fake = runInstall(t, false, in)
	if run.Status != StatusFailed || !strings.Contains(run.Error, "وجود دارد") {
		t.Fatalf("expected refusal, got %s: %s", run.Status, run.Error)
	}
	if fake.Called("SubDomain/addsubdomain") || fake.Called("Mysql/create_database") {
		t.Fatal("changes made despite refusal")
	}
}

func TestWPInstallOnDomainRoot(t *testing.T) {
	// the main domain already has WordPress files in its folder → refused, nothing changed
	in := installInputs()
	in["mode"], in["name"] = "root", ""
	run, fake := runInstall(t, false, in)
	if run.Status != StatusFailed || !strings.Contains(run.Error, "سایت دیگر") {
		t.Fatalf("expected refusal on occupied root, got %s: %s", run.Status, run.Error)
	}
	if fake.Called("Mysql/create_database") {
		t.Fatal("changes made despite refusal")
	}
	// blog.example.com's folder only has a start page → WordPress goes in, the page is set aside
	in["domain"] = "blog.example.com"
	run, fake = runInstall(t, false, in)
	logs := ""
	for _, l := range run.Log {
		logs += l.Msg + "\n"
	}
	if run.Status != StatusSuccess {
		t.Fatalf("status %s: %s\n%s", run.Status, run.Error, logs)
	}
	if fake.Called("SubDomain/addsubdomain") || !fake.Called("api2:Fileman/fileop") {
		t.Fatal("root install created a subdomain or did not set index.html aside")
	}
	if _, ok := fake.File("/home/kfncspot/blog/wp-config.php"); !ok {
		t.Fatal("wp-config not written in the domain's folder")
	}
	if !strings.Contains(logs, "index.html.wpauto-bak") {
		t.Fatalf("start page not set aside:\n%s", logs)
	}
}

func TestWPInstallEmptyNameMeansRoot(t *testing.T) {
	// location left on «ساب‌دامین» but no name → installs on the domain itself
	in := installInputs()
	in["mode"], in["name"], in["domain"] = "subdomain", "", "blog.example.com"
	run, fake := runInstall(t, false, in)
	if run.Status != StatusSuccess {
		t.Fatalf("status %s: %s", run.Status, run.Error)
	}
	if fake.Called("SubDomain/addsubdomain") {
		t.Fatal("created a subdomain for an empty name")
	}
	if _, ok := fake.File("/home/kfncspot/blog/wp-config.php"); !ok {
		t.Fatal("wp-config not written in the domain's folder")
	}
}
