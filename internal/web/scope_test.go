package web

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/traptiterbeats/wpauto/internal/catalog"
	"github.com/traptiterbeats/wpauto/internal/catalog/catalogtest"
	"github.com/traptiterbeats/wpauto/internal/config"
	"github.com/traptiterbeats/wpauto/internal/cpanel/cpaneltest"
	"github.com/traptiterbeats/wpauto/internal/engine"
	"github.com/traptiterbeats/wpauto/internal/projects"
	"github.com/traptiterbeats/wpauto/internal/store"
)

// ownHostFor gives a project a host record of its own on the local fake cPanel. The saved
// secret is wrong on purpose: a run stops at the login, so nothing is installed anywhere.
func ownHostFor(t *testing.T, e *homeEnv, p projects.Project, fakeURL string) store.Host {
	t.Helper()
	sealed, err := e.s.box.Seal("wrong-secret")
	if err != nil {
		t.Fatal(err)
	}
	h, err := e.s.store.Add(store.Host{Name: "own-" + p.Name, ProfileID: "fake", URL: fakeURL, Username: "kfncspot", SealedSecret: sealed, ProjectID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.s.projects.Update(p.ID, func(x *projects.Project) { x.HostID = h.ID }); err != nil {
		t.Fatal(err)
	}
	return h
}

// startRun posts a task form and returns the started run (the redirect target).
func startRun(t *testing.T, e *homeEnv, wf string, v url.Values) engine.Run {
	t.Helper()
	r, body := e.post("run/"+wf, v)
	path := r.Request.URL.Path
	i := strings.Index(path, "/runs/")
	if i < 0 {
		t.Fatalf("POST run/%s did not start a run: %s %s", wf, r.Request.URL, body[:min(len(body), 400)])
	}
	id := path[i+len("/runs/"):]
	e.waitRun(id)
	run, ok := e.s.runner.Get(id, 0)
	if !ok {
		t.Fatal("run not found")
	}
	return run
}

func TestWPInstallTakesProjectAddressAndHost(t *testing.T) {
	e := newBareEnv(t)
	bp := e.s.cfg.BasePath
	fake := cpaneltest.New(t, "kfncspot", "secret")
	t.Cleanup(fake.Close)
	other, _ := e.s.store.Add(store.Host{Name: "someone-else", ProfileID: "fake", URL: fake.URL, Username: "x"})

	cases := []struct {
		name, addr, kind, mode, domain, sub string
	}{
		{"احمد", "ahmad.example.test", "ساب‌دامین", "subdomain", "example.test", "ahmad"},
		{"بلاگ", "example.test/blog", "پوشه", "subfolder", "example.test", "blog"},
		{"اصلی", "naranj.test", "اصلی", "root", "naranj.test", ""},
	}
	for _, c := range cases {
		_, p := e.create(c.name, c.addr, "")
		h := ownHostFor(t, e, p, fake.URL)
		form := e.page("run/wp-install?project=" + p.ID)
		for _, want := range []string{`id="scope"`, "هاست:", "کاربر kfncspot", "آدرس نصب:",
			`<span dir="ltr">` + c.addr + `</span> (` + c.kind + `)`, `href="` + bp + `p/` + p.ID + `/host"`, "ویرایش هاست",
			`name="project" value="` + p.ID + `"`, `name="in_admin_user"`, `name="in_admin_email"`, `name="in_title"`, `name="in_locale"`, `name="in_admin_pass"`} {
			if !strings.Contains(form, want) {
				t.Errorf("%s: wp-install form lacks %q", c.addr, want)
			}
		}
		for _, bad := range []string{`name="host"`, `name="in_mode"`, `name="in_domain"`, `name="in_name"`, "محل نصب", "دامنه‌ی اصلی", "نام ساب‌دامین یا پوشه"} {
			if strings.Contains(form, bad) {
				t.Errorf("%s: wp-install form inside a project still has %q", c.addr, bad)
			}
		}
		sameCSRF(t, "wp-install", form)
		// the plan follows the project's mode, not what the page asks
		var plan struct {
			Steps []sideStep `json:"steps"`
		}
		b, _ := e.get("run/wp-install/plan?project=" + p.ID + "&in_mode=root")
		_ = json.Unmarshal([]byte(b), &plan)
		st := map[string]string{}
		for _, x := range plan.Steps {
			st[x.ID] = x.State
		}
		if (c.mode == "subfolder") != (st["folder"] == "yes") || (c.mode == "subdomain") != (st["subdomain"] == "yes") {
			t.Errorf("%s: plan ignores the project's mode: %v", c.addr, st)
		}

		// tampered POST: another host, mode, domain and name are all ignored
		run := startRun(t, e, "wp-install", url.Values{"csrf": {csrfOf(t, form)}, "project": {p.ID}, "host": {other.ID},
			"in_mode": {"root"}, "in_domain": {"evil.test"}, "in_name": {"x"}, "in_title": {"t"}, "in_locale": {"fa_IR"},
			"in_admin_user": {"boss"}, "in_admin_email": {"a@b.test"}, "in_admin_pass": {"long-enough-pass-1"}})
		if run.Inputs["mode"] != c.mode || run.Inputs["domain"] != c.domain || run.Inputs["name"] != c.sub {
			t.Errorf("%s: run used %q %q %q, want %q %q %q", c.addr, run.Inputs["mode"], run.Inputs["domain"], run.Inputs["name"], c.mode, c.domain, c.sub)
		}
		if run.HostID != h.ID || run.ProjectID != p.ID {
			t.Errorf("%s: run on host %s / project %s, want the project's own %s", c.addr, run.HostID, run.ProjectID, h.ID)
		}
		if pp, _ := e.s.projects.Get(p.ID); pp.HostID != h.ID || pp.Site() != c.addr {
			t.Errorf("%s: project changed: %+v", c.addr, pp)
		}
	}

	// the legacy form outside any project keeps every field
	_ = e.s.projects.SetActive("")
	legacy := e.page("run/wp-install")
	for _, want := range []string{`name="host"`, `name="in_mode"`, `name="in_domain"`, `name="in_name"`} {
		if !strings.Contains(legacy, want) {
			t.Errorf("legacy wp-install form lacks %q", want)
		}
	}
	if strings.Contains(legacy, `id="scope"`) {
		t.Error("legacy form shows a project summary")
	}
}

func TestProjectTaskWithoutHostOrAddress(t *testing.T) {
	e := newBareEnv(t)
	bp := e.s.cfg.BasePath
	fake := cpaneltest.New(t, "kfncspot", "secret")
	t.Cleanup(fake.Close)
	_, p := e.create("بی‌هاست", "ahmad.example.test", "")
	form := e.page("run/wp-install?project=" + p.ID)
	if !strings.Contains(form, `id="needhost"`) || !strings.Contains(form, `href="`+bp+`p/`+p.ID+`/host"`) || strings.Contains(form, `id="runform"`) {
		t.Error("missing-host message not shown")
	}
	before := len(e.s.runner.List(0))
	r, _ := e.post("run/wp-install", url.Values{"csrf": {csrfOf(t, form)}, "project": {p.ID}, "in_admin_user": {"boss"}})
	if !strings.Contains(r.Request.URL.RawQuery, "err=") || len(e.s.runner.List(0)) != before {
		t.Error("a project without a host started a run")
	}

	// a project without an address: no address field, a link to enter it once on the project page
	_, q := e.create("بی‌آدرس", "", "")
	ownHostFor(t, e, q, fake.URL)
	for _, path := range []string{"run/wp-install?project=", "run/theme-install?project=", "run/wp-check-domain?project=", "wordpress?project="} {
		pg := e.page(path + q.ID)
		if !strings.Contains(pg, `id="needaddr"`) || strings.Contains(pg, `name="in_domain"`) || strings.Contains(pg, `name="in_mode"`) {
			t.Errorf("%s: no address message (or an address field) for a project without an address", path)
		}
	}
	r, _ = e.post("run/wp-check-domain", url.Values{"csrf": {csrfOf(t, form)}, "project": {q.ID}, "in_domain": {"evil.test"}})
	if !strings.Contains(r.Request.URL.RawQuery, "err=") || len(e.s.runner.List(0)) != before {
		t.Error("a project without an address took a posted domain")
	}
	page := e.page("p/" + q.ID)
	if !strings.Contains(page, `id="address"`) {
		t.Fatal("project page has no address form")
	}
	e.post("projects/"+q.ID+"/address", url.Values{"csrf": {csrfOf(t, page)}, "domain": {"shop.example.test"}, "back": {bp + "p/" + q.ID}})
	if qq, _ := e.s.projects.Get(q.ID); qq.Site() != "shop.example.test" || qq.Mode != "subdomain" {
		t.Fatalf("address not saved: %+v", qq)
	}
	// entered once: a second post does not change it, and the form is gone
	e.post("projects/"+q.ID+"/address", url.Values{"csrf": {csrfOf(t, page)}, "domain": {"evil.test"}, "back": {bp + "p/" + q.ID}})
	if qq, _ := e.s.projects.Get(q.ID); qq.Site() != "shop.example.test" || strings.Contains(e.page("p/"+q.ID), `id="address"`) {
		t.Fatal("project address changed after it was set")
	}
}

func TestCheckAndThemeFormsUseProjectAddress(t *testing.T) {
	e := newBareEnv(t)
	fake := cpaneltest.New(t, "kfncspot", "secret")
	t.Cleanup(fake.Close)
	_, p := e.create("بلاگ", "example.test/blog", "")
	ownHostFor(t, e, p, fake.URL)

	check := e.page("run/wp-check-domain?project=" + p.ID)
	if !strings.Contains(check, "<title>بررسی وردپرس دامنه | wpauto</title>") || !strings.Contains(check, `<strong aria-current="page">بررسی وردپرس دامنه</strong>`) ||
		strings.Contains(check, "یک دامنه") {
		t.Error("wp-check-domain title not renamed")
	}
	if !strings.Contains(check, `id="scope"`) || !strings.Contains(check, "آدرس سایت:") || strings.Contains(check, `name="in_domain"`) || strings.Contains(check, `name="host"`) {
		t.Error("wp-check-domain form still asks host/domain inside a project")
	}
	run := startRun(t, e, "wp-check-domain", url.Values{"csrf": {csrfOf(t, check)}, "project": {p.ID}, "in_domain": {"evil.test"}})
	if run.Inputs["domain"] != "example.test/blog" {
		t.Errorf("wp-check-domain used %q", run.Inputs["domain"])
	}
	if run.Title != "بررسی وردپرس دامنه" {
		t.Errorf("run title %q", run.Title)
	}

	// D: theme + demo takes the host, domain and folder from the project
	theme := e.page("run/theme-install?project=" + p.ID)
	for _, bad := range []string{`name="host"`, `name="in_domain"`, `name="in_folder"`, "دامنه یا ساب‌دامین سایت", "پوشه (اگر وردپرس داخل پوشه است)"} {
		if strings.Contains(theme, bad) {
			t.Errorf("theme-install form inside a project still has %q", bad)
		}
	}
	if !strings.Contains(theme, `id="scope"`) || !strings.Contains(theme, "آدرس نصب:") || !strings.Contains(theme, `<span dir="ltr">example.test/blog</span> (پوشه)`) {
		t.Error("theme-install form lacks the read-only summary")
	}
	_ = e.s.projects.SetActive("")
	if legacy := e.page("run/theme-install"); !strings.Contains(legacy, `name="in_domain"`) || !strings.Contains(legacy, `name="in_folder"`) {
		t.Error("legacy theme-install form lost its fields")
	}
	// the server ignores a posted domain/folder for the project (stored inputs show what it used)
	sc := e.s.scopeFor(mustWF(t, e.s, "theme-install"), p)
	in := map[string]string{"domain": "evil.test", "folder": "x"}
	sc.applyLocks(in)
	if in["domain"] != "example.test" || in["folder"] != "blog" {
		t.Errorf("theme-install locks: %v", in)
	}
}

func mustWF(t *testing.T, s *Server, id string) *engine.Workflow {
	t.Helper()
	wf, ok := engine.FindWorkflow(s.workflows(), id)
	if !ok {
		t.Fatal("no workflow " + id)
	}
	return wf
}

// C and F: an old run keeps its stored title on disk but shows the registry's; the remove page
// inside a project lists and removes only the WordPress at the project's own address.
func TestRenamedRunTitleAndProjectRemove(t *testing.T) {
	e := newBareEnv(t)
	bp := e.s.cfg.BasePath
	fake := cpaneltest.New(t, "kfncspot", "secret")
	t.Cleanup(fake.Close)
	_, p := e.create("احمد", "ahmad.example.test", "")
	h := ownHostFor(t, e, p, fake.URL)

	dir := config.Dir()
	write := func(run engine.Run) {
		b, _ := json.Marshal(run)
		if err := os.WriteFile(filepath.Join(dir, "runs", run.ID+".json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	write(engine.Run{ID: "old-check", WorkflowID: "wp-check-domain", Title: "بررسی وردپرس یک دامنه", HostID: h.ID, ProjectID: p.ID,
		Status: engine.StatusSuccess, StartedAt: now, EndedAt: now})
	list, _ := json.Marshal([]engine.FoundWP{{Label: "ahmad.example.test", Dir: "/home/kfncspot/ahmad.example.test", Version: "6.6"},
		{Label: "example.test", Dir: "/home/kfncspot/public_html", Version: "6.5"},
		{Label: "example.test/blog", Dir: "/home/kfncspot/public_html/blog", Version: "6.4"}})
	write(engine.Run{ID: "scan", WorkflowID: "wp-scan", Title: "x", HostID: h.ID, Status: engine.StatusSuccess, StartedAt: now.Add(-time.Minute), EndedAt: now,
		Steps: []engine.StepState{{ID: "detect", Status: engine.StatusSuccess, Outputs: map[string]string{"list": string(list)}}}})

	// a second panel on the same data loads the runs from disk
	cfg2, pass2, _ := config.New(1)
	cfg2.BasePath = e.s.cfg.BasePath
	s2, err := New(cfg2, "test")
	if err != nil {
		t.Fatal(err)
	}
	e2 := loginEnv(t, s2, pass2)

	wp := e2.page("p/" + p.ID + "/wordpress")
	if !strings.Contains(wp, "آخرین اجرا: <a href=\""+bp+"runs/old-check\">بررسی وردپرس دامنه</a>") || strings.Contains(wp, "یک دامنه") {
		t.Error("last run line does not use the renamed title")
	}
	if rv := e2.page("runs/old-check"); strings.Contains(rv, "یک دامنه") {
		t.Error("run page shows the old title")
	}

	rm := e2.page("wordpress?project=" + p.ID)
	if !strings.Contains(rm, "<title>حذف وردپرس | wpauto</title>") || !strings.Contains(rm, `<strong aria-current="page">حذف وردپرس</strong>`) ||
		!strings.Contains(rm, `<a href="`+bp+`p/`+p.ID+`/wordpress">وردپرس</a>`) {
		t.Error("remove page title or breadcrumb")
	}
	if !strings.Contains(rm, `value="/home/kfncspot/ahmad.example.test"`) || strings.Contains(rm, `value="/home/kfncspot/public_html"`) ||
		strings.Contains(rm, `value="/home/kfncspot/public_html/blog"`) || strings.Contains(rm, `<select name="host"`) {
		t.Error("remove page inside a project must list only the project's own install")
	}
	sameCSRF(t, "remove", rm)
	// posting another site's directory (outside the project's address) removes nothing
	before := len(e2.s.runner.List(0))
	r, _ := e2.post("wordpress/remove", url.Values{"csrf": {csrfOf(t, rm)}, "project": {p.ID}, "host": {h.ID},
		"site": {"/home/kfncspot/public_html", "/home/kfncspot/public_html/blog"}, "confirm": {removeWord}})
	if !strings.Contains(r.Request.URL.RawQuery, "err=") || len(e2.s.runner.List(0)) != before {
		t.Error("a site outside the project's address was accepted for removal")
	}
	// without the project, the old page still lists every install of the host
	_ = s2.projects.SetActive("")
	if all := e2.page("wordpress?host=" + h.ID); !strings.Contains(all, `value="/home/kfncspot/public_html/blog"`) || !strings.Contains(all, "وردپرس‌های هاست") {
		t.Error("legacy page lost the full list")
	}
}

func loginEnv(t *testing.T, s *Server, pass string) *homeEnv {
	t.Helper()
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	e := &homeEnv{t: t, s: s, cl: &http.Client{Jar: jar}, base: ts.URL + s.cfg.BasePath}
	if r, err := e.cl.PostForm(e.base+"login", url.Values{"username": {s.cfg.Username}, "password": {pass}}); err != nil || r.StatusCode != 200 {
		t.Fatal("login failed")
	}
	return e
}

func TestProjectSitesFilter(t *testing.T) {
	list := []engine.FoundWP{{Label: "a.test"}, {Label: "a.test/blog"}, {Label: "shop.a.test"}}
	if got := projectSites(projects.Project{Domain: "a.test", Folder: "blog"}, list); len(got) != 1 || got[0].Label != "a.test/blog" {
		t.Errorf("folder project: %v", got)
	}
	if got := projectSites(projects.Project{Domain: "a.test"}, list); len(got) != 1 || got[0].Label != "a.test" {
		t.Errorf("root project: %v", got)
	}
	if got := projectSites(projects.Project{}, list); len(got) != 0 {
		t.Errorf("project without address: %v", got)
	}
}

// E: the customize theme picker is a grid of radio cards with thumbnails (placeholder without one).
func TestCustomizeThemeGrid(t *testing.T) {
	e := newBareEnv(t)
	src := t.TempDir()
	cat := engine.Catalog()
	if _, err := cat.AddPlugin(catalogtest.Plugin(t, src, catalog.ImporterSlug, "")); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"acme", "zeta"} {
		if _, err := cat.Add(catalog.AddInput{ThemeZip: catalogtest.Theme(t, src, slug), DemoZip: catalogtest.RichDemo(t, src, slug, false)}); err != nil {
			t.Fatal(err)
		}
	}
	pg := e.page("customize")
	for _, want := range []string{`class="theme-pick"`, `class="theme-card"`, `type="radio" name="theme" value="acme" checked`, `type="radio" name="theme" value="zeta"`, `<img src="` + e.s.cfg.BasePath + `themes/acme/screenshot" alt="Test acme"`} {
		if !strings.Contains(pg, want) {
			t.Errorf("customize page lacks %q", want)
		}
	}
	if strings.Contains(pg, `<select name="theme">`) {
		t.Error("customize still uses a plain select for the theme")
	}
	if strings.Count(pg, `class="theme-card"`) != 2 {
		t.Error("one card per theme")
	}
	sameCSRF(t, "customize", pg)
	// picking the second card makes a draft on that theme
	r, _ := e.post("customize/new", url.Values{"csrf": {csrfOf(t, pg)}, "title": {"کافه"}, "theme": {"zeta"}})
	if !strings.Contains(r.Request.URL.Path, "/customize/") {
		t.Fatalf("draft not created: %s", r.Request.URL)
	}
	found := false
	for _, d := range engine.Drafts().List() {
		found = found || d.Theme == "zeta"
	}
	if !found {
		t.Error("draft not on the picked theme")
	}
	// a theme without a thumbnail gets a placeholder
	var buf strings.Builder
	if err := e.s.tpl.ExecuteTemplate(&buf, "themepick", themePick{Name: "theme", Themes: []*catalog.Theme{{Slug: "bare", Name: "Bare"}}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `class="noshot"`) || strings.Contains(buf.String(), "<img") || !strings.Contains(buf.String(), `value="bare" checked`) {
		t.Errorf("placeholder card: %s", buf.String())
	}
	// the theme-install form uses the same picker
	e.s.store.Add(store.Host{Name: "h", ProfileID: "generic", URL: "https://h.test:2083", Username: "u"})
	if f := e.page("run/theme-install"); !strings.Contains(f, `type="radio" name="in_theme" value="acme"`) {
		t.Error("theme-install form does not use the theme grid")
	}
}
