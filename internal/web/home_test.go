package web

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/traptiterbeats/wpauto/internal/config"
	"github.com/traptiterbeats/wpauto/internal/cpanel"
	"github.com/traptiterbeats/wpauto/internal/projects"
	"github.com/traptiterbeats/wpauto/internal/store"
)

func TestParseSite(t *testing.T) {
	for in, want := range map[string]siteAddr{
		"example.com":                  {Kind: kindRoot, Domain: "example.com", Parent: "example.com"},
		"https://www.Example.com/":     {Kind: kindRoot, Domain: "example.com", Parent: "example.com"},
		"shop.example.com":             {Kind: kindSubdomain, Domain: "shop.example.com", Parent: "example.com", Label: "shop"},
		"example.com/blog":             {Kind: kindSubfolder, Domain: "example.com", Folder: "blog", Parent: "example.com", Label: "blog"},
		"http://example.com/blog/":     {Kind: kindSubfolder, Domain: "example.com", Folder: "blog", Parent: "example.com", Label: "blog"},
		"example.co.ir":                {Kind: kindRoot, Domain: "example.co.ir", Parent: "example.co.ir"},
		"shop.example.co.ir":           {Kind: kindSubdomain, Domain: "shop.example.co.ir", Parent: "example.co.ir", Label: "shop"},
		"shop.example.com/blog":        {Kind: kindSubfolder, Domain: "shop.example.com", Folder: "blog", Parent: "example.com", Label: "blog"},
		"  ":                           {},
		"example.com:2083/x?y":         {Kind: kindSubfolder, Domain: "example.com", Folder: "x?y", Parent: "example.com", Label: "x?y"},
		"a.b.example.com":              {},
		"example":                      {},
		"exa mple.com":                 {},
		"example.com/a/b":              {},
		"example.com/Blog_Two":         {},
		"-bad.example.com":             {},
		"subdomain-ok-1.example.com":   {Kind: kindSubdomain, Domain: "subdomain-ok-1.example.com", Parent: "example.com", Label: "subdomain-ok-1"},
		"https://shop.example.com/x/":  {Kind: kindSubfolder, Domain: "shop.example.com", Folder: "x", Parent: "example.com", Label: "x"},
		"www.shop.example.com":         {Kind: kindSubdomain, Domain: "shop.example.com", Parent: "example.com", Label: "shop"},
		"EXAMPLE.COM/BLOG":             {Kind: kindSubfolder, Domain: "example.com", Folder: "blog", Parent: "example.com", Label: "blog"},
		"example.com//blog//":          {Kind: kindSubfolder, Domain: "example.com", Folder: "blog", Parent: "example.com", Label: "blog"},
		"https://example.com/blog?x=1": {},
	} {
		got, err := parseSite(in)
		invalid := want == (siteAddr{}) && strings.TrimSpace(in) != ""
		if in == "example.com:2083/x?y" {
			// a query in the folder is not a valid folder name
			invalid, want = true, siteAddr{}
		}
		if invalid {
			if err == nil {
				t.Errorf("%q: expected an error, got %+v", in, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("%q: got %+v, %v; want %+v", in, got, err, want)
		}
	}
	if (siteAddr{Kind: kindSubdomain}).KindLabel() != "ساب‌دامین" || (siteAddr{Kind: kindSubfolder}).KindLabel() != "پوشه" || (siteAddr{Kind: kindRoot}).KindLabel() != "اصلی" {
		t.Fatal("kind labels")
	}
}

func TestInstallInputs(t *testing.T) {
	for _, c := range []struct {
		p                  projects.Project
		mode, domain, name string
	}{
		{projects.Project{Domain: "example.com"}, kindRoot, "example.com", ""},
		{projects.Project{Domain: "shop.example.com"}, kindSubdomain, "example.com", "shop"},
		{projects.Project{Domain: "example.com", Folder: "blog"}, kindSubfolder, "example.com", "blog"},
		// an addon domain the user created as a main domain stays one
		{projects.Project{Domain: "shop.example.com", Mode: kindRoot}, kindRoot, "shop.example.com", ""},
	} {
		mode, dom, name, ok := installInputs(c.p)
		if !ok || mode != c.mode || dom != c.domain || name != c.name {
			t.Errorf("%+v: %s %s %s %v", c.p, mode, dom, name, ok)
		}
	}
	if _, _, _, ok := installInputs(projects.Project{}); ok {
		t.Fatal("project without address gave install inputs")
	}
}

// homeEnv is a logged-in panel with two hosts: h1 reports example.com (+ addon shop.ir),
// h2 reports nothing.
type homeEnv struct {
	t      *testing.T
	s      *Server
	cl     *http.Client
	base   string
	h1, h2 string
}

func newHomeEnv(t *testing.T) *homeEnv {
	t.Helper()
	t.Setenv("WPAUTO_DIR", t.TempDir())
	cfg, pass, _ := config.New(1)
	s, err := New(cfg, "9.9.9-test")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	e := &homeEnv{t: t, s: s, cl: &http.Client{Jar: jar}, base: ts.URL + cfg.BasePath}
	if r, err := e.cl.PostForm(e.base+"login", url.Values{"username": {cfg.Username}, "password": {pass}}); err != nil || r.StatusCode != 200 {
		t.Fatal("login failed")
	}
	h1, _ := s.store.Add(store.Host{Name: "first-host", ProfileID: "generic", URL: "https://a.test:2083", Username: "u"})
	_ = s.store.Update(h1.ID, func(x *store.Host) {
		x.LastReport = &cpanel.Report{MainDomain: "example.com", Domains: []string{"shop.ir"}}
	})
	h2, _ := s.store.Add(store.Host{Name: "second-host", ProfileID: "generic", URL: "https://b.test:2083", Username: "u"})
	e.h1, e.h2 = h1.ID, h2.ID
	return e
}

func (e *homeEnv) get(p string) (string, *http.Response) {
	e.t.Helper()
	r, err := e.cl.Get(e.base + p)
	if err != nil {
		e.t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	return string(b), r
}

func (e *homeEnv) page(p string) string {
	e.t.Helper()
	b, r := e.get(p)
	if r.StatusCode != 200 || !strings.Contains(b, "</html>") {
		e.t.Fatalf("GET %s: %d", p, r.StatusCode)
	}
	return b
}

func (e *homeEnv) create(name, domain, host string) (*http.Response, projects.Project) {
	e.t.Helper()
	v := url.Values{"csrf": {csrfOf(e.t, e.page(""))}, "name": {name}, "domain": {domain}, "back": {e.s.cfg.BasePath}}
	if host != "" {
		v.Set("host", host)
	}
	r, err := e.cl.PostForm(e.base+"projects/new", v)
	if err != nil {
		e.t.Fatal(err)
	}
	r.Body.Close()
	p, _ := e.s.projects.Active()
	return r, p
}

// sameCSRF checks that every POST form on a page carries the token.
func sameCSRF(t *testing.T, name, page string) {
	t.Helper()
	if n := strings.Count(page, `method="post"`); n != strings.Count(page, `name="csrf"`) {
		t.Errorf("%s: %d POST forms but %d csrf fields", name, n, strings.Count(page, `name="csrf"`))
	}
}

func TestHomePage(t *testing.T) {
	e := newHomeEnv(t)
	home := e.page("")
	for _, want := range []string{
		`wpauto 9.9.9-test`, `id="tehran-clock"`, `timeZone:'Asia/Tehran'`, // version + live Tehran clock
		`action="` + e.s.cfg.BasePath + `projects/new"`, `name="name"`, `name="domain"`, `id="np-domain"`,
		`id="np-hostnote"`, // no host selector: the host belongs to the project
		`id="psearch"`, `type="search"`, "هنوز پروژه‌ای نساخته‌اید", `class="toolmenu"`,
		`"co.ir"`, // the script's suffix list
	} {
		if !strings.Contains(home, want) {
			t.Errorf("home lacks %q", want)
		}
	}
	// home is minimal: no breadcrumb, no old menu, no side panel, no runs drawer
	for _, bad := range []string{`class="trail"`, `class="side"`, `id="drawer"`, "شما این‌جا هستید", "مسیر ساخت سایت", `id="np-host"`, `name="host"`} {
		if strings.Contains(home, bad) {
			t.Errorf("home still has %q", bad)
		}
	}
	sameCSRF(t, "home", home)

	e.create("کارگاه سیس", "example.com/blog", "")
	e.create("Naranj Shop", "shop.example.com", "")
	e.create("Root Site", "example.com", "")
	home = e.page("")
	for _, want := range []string{`data-search="کارگاه سیس example.com/blog"`, `data-search="naranj shop shop.example.com"`,
		`data-search="root site example.com"`, ">پوشه<", ">ساب‌دامین<", ">اصلی<", `id="pnone"`, "ساخته شد"} {
		if !strings.Contains(home, want) {
			t.Errorf("project list lacks %q", want)
		}
	}
}

func TestProjectCreateDomainKinds(t *testing.T) {
	e := newHomeEnv(t)
	for _, c := range []struct {
		domain, host                string
		kind, dom, folder, wantHost string
	}{
		// no host at creation, even when a saved host has the domain or one is named:
		// each project enters its own host in «بررسی هاست»
		{"example.com", "", kindRoot, "example.com", "", ""},
		{"shop.example.com", "", kindSubdomain, "shop.example.com", "", ""},
		{"https://shop.ir/blog/", "", kindSubfolder, "shop.ir", "blog", ""},
		{"other.com", "", kindRoot, "other.com", "", ""},
		{"other.com/x", e.h2, kindSubfolder, "other.com", "x", ""},
		{"", "", "", "", "", ""}, // address can come later
	} {
		r, p := e.create("p-"+c.domain, c.domain, c.host)
		if p.Mode != c.kind || p.Domain != c.dom || p.Folder != c.folder || p.HostID != c.wantHost {
			t.Errorf("%q: got mode=%q domain=%q folder=%q host=%q", c.domain, p.Mode, p.Domain, p.Folder, p.HostID)
		}
		// creating goes straight to step 2 (choose the section)
		if r.Request.URL.Path != e.s.cfg.BasePath+"p/"+p.ID || !strings.Contains(r.Request.URL.RawQuery, "msg=") {
			t.Errorf("%q: landed on %s", c.domain, r.Request.URL)
		}
	}
	// an invalid address is refused and nothing is saved
	n := len(e.s.projects.List())
	r, _ := e.create("bad", "a.b.example.com", "")
	if len(e.s.projects.List()) != n || !strings.Contains(r.Request.URL.RawQuery, "err=") || r.Request.URL.Path != e.s.cfg.BasePath {
		t.Fatalf("invalid domain: %s", r.Request.URL)
	}
	// without csrf nothing is created
	if r, _ := e.cl.PostForm(e.base+"projects/new", url.Values{"name": {"x"}}); r.StatusCode != 400 {
		t.Fatal("csrf not enforced on project create")
	}
}

func TestProjectSectionAndWordPressPages(t *testing.T) {
	e := newHomeEnv(t)
	_, p := e.create("فروشگاه نارنج", "example.com/shop", "")
	bp := e.s.cfg.BasePath
	// the project's host: h1 is unowned and unused, so the project takes it as its own
	if id, err := e.s.ensureOwnHost(p.ID, e.h1); err != nil || id != e.h1 {
		t.Fatalf("own host: %s %v", id, err)
	}

	sec := e.page("p/" + p.ID)
	for _, want := range []string{"فروشگاه نارنج", "example.com/shop", ">پوشه<", "first-host",
		`href="` + bp + `p/` + p.ID + `/wordpress"`, ">وردپرس<", "جوملا", "بدون CMS", "لاراول", "به‌زودی",
		`<a href="` + bp + `">پروژه‌ها</a>`, `<strong aria-current="page">فروشگاه نارنج</strong>`, `href="` + bp + `" id="backbtn"`,
		`projects/` + p.ID + `/delete`} {
		if !strings.Contains(sec, want) {
			t.Errorf("section page lacks %q", want)
		}
	}
	if strings.Count(sec, `class="tile soon"`) != 4 || strings.Count(sec, "به‌زودی") != 4 {
		t.Error("joomla, no-CMS and the other sections should be disabled «به‌زودی»")
	}
	sameCSRF(t, "section", sec)

	wp := e.page("p/" + p.ID + "/wordpress")
	for _, want := range []string{"بررسی هاست", "نصب وردپرس", "شخصی‌سازی برای مشتری (AI)", "نصب قالب و دمو", "ساخت خودکار سایت", "همه‌ی مراحل، خودکار و پشت سر هم", `class="ftag">AutoPilot<`, "🚀", `class="tile feature`, "بررسی وردپرس دامنه",
		"حذف وردپرس", "وردپرس نصب‌شده روی آدرس این پروژه را پاک کنید", "بررسی سئو", "انجام‌شده برای این پروژه", "هنوز اجرایی برای این پروژه ثبت نشده",
		`action="` + bp + `projects/` + p.ID + `/activate"`,
		`value="` + bp + `p/` + p.ID + `/host"`, // the host card opens the project's own host page
		`value="` + bp + `run/wp-check-domain?project=` + p.ID + `&amp;host=` + e.h1 + `"`,
		`value="` + bp + `run/wp-install?project=`, `value="` + bp + `customize"`, `value="` + bp + `autopilot?project=` + p.ID + `"`,
		`value="` + bp + `wordpress?project=` + p.ID + `&amp;host=` + e.h1 + `"`,
		"بررسی افزونه‌ها", "بررسی قالب‌ها", `id="g-setup"`, `id="g-site"`, `id="g-auto"`, ">راه‌اندازی<", ">مراحل بیشتر<",
		`<a href="` + bp + `p/` + p.ID + `">فروشگاه نارنج</a>`, `<strong aria-current="page">وردپرس</strong>`,
		`href="` + bp + `p/` + p.ID + `" id="backbtn"`} {
		if !strings.Contains(wp, want) {
			t.Errorf("wordpress page lacks %q", want)
		}
	}
	// «ساخت خودکار سایت» is the very last card, after the «به‌زودی» ones; old names are gone
	if a, z := strings.LastIndex(wp, "بررسی سئو"), strings.Index(wp, `class="tile feature`); a < 0 || z < a || z < strings.LastIndex(wp, `class="tile soon"`) {
		t.Error("the AutoPilot card is not at the end of the page")
	}
	if strings.Contains(wp, "سایت وردپرسی") || strings.Contains(wp, "یک دامنه") || strings.Contains(wp, ">AutoPilot</strong>") {
		t.Error("old card or group name still shown")
	}
	if strings.Count(wp, `class="tile soon"`) != 3 || strings.Count(wp, `class="pp-todo"`) != 5 {
		t.Error("plugins, themes and seo should be the disabled cards; a new project has every stage dark")
	}
	if strings.Contains(wp, "wp-admin/") {
		t.Error("admin link shown before WordPress is installed")
	}
	sameCSRF(t, "wordpress", wp)

	// ticking a stage on this page lights it and shows the site links once WordPress is there
	r, err := e.cl.PostForm(e.base+"projects/"+p.ID+"/mark", url.Values{"csrf": {csrfOf(t, wp)}, "stage": {"wpinstall"}, "on": {"on"}, "back": {bp + "p/" + p.ID + "/wordpress"}})
	if err != nil || r.Request.URL.Path != bp+"p/"+p.ID+"/wordpress" {
		t.Fatalf("mark: %v", err)
	}
	wp = e.page("p/" + p.ID + "/wordpress")
	if !strings.Contains(wp, "✔ نصب وردپرس") || !strings.Contains(wp, `href="https://example.com/shop/wp-admin/"`) || !strings.Contains(wp, "✔ انجام شد") {
		t.Error("done stage or site links missing")
	}

	// a task card activates the project and opens the existing form with it preselected
	_ = e.s.projects.SetActive("")
	r, err = e.cl.PostForm(e.base+"projects/"+p.ID+"/activate", url.Values{"csrf": {csrfOf(t, wp)}, "back": {bp + "run/wp-install?project=" + p.ID}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	form := string(b)
	if a, _ := e.s.projects.Active(); a.ID != p.ID {
		t.Fatal("task card did not activate the project")
	}
	// the project's address and host are shown read-only instead of asked
	for _, want := range []string{`id="scope"`, "آدرس نصب:", `<span dir="ltr">example.com/shop</span> (پوشه)`, `value="فروشگاه نارنج"`,
		`name="project" value="` + p.ID + `"`, `<a href="` + bp + `p/` + p.ID + `/wordpress">وردپرس</a>`, `<strong aria-current="page">نصب وردپرس</strong>`} {
		if !strings.Contains(form, want) {
			t.Errorf("wp-install form lacks %q", want)
		}
	}
	for _, bad := range []string{`name="in_mode"`, `name="in_domain"`, `name="in_name"`, `name="host"`} {
		if strings.Contains(form, bad) {
			t.Errorf("wp-install form inside a project still asks %q", bad)
		}
	}
	if _, r := e.get("p/nope"); r.StatusCode != 404 {
		t.Fatal("unknown project page")
	}
	if _, r := e.get("p/nope/wordpress"); r.StatusCode != 404 {
		t.Fatal("unknown project wordpress page")
	}
}

func TestOldRoutesStillReachable(t *testing.T) {
	e := newHomeEnv(t)
	_, p := e.create("x", "example.com", "")
	tools := e.page("tools")
	// every existing page is linked from the tools menu (on every page) and still renders
	for _, path := range []string{"hosts", "hosts/" + e.h1, "tasks", "runs", "projects", "themes", "customize", "ai", "wordpress",
		"autopilot", "tools", "run/host-check", "run/wp-install", "run/wp-check-domain", "run/theme-install", "p/" + p.ID, "p/" + p.ID + "/wordpress"} {
		page := e.page(path)
		sameCSRF(t, path, page)
		if !strings.Contains(page, `class="toolmenu"`) {
			t.Errorf("%s: tools menu missing", path)
		}
		if path != "tools" && !strings.Contains(page, `id="backbtn"`) {
			t.Errorf("%s: back button missing", path)
		}
	}
	for _, path := range []string{"tasks", "runs", "projects", "themes", "customize", "ai", "wordpress", "autopilot", "tools"} {
		if !strings.Contains(tools, `href="`+e.s.cfg.BasePath+path+`"`) {
			t.Errorf("tools menu lacks %s", path)
		}
	}
	// hosts belong to projects now: the /hosts pages still work but are not listed
	if strings.Contains(tools, `href="`+e.s.cfg.BasePath+`hosts"`) || strings.Contains(tools, "هاست‌ها") {
		t.Error("tools page still lists هاست‌ها")
	}
	// the logout form (with csrf) lives in the tools menu
	if !strings.Contains(tools, `action="`+e.s.cfg.BasePath+`logout"`) {
		t.Error("logout missing")
	}
	if _, r := e.get("api/health"); r.StatusCode != 200 {
		t.Error("health")
	}
}

func TestCrumbsAndBack(t *testing.T) {
	cs := []crumb{{"پروژه‌ها", "/a/"}, {"x", "/a/p/1"}, {"وردپرس", "/a/p/1/wordpress"}, {"کار", ""}}
	if backOf(cs) != "/a/p/1/wordpress" {
		t.Fatal("back should go to the parent crumb")
	}
	if backOf([]crumb{{"پروژه‌ها", ""}}) != "" {
		t.Fatal("home has no back")
	}
	if !projectScoped("run/wp-install") || !projectScoped("runs/123") || projectScoped("runs") || projectScoped("hosts") {
		t.Fatal("projectScoped")
	}
}
