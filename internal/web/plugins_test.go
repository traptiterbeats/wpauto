package web

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/traptiterbeats/wpauto/internal/cpanel/cpaneltest"
	"github.com/traptiterbeats/wpauto/internal/engine"
	"github.com/traptiterbeats/wpauto/internal/projects"
	"github.com/traptiterbeats/wpauto/internal/store"
)

// plugSite emulates the temporary helper on the project's WordPress (wp_info + plugin actions).
type plugSite struct {
	mu      sync.Mutex
	actions []string
	plugins map[string]any
}

func (f *plugSite) seen() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.actions, " ")
}

func (f *plugSite) server(fake *cpaneltest.Fake) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "wpauto-") {
			return
		}
		if !fake.Find(r.URL.Path, "'"+r.URL.Query().Get("t")+"'") {
			w.WriteHeader(404)
			return
		}
		a := r.URL.Query().Get("a")
		f.mu.Lock()
		rec := a
		if s := r.URL.Query().Get("slug"); s != "" {
			rec += ":" + s
		}
		f.actions = append(f.actions, rec)
		f.mu.Unlock()
		out := map[string]any{"ok": true}
		switch a {
		case "wp_info":
			out = map[string]any{"ok": true, "version": "6.9", "url": "http://example.com/", "stylesheet": "twentytwentyfive",
				"themes": map[string]any{}, "php": "8.3", "plugins": f.plugins}
		case "plugin_install":
			out["created"] = true
		case "plugin_activate":
			out["already"] = false
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
}

type plugEnv struct {
	*homeEnv
	fake *cpaneltest.Fake
	site *plugSite
	p    projects.Project
	h    store.Host
}

