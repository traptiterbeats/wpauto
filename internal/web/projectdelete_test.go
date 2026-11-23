package web

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/traptiterbeats/wpauto/internal/config"
	"github.com/traptiterbeats/wpauto/internal/cpanel/cpaneltest"
	"github.com/traptiterbeats/wpauto/internal/engine"
	"github.com/traptiterbeats/wpauto/internal/profile"
	"github.com/traptiterbeats/wpauto/internal/projects"
	"github.com/traptiterbeats/wpauto/internal/store"
)

const delSecret = "s3cret-Pass"

// delProject makes a project at site with its own WORKING host on the fake cPanel; installed
// marks WordPress as installed (so the host checkbox is offered).
func delProject(t *testing.T, e *homeEnv, name, site, fakeURL string, installed bool) projects.Project {
	t.Helper()
	_, p := e.create(name, site, "")
	sealed, err := e.s.box.Seal(delSecret)
	if err != nil {
		t.Fatal(err)
	}
	h, err := e.s.store.Add(store.Host{Name: "own-" + name, ProfileID: "fake", URL: fakeURL, Username: "kfncspot", SealedSecret: sealed, ProjectID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	_ = e.s.projects.Update(p.ID, func(x *projects.Project) {
		x.HostID = h.ID
		if installed {
			x.Marked = map[string]bool{"wpinstall": true}
		}
	})
	p, _ = e.s.projects.Get(p.ID)
	return p
}

// runIDOf returns the run a POST redirected to ("" when it did not start one).
func runIDOf(path string) string {
	if i := strings.Index(path, "/runs/"); i >= 0 {
		return path[i+len("/runs/"):]
	}
	return ""
}

func hostCalls(f *cpaneltest.Fake) string { return strings.Join(f.CallList(), ",") }

// The dialog on the home row and on the project page, the confirm page, and the plain delete
// (host checkbox off): project, its host record, runs and draft go; the host is not contacted.
func TestProjectDeleteWithoutSite(t *testing.T) {
	e := newBareEnv(t)
	bp := e.s.cfg.BasePath
	fake := cpaneltest.New(t, "kfncspot", delSecret)
	t.Cleanup(fake.Close)
	p := delProject(t, e, "بی‌سایت", "example.com/shop", fake.URL, false)

	home := e.page("")
	for _, want := range []string{
		`</a>
        <a class="btn ghost danger small pr-del" href="` + bp + `p/` + p.ID + `/delete" data-dialog="del-` + p.ID + `"`, // outside the row link
		`<dialog id="del-` + p.ID + `" class="deldialog"`, "همه‌چیز این پروژه پاک شود؟",
		"سایت وردپرس روی هاست هم پاک شود (فایل‌ها و دیتابیس) تا فضای هاست آزاد شود",
		`<span dir="ltr" class="del-site">example.com/shop</span>`,
		`name="wipe_site" value="1" class="del-wipe" disabled`, "هنوز وردپرسی برای این پروژه نصب نشده",
		`name="sure" value="1" required class="del-sure"`, `class="danger del-go"`, `go.disabled=!sure.checked`, "showModal",
	} {
		if !strings.Contains(home, want) {
			t.Errorf("home lacks %q", want)
		}
	}
	sameCSRF(t, "home", home)
	pp := e.page("p/" + p.ID)
	if !strings.Contains(pp, `data-dialog="del-`+p.ID+`"`) || !strings.Contains(pp, "همه‌چیز این پروژه پاک شود؟") || strings.Contains(pp, "onsubmit=") {
		t.Error("project page does not use the same dialog")
	}
	sameCSRF(t, "project", pp)
	cp := e.page("p/" + p.ID + "/delete")
	if !strings.Contains(cp, `id="delpage"`) || !strings.Contains(cp, `href="`+bp+`p/`+p.ID+`" id="backbtn"`) || !strings.Contains(cp, `name="errback" value="`+bp+`p/`+p.ID+`/delete"`) {
		t.Error("confirm page lacks the form or Back")
	}
	sameCSRF(t, "confirm", cp)
	if strings.Contains(e.page("projects"), "onsubmit=\"return confirm(this.purge") {
		t.Error("projects list still has the old delete form")
	}
	csrf := csrfOf(t, home)

	// no csrf → refused; no tick → refused; project kept
	if r, _ := e.post("projects/"+p.ID+"/delete", url.Values{"sure": {"1"}, "purge": {"1"}}); r.StatusCode != 400 {
		t.Errorf("delete without csrf: %d", r.StatusCode)
	}
	r, _ := e.post("projects/"+p.ID+"/delete", url.Values{"csrf": {csrf}, "purge": {"1"}, "back": {bp}, "errback": {bp + "p/" + p.ID + "/delete"}})
	if _, ok := e.s.projects.Get(p.ID); !ok || r.Request.URL.Path != bp+"p/"+p.ID+"/delete" || !strings.Contains(r.Request.URL.RawQuery, "err=") {
		t.Fatalf("delete without the tick: %s", r.Request.URL)
	}
	// the host checkbox is refused server-side too while nothing is installed
	r, _ = e.post("projects/"+p.ID+"/delete", url.Values{"csrf": {csrf}, "purge": {"1"}, "sure": {"1"}, "wipe_site": {"1"}, "back": {bp}})
	if _, ok := e.s.projects.Get(p.ID); !ok || !strings.Contains(r.Request.URL.RawQuery, "err=") || len(e.s.runner.List(0)) != 0 {
		t.Fatal("site wipe accepted for a project without WordPress")
	}

	hostID := p.HostID
	r, body := e.post("projects/"+p.ID+"/delete", url.Values{"csrf": {csrf}, "purge": {"1"}, "sure": {"1"}, "back": {bp}})
	if _, ok := e.s.projects.Get(p.ID); ok {
		t.Fatal("project not deleted")
	}
	if _, ok := e.s.store.Get(hostID); ok {
		t.Fatal("project's host record not deleted")
	}
	if r.Request.URL.Path != bp || !strings.Contains(body, "سایت روی هاست دست نخورد") {
		t.Fatalf("landed on %s", r.Request.URL)
	}
	if fake.CallCount() != 0 {
		t.Fatalf("the host was contacted: %s", hostCalls(fake))
	}
}

// Host checkbox on: a visible wp-remove run removes only the project's site, then the project,
// its host record and runs go; the run itself stays (detached) and home shows the summary.
func TestProjectDeleteWithSite(t *testing.T) {
	e := newBareEnv(t)
	bp := e.s.cfg.BasePath
	fake := cpaneltest.New(t, "kfncspot", delSecret)
	t.Cleanup(fake.Close)
	fake.QuotaMB = []float64{300, 250}
	other := delProject(t, e, "دیگری", "example.com", fake.URL, true) // the main site must stay
	p := delProject(t, e, "فروشگاه", "example.com/shop", fake.URL, true)

	home := e.page("")
	if strings.Contains(home, `class="del-wipe" disabled`) || !strings.Contains(home, `href="https://example.com/shop/"`) || !strings.Contains(home, "ساب‌دامینش فقط اگر wpauto ساخته باشد") {
		t.Error("host checkbox not offered for an installed project")
	}
	hostID := p.HostID
	r, body := e.post("projects/"+p.ID+"/delete", url.Values{"csrf": {csrfOf(t, home)}, "purge": {"1"}, "sure": {"1"}, "wipe_site": {"1"}, "back": {bp}})
	id := runIDOf(r.Request.URL.Path)
	if id == "" {
		t.Fatalf("no run started: %s", r.Request.URL)
	}
	if !strings.Contains(body, `id="wipeinfo"`) || !strings.Contains(body, `id="backbtn"`) || !strings.Contains(body, `?finish=1`) {
		t.Error("live run page lacks the delete banner, Back or the finish redirect")
	}
	if st := e.waitRun(id); st != "success" {
		run, _ := e.s.runner.Get(id, 0)
		t.Fatalf("wipe run: %s %s", st, run.Error)
	}
	if _, ok := e.s.projects.Get(p.ID); ok {
		t.Fatal("project kept after its site was removed")
	}
	if _, ok := e.s.store.Get(hostID); ok {
		t.Fatal("host record kept")
	}
	calls := hostCalls(fake)
	for _, want := range []string{"Mysql/delete_database", "api2:Fileman/fileop"} {
		if !strings.Contains(calls, want) {
			t.Errorf("wp-remove did not call %s: %s", want, calls)
		}
	}
	if strings.Contains(calls, "delsubdomain") {
		t.Error("a subdomain was deleted")
	}
	run, ok := e.s.runner.Get(id, 0)
	if !ok || run.ProjectID != "" || run.Inputs["project"] != p.ID || run.Inputs["site"] != "example.com/shop" {
		t.Fatalf("wipe run not kept detached: %+v", run.Inputs)
	}
	log := ""
	for _, l := range run.Log {
		log += l.Msg + "\n"
	}
	if strings.Contains(log, "kfncspot_public_html") || !strings.Contains(log, "پوشه‌ی /home/kfncspot/public_html/shop حذف شد") {
		t.Errorf("removed more than the project's site:\n%s", log)
	}
	if _, ok := e.s.projects.Get(other.ID); !ok {
		t.Fatal("another project was deleted")
	}
	// the page's end-of-run reload goes home with the summary and a link to this run
	home, r = e.get("runs/" + id + "?finish=1")
	if r.Request.URL.Path != bp || r.Request.URL.Query().Get("wiperun") != id {
		t.Fatalf("finish landed on %s", r.Request.URL)
	}
	for _, want := range []string{"پروژه‌ی «فروشگاه»", "حدود 50 MB از فضای هاست آزاد شد", "250 MB از 1024 MB", `id="wipereport"`, `href="` + bp + `runs/` + id + `"`} {
		if !strings.Contains(home, want) {
			t.Errorf("home flash lacks %q", want)
		}
	}
	// the report stays reachable; its Back goes home
	rv := e.page("runs/" + id)
	if !strings.Contains(rv, `href="`+bp+`" id="backbtn"`) || !strings.Contains(rv, "حدود 50 MB") {
		t.Error("run report after the delete")
	}
}

// wp-remove fails (no WordPress at the address any more): the project stays, the run shows why.
func TestProjectDeleteWithSiteFails(t *testing.T) {
	e := newBareEnv(t)
	bp := e.s.cfg.BasePath
	fake := cpaneltest.New(t, "kfncspot", delSecret)
	t.Cleanup(fake.Close)
	p := delProject(t, e, "وبلاگ", "blog.example.com", fake.URL, true)
	home := e.page("")
	r, _ := e.post("projects/"+p.ID+"/delete", url.Values{"csrf": {csrfOf(t, home)}, "purge": {"1"}, "sure": {"1"}, "wipe_site": {"1"}, "back": {bp}})
	id := runIDOf(r.Request.URL.Path)
	if id == "" {
		t.Fatalf("no run started: %s", r.Request.URL)
	}
	if st := e.waitRun(id); st != "failed" {
		t.Fatalf("wipe run: %s", st)
	}
	if _, ok := e.s.projects.Get(p.ID); !ok {
		t.Fatal("project deleted although its site was not removed")
	}
	if _, ok := e.s.store.Get(p.HostID); !ok {
		t.Fatal("host record deleted on failure")
	}
	if strings.Contains(hostCalls(fake), "delete_database") || strings.Contains(hostCalls(fake), "delsubdomain") {
		t.Fatal("something was removed")
	}
	_, rv := e.get("runs/" + id + "?finish=1") // a failed run is shown, never redirected
	if rv.Request.URL.Path != bp+"runs/"+id {
		t.Fatalf("failed run redirected to %s", rv.Request.URL)
	}
	page := e.page("runs/" + id)
	for _, want := range []string{"وردپرسی پیدا نشد", "نگه داشته شد", `href="` + bp + `p/` + p.ID + `" id="backbtn"`} {
		if !strings.Contains(page, want) {
			t.Errorf("failed run page lacks %q", want)
		}
	}
}

// Two projects on the same site: removing the site is refused (dialog + server), nothing runs.
func TestProjectDeleteSharedSiteRefused(t *testing.T) {
	e := newBareEnv(t)
	bp := e.s.cfg.BasePath
	fake := cpaneltest.New(t, "kfncspot", delSecret)
	t.Cleanup(fake.Close)
	a := delProject(t, e, "اول", "example.com/shop", fake.URL, true)
	b := delProject(t, e, "دوم", "Example.com/shop", fake.URL, true)
	home := e.page("")
	if !strings.Contains(home, "هم به همین سایت (example.com/shop) اشاره می‌کند") {
		t.Error("dialog does not explain the shared site")
	}
	r, _ := e.post("projects/"+b.ID+"/delete", url.Values{"csrf": {csrfOf(t, home)}, "purge": {"1"}, "sure": {"1"}, "wipe_site": {"1"}, "back": {bp}})
	if !strings.Contains(r.Request.URL.RawQuery, "err=") || runIDOf(r.Request.URL.Path) != "" {
		t.Fatalf("shared site accepted: %s", r.Request.URL)
	}
	for _, p := range []projects.Project{a, b} {
		if _, ok := e.s.projects.Get(p.ID); !ok {
			t.Fatal("a project was deleted")
		}
	}
	if fake.CallCount() != 0 || len(e.s.runner.List(0)) != 0 {
		t.Fatal("the host was contacted")
	}
}

// A subdomain is removed only when a wp-install run of wpauto created it.
func TestProjectDeleteOwnSubdomain(t *testing.T) {
	e := newBareEnv(t)
	fake := cpaneltest.New(t, "kfncspot", delSecret)
	t.Cleanup(fake.Close)
	for _, f := range []string{"wp-config.php", "wp-includes/version.php", "index.php"} {
		fake.Put("/home/kfncspot/blog/"+f, "x")
	}
	p := delProject(t, e, "ساب", "blog.example.com", fake.URL, true)
	if e.s.ownSubs(p) != "" {
		t.Fatal("a subdomain counted as wpauto's without an install run")
	}
	now := time.Now()
	b, _ := json.Marshal(engine.Run{ID: "inst", WorkflowID: "wp-install", Title: "x", HostID: "gone", Status: engine.StatusSuccess,
		StartedAt: now, EndedAt: now, Inputs: map[string]string{"mode": "subdomain", "domain": "example.com", "name": "blog"},
		Steps: []engine.StepState{{ID: "subdomain", Status: engine.StatusSuccess, Outputs: map[string]string{"host": "blog.example.com"}}}})
	if err := os.WriteFile(filepath.Join(config.Dir(), "runs", "inst.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg2, pass2, _ := config.New(1)
	cfg2.BasePath = e.s.cfg.BasePath
	s2, err := New(cfg2, "test")
	if err != nil {
		t.Fatal(err)
	}
	e2 := loginEnv(t, s2, pass2)
	if got := s2.ownSubs(p); got != "blog.example.com" {
		t.Fatalf("ownSubs = %q", got)
	}
	r, _ := e2.post("projects/"+p.ID+"/delete", url.Values{"csrf": {csrfOf(t, e2.page(""))}, "purge": {"1"}, "sure": {"1"}, "wipe_site": {"1"}, "back": {s2.cfg.BasePath}})
	id := runIDOf(r.Request.URL.Path)
	if id == "" || e2.waitRun(id) != "success" {
		t.Fatalf("wipe: %s", r.Request.URL)
	}
	if !fake.Called("api2:SubDomain/delsubdomain") || len(fake.Under("/home/kfncspot/blog")) != 0 {
		t.Fatalf("own subdomain not removed: %s", hostCalls(fake))
	}
	if _, ok := s2.projects.Get(p.ID); ok {
		t.Fatal("project kept")
	}
}

// Profiles are picked from the cPanel address, never from a form.
func TestHostProfileAuto(t *testing.T) {
	e := newBareEnv(t)
	for _, c := range []struct{ addr, posted, want string }{
		{"https://autopilo.ir:2083", "", "autopilo"},
		{"https://cp.autopilo.ir:2083", "generic", "autopilo"}, // admin pages may name one; projects pass ""
		{"https://example.com:2083", "", "generic"},
		{"https://127.0.0.1:1234", "", "fake"},
	} {
		in, err := e.s.parseHostInput(c.addr, "u", "x", "", true)
		if err != nil || in.Profile != c.want {
			t.Errorf("%s: %q %v", c.addr, in.Profile, err)
		}
	}
	if in, _ := e.s.parseHostInput("https://example.com:2083", "u", "x", "fake", true); in.Profile != "fake" {
		t.Error("an admin's named profile is not kept")
	}
	if p := profile.ForAddr(e.s.profiles(), "https://autopilo.ir"); p == nil || p.ID != "autopilo" {
		t.Error("builtin autopilo match")
	}
	// creating a project with a host: a posted profile is ignored; the summary shows the chosen one
	fake := cpaneltest.New(t, "kfncspot", delSecret)
	t.Cleanup(fake.Close)
	r, _ := e.homeCreate(url.Values{"name": {"خودکار"}, "domain": {"auto.example.com"}, "host_url": {fake.URL},
		"host_user": {"kfncspot"}, "host_secret": {delSecret}, "host_profile": {"autopilo"}})
	_ = e.waitRun(r.Request.URL.Query().Get("run"))
	p, _ := projectNamed(e, "خودکار")
	h, _ := e.s.store.Get(p.HostID)
	if h.ProfileID != "fake" {
		t.Fatalf("profile %q", h.ProfileID)
	}
	if page := e.page("p/" + p.ID + "/host"); !strings.Contains(page, `<dd id="hostprofile" title="خودکار از روی آدرس cPanel">fake</dd>`) || strings.Contains(page, "<select") {
		t.Error("host summary does not show the chosen profile read-only")
	}
}