// newPlugEnv is a project at example.com (WordPress on the fake cPanel) with its own host,
// a fake site and a fake downloads.wordpress.org. Nothing leaves the test.
func newPlugEnv(t *testing.T, plugins map[string]any) *plugEnv {
	t.Helper()
	e := newBareEnv(t)
	fake := cpaneltest.New(t, "kfncspot", "secret")
	t.Cleanup(fake.Close)
	ps := &plugSite{plugins: plugins}
	site := ps.server(fake)
	t.Cleanup(site.Close)
	org := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slug := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/plugin/"), ".latest-stable.zip")
		var b bytes.Buffer
		z := zip.NewWriter(&b)
		fw, _ := z.Create(slug + "/" + slug + ".php")
		_, _ = fw.Write([]byte("<?php"))
		_ = z.Close()
		_, _ = w.Write(b.Bytes())
	}))
	t.Cleanup(org.Close)
	t.Cleanup(engine.SetWPOrgZipURL(func(slug string) string { return org.URL + "/plugin/" + slug + ".latest-stable.zip" }))
	prof := "id: fake\nname: fake\ninsecure_tls: true\nquirks:\n  site_addr: " + strings.TrimPrefix(site.URL, "http://") + "\n"
	_ = os.WriteFile(filepath.Join(os.Getenv("WPAUTO_DIR"), "profiles", "fake.yaml"), []byte(prof), 0o600)
	_, p := e.create("فروشگاه", "example.com", "")
	sealed, _ := e.s.box.Seal("secret")
	h, err := e.s.store.Add(store.Host{Name: "own", ProfileID: "fake", URL: fake.URL, Username: "kfncspot", SealedSecret: sealed, ProjectID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	_ = e.s.projects.Update(p.ID, func(x *projects.Project) { x.HostID = h.ID })
	p, _ = e.s.projects.Get(p.ID)
	return &plugEnv{homeEnv: e, fake: fake, site: ps, p: p, h: h}
}

// op posts to the plugins page and returns the started run (waited for) or nil, and the page shown after.
func (e *plugEnv) op(op string, v url.Values) (*engine.Run, string) {
	e.t.Helper()
	pg := "p/" + e.p.ID + "/wordpress/plugins"
	if v.Get("csrf") == "" {
		v.Set("csrf", csrfOf(e.t, e.page(pg)))
	}
	r, body := e.post(pg+"/"+op, v)
	id := r.Request.URL.Query().Get("run")
	if id == "" {
		return nil, body
	}
	e.waitRun(id)
	run, _ := e.s.runner.Get(id, 0)
	return &run, e.page(pg + "?run=" + id)
}

func TestPluginsCardFirstInMoreSteps(t *testing.T) {
	e := newPlugEnv(t, map[string]any{})
	pg := e.page("p/" + e.p.ID + "/wordpress")
	g := pg[strings.Index(pg, `id="g-site"`):]
	g = g[:strings.Index(g, "</section>")]
	first := strings.Index(g, `data-key="`)
	if first < 0 || !strings.HasPrefix(g[first:], `data-key="recplugins"`) {
		t.Fatalf("«افزونه‌های لازم» is not the first card of «مراحل بیشتر»: %s", g[:min(len(g), 600)])
	}
	if !strings.Contains(g, "افزونه‌های لازم") || !strings.Contains(g, `data-link="`+e.s.cfg.BasePath+`p/`+e.p.ID+`/wordpress/plugins"`) {
		t.Fatal("card title or link")
	}
}

func TestPluginsPageListsCatalog(t *testing.T) {
	e := newPlugEnv(t, map[string]any{})
	bp := e.s.cfg.BasePath
	pg := e.page("p/" + e.p.ID + "/wordpress/plugins")
	for _, want := range []string{`id="scope"`, "کاربر kfncspot", `<span dir="ltr">example.com</span>`, "بررسی وضعیت", "نصب همه‌ی انتخاب‌شده‌ها",
		`data-slug="woocommerce"`, `data-slug="persian-woocommerce"`, `data-slug="classic-editor"`, "فروشگاه‌ساز", "LiteSpeed", "وضعیت نامشخص",
		`id="backbtn"`, `href="` + bp + `p/` + e.p.ID + `/wordpress"`, `aria-current="page">افزونه‌های لازم`} {
		if !strings.Contains(pg, want) {
			t.Errorf("plugins page lacks %q", want)
		}
	}
	if n := strings.Count(pg, `class="plugcard pl-`); n != 10 {
		t.Errorf("%d plugin cards", n)
	}
	sameCSRF(t, "plugins", pg)
	if strings.Contains(pg, `name="host"`) || strings.Contains(pg, `name="domain"`) {
		t.Error("the page must not ask for host or address")
	}
	// POST without the form token is refused
	r, _ := e.post("p/"+e.p.ID+"/wordpress/plugins/install", url.Values{"plugin": {"woocommerce"}})
	if r.StatusCode != http.StatusBadRequest || len(e.s.runner.List(0)) != 0 {
		t.Fatal("POST without csrf accepted")
	}
}

func TestPluginsInstallActivateOnProjectSite(t *testing.T) {
	e := newPlugEnv(t, map[string]any{"wordfence": map[string]any{"version": "8", "active": false}})
	other, _ := e.s.store.Add(store.Host{Name: "someone-else", ProfileID: "fake", URL: "https://other.test:2083", Username: "x"})
	// status check first (read-only)
	run, pg := e.op("check", url.Values{})
	if run == nil || run.Status != engine.StatusSuccess || run.WorkflowID != "plugins-status" {
		t.Fatalf("status run: %+v", run)
	}
	if e.site.seen() != "ping wp_info cleanup" {
		t.Fatalf("status check changed something: %s", e.site.seen())
	}
	card := func(pg, slug string) string {
		i := strings.Index(pg, `data-slug="`+slug+`"`)
		c := pg[i+10:]
		if j := strings.Index(c, `data-slug="`); j > 0 {
			c = c[:j]
		}
		return c
	}
	if c := card(pg, "wordfence"); !strings.Contains(c, "نصب‌شده، غیرفعال") || !strings.Contains(c, ">فعال‌سازی</button>") || strings.Contains(c, ">نصب</button>") {
		t.Fatalf("installed-inactive card: %s", c)
	}
	if c := card(pg, "woocommerce"); !strings.Contains(c, "نصب نیست") || !strings.Contains(c, ">نصب</button>") {
		t.Fatalf("missing card: %s", c)
	}
	// install one plugin; posted host/address values are ignored
	run, pg = e.op("install", url.Values{"plugin": {"woocommerce"}, "host": {other.ID}, "domain": {"evil.test"}, "folder": {"x"}, "in_domain": {"evil.test"}})
	if run == nil || run.Status != engine.StatusSuccess {
		t.Fatalf("install run: %+v", run)
	}
	if run.HostID != e.h.ID || run.ProjectID != e.p.ID || run.Inputs["domain"] != "example.com" || run.Inputs["folder"] != "" || run.Inputs["plugins"] != "woocommerce" {
		t.Fatalf("run used host %s inputs %v", run.HostID, run.Inputs)
	}
	if !strings.HasSuffix(e.site.seen(), "ping wp_info plugin_install:woocommerce plugin_activate:woocommerce cleanup") {
		t.Fatalf("actions: %s", e.site.seen())
	}
	if !e.fake.Find("/woocommerce.zip", "") {
		t.Error("zip not uploaded through cPanel")
	}
	if c := card(pg, "woocommerce"); !strings.Contains(c, "✓ نصب و فعال") || strings.Contains(c, "<button") || strings.Contains(c, `name="sel"`) {
		t.Fatalf("installed card: %s", c)
	}
	for _, want := range []string{`id="plugrun"`, "نصب افزونه‌های لازم", `id="log"`, "✔ WooCommerce نصب و فعال شد"} {
		if !strings.Contains(pg, want) {
			t.Errorf("page after install lacks %q", want)
		}
	}
	// an existing plugin is only activated, never uploaded or overwritten
	before := len(e.site.seen())
	run, pg = e.op("install", url.Values{"plugin": {"wordfence"}})
	if run.Status != engine.StatusSuccess {
		t.Fatalf("activate run %s: %s", run.Status, run.Error)
	}
	got := e.site.seen()[before:]
	if strings.Contains(got, "plugin_install") || e.fake.Find("/wordfence.zip", "") || !strings.Contains(got, "plugin_activate:wordfence") {
		t.Fatalf("existing plugin touched: %s", got)
	}
	if c := card(pg, "wordfence"); !strings.Contains(c, "✓ نصب و فعال") {
		t.Fatalf("activated card: %s", c)
	}
	// bulk: checked boxes; unknown slugs are dropped
	run, _ = e.op("install", url.Values{"sel": {"classic-editor", "contact-form-7", "evil"}})
	if run.Inputs["plugins"] != "classic-editor,contact-form-7" || run.Status != engine.StatusSuccess {
		t.Fatalf("bulk run %s inputs %v", run.Status, run.Inputs)
	}
	// run page goes back to the plugins page
	rp := e.page("runs/" + run.ID)
	if !strings.Contains(rp, `href="`+e.s.cfg.BasePath+`p/`+e.p.ID+`/wordpress/plugins?run=`+run.ID+`" id="backbtn"`) {
		t.Error("run page Back does not lead to the plugins page")
	}
	// nothing selected
	if run, body := e.op("install", url.Values{"sel": {"evil"}}); run != nil || !strings.Contains(body, "هیچ افزونه‌ای از فهرست انتخاب نشده") {
		t.Fatal("empty selection started a run")
	}
}

func TestPluginsWithoutHost(t *testing.T) {
	e := newBareEnv(t)
	_, p := e.create("بی‌هاست", "nohost.test", "")
	pg := e.page("p/" + p.ID + "/wordpress/plugins")
	if !strings.Contains(pg, `id="needhost"`) || !strings.Contains(pg, `href="`+e.s.cfg.BasePath+`p/`+p.ID+`/host"`) {
		t.Fatal("missing-host message not shown")
	}
	if strings.Contains(pg, ">نصب</button>") || strings.Contains(pg, "نصب همه‌ی انتخاب‌شده‌ها") || strings.Contains(pg, `id="plugcheck"`) {
		t.Fatal("install buttons shown without a host")
	}
	// a posted host is ignored: the project has none, so nothing starts
	h, _ := e.s.store.Add(store.Host{Name: "x", ProfileID: "fake", URL: "https://x.test:2083", Username: "u"})
	r, body := e.post("p/"+p.ID+"/wordpress/plugins/install", url.Values{"csrf": {csrfOf(t, pg)}, "plugin": {"woocommerce"}, "host": {h.ID}})
	if r.Request.URL.Query().Get("run") != "" || len(e.s.runner.List(0)) != 0 || !strings.Contains(body, "این پروژه هنوز هاست ندارد") {
		t.Fatalf("install without host: %s", r.Request.URL)
	}
}
