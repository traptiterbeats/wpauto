package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/traptiterbeats/wpauto/internal/config"
	"github.com/traptiterbeats/wpauto/internal/cpanel/cpaneltest"
	"github.com/traptiterbeats/wpauto/internal/projects"
	"github.com/traptiterbeats/wpauto/internal/store"
)

// newBareEnv is a logged-in panel with no hosts, and a "fake" profile that trusts the fake
// cPanel's self-signed certificate.
func newBareEnv(t *testing.T) *homeEnv {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("WPAUTO_DIR", dir)
	_ = os.MkdirAll(filepath.Join(dir, "profiles"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "profiles", "fake.yaml"), []byte("id: fake\nname: fake\ninsecure_tls: true\n"), 0o600)
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
	return e
}

func (e *homeEnv) post(path string, v url.Values) (*http.Response, string) {
	e.t.Helper()
	r, err := e.cl.PostForm(e.base+path, v)
	if err != nil {
		e.t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	return r, string(b)
}

func (e *homeEnv) waitRun(id string) string {
	e.t.Helper()
	var st struct {
		Status string `json:"status"`
		Done   bool   `json:"done"`
	}
	for i := 0; i < 300 && !st.Done; i++ {
		time.Sleep(20 * time.Millisecond)
		r, err := e.cl.Get(e.base + "runs/" + id + "/json")
		if err != nil {
			e.t.Fatal(err)
		}
		_ = json.NewDecoder(r.Body).Decode(&st)
		r.Body.Close()
	}
	// OnDone (project + host updates) runs right after the status is final
	time.Sleep(50 * time.Millisecond)
	return st.Status
}

func TestProjectHostInlineFormWhenNoHost(t *testing.T) {
	e := newBareEnv(t)
	bp := e.s.cfg.BasePath
	_, p := e.create("احمد", "ahmad.autopilo.ir", "")
	if p.HostID != "" {
		t.Fatal("a host was linked out of nowhere")
	}
	page := e.page("p/" + p.ID + "/host")
	for _, want := range []string{`id="hostform"`, `action="` + bp + `p/` + p.ID + `/host/save"`,
		`name="url"`, `value="https://autopilo.ir:2083"`, `name="username"`, `name="secret" type="password"`, `name="profile"`,
		"ذخیره و بررسی", "هاست این پروژه هنوز وصل نشده",
		`<a href="` + bp + `p/` + p.ID + `/wordpress">وردپرس</a>`, `<strong aria-current="page">بررسی هاست</strong>`,
		`href="` + bp + `p/` + p.ID + `/wordpress" id="backbtn"`} {
		if !strings.Contains(page, want) {
			t.Errorf("host page lacks %q", want)
		}
	}
	if strings.Contains(page, `id="hostsummary"`) {
		t.Error("summary shown without a host")
	}
	sameCSRF(t, "project host", page)

	// the WordPress page sends the host card to this page and marks the host tasks
	wp := e.page("p/" + p.ID + "/wordpress")
	for _, want := range []string{`value="` + bp + `p/` + p.ID + `/host"`, "اول بررسی هاست", `class="small muted needhost"`,
		"هاست این پروژه هنوز وصل نشده"} {
		if !strings.Contains(wp, want) {
			t.Errorf("wordpress page lacks %q", want)
		}
	}
	// a task that needs the host shows a link to the project's host check instead of its form
	_ = e.s.projects.SetActive(p.ID)
	for _, path := range []string{"run/wp-check-domain?project=" + p.ID, "run/wp-install?project=" + p.ID + "&host=",
		"run/theme-install?project=" + p.ID, "autopilot?project=" + p.ID, "wordpress?project=" + p.ID} {
		pg := e.page(path)
		if !strings.Contains(pg, `id="needhost"`) || !strings.Contains(pg, `href="`+bp+`p/`+p.ID+`/host"`) {
			t.Errorf("%s: no message linking to the project's host check", path)
		}
		if strings.Contains(pg, `id="runform"`) {
			t.Errorf("%s: run form shown without a host", path)
		}
		sameCSRF(t, path, pg)
	}
	// re-checking without a host goes back to the form with a message
	r, _ := e.post("p/"+p.ID+"/host/check", url.Values{"csrf": {csrfOf(t, page)}})
	if r.Request.URL.Path != bp+"p/"+p.ID+"/host" || !strings.Contains(r.Request.URL.RawQuery, "err=") {
		t.Fatalf("check without host: %s", r.Request.URL)
	}
	// csrf is required
	if r, _ := e.post("p/"+p.ID+"/host/save", url.Values{"url": {"x"}}); r.StatusCode != 400 {
		t.Fatal("csrf not enforced on host save")
	}
	if _, r := e.get("p/nope/host"); r.StatusCode != 404 {
		t.Fatal("unknown project host page")
	}
}

func TestProjectHostSaveCheckAndReuse(t *testing.T) {
	e := newBareEnv(t)
	bp := e.s.cfg.BasePath
	const secret = "s3cret-Pass"
	fake := cpaneltest.New(t, "kfncspot", secret)
	defer fake.Close()

	_, p := e.create("احمد", "ahmad.example.com", "")
	page := e.page("p/" + p.ID + "/host")
	csrf := csrfOf(t, page)

	// a missing password for a new host is refused, nothing is saved
	r, _ := e.post("p/"+p.ID+"/host/save", url.Values{"csrf": {csrf}, "profile": {"fake"}, "url": {fake.URL}, "username": {"kfncspot"}})
	if len(e.s.store.List()) != 0 || !strings.Contains(r.Request.URL.RawQuery, "err=") {
		t.Fatal("host saved without a password")
	}

	r, body := e.post("p/"+p.ID+"/host/save", url.Values{"csrf": {csrf}, "profile": {"fake"}, "url": {fake.URL},
		"username": {"kfncspot"}, "secret": {secret}})
	if r.Request.URL.Path != bp+"p/"+p.ID+"/host" {
		t.Fatalf("save landed on %s", r.Request.URL)
	}
	runID := r.Request.URL.Query().Get("run")
	if runID == "" || !strings.Contains(body, `id="hostrun"`) || !strings.Contains(body, "ذخیره شد") {
		t.Fatal("the check did not start on the same page")
	}
	if st := e.waitRun(runID); st != "success" {
		t.Fatalf("host check: %s", st)
	}
	hosts := e.s.store.List()
	if len(hosts) != 1 || hosts[0].SealedSecret == secret || hosts[0].SealedSecret == "" || hosts[0].Name != "example.com" {
		t.Fatalf("host not stored as expected: %d hosts", len(hosts))
	}
	h := hosts[0]
	if pp, _ := e.s.projects.Get(p.ID); pp.HostID != h.ID {
		t.Fatal("host not linked to the project")
	}
	if h.LastReport == nil || !h.LastReport.ReadyForWP || h.LastReport.MainDomain != "example.com" {
		t.Fatal("check report not saved on the host")
	}

	page = e.page("p/" + p.ID + "/host?run=" + runID)
	for _, want := range []string{`id="hostsummary"`, `id="hostrun"`, `id="log"`, "نتیجه‌ی بررسی", "آماده برای وردپرس", fake.URL,
		"kfncspot", "بررسی دوباره", "ویرایش اطلاعات اتصال", `action="` + bp + `p/` + p.ID + `/host/check"`,
		`href="` + bp + `runs/` + runID + `"`, "ادامه: کارهای وردپرس"} {
		if !strings.Contains(page, want) {
			t.Errorf("host page after check lacks %q", want)
		}
	}
	if strings.Contains(page, secret) || strings.Contains(page, h.SealedSecret) {
		t.Fatal("the password is echoed back")
	}
	if strings.Contains(page, `id="hostform"`) {
		t.Error("empty form shown although a host is linked")
	}
	sameCSRF(t, "host page", page)

	// the host stage is done and the next cards use the project's host
	wp := e.page("p/" + p.ID + "/wordpress")
	if strings.Contains(wp, "اول بررسی هاست") || !strings.Contains(wp, "✔ بررسی هاست") ||
		!strings.Contains(wp, `value="`+bp+`run/wp-check-domain?project=`+p.ID+`&amp;host=`+h.ID+`"`) {
		t.Error("wordpress page does not use the checked host")
	}
	form := e.page("run/wp-check-domain?project=" + p.ID + "&host=" + h.ID)
	if !strings.Contains(form, `id="runform"`) || !strings.Contains(form, `<option value="`+h.ID+`" selected>`) {
		t.Error("task form does not preselect the project's host")
	}

	// check again
	r, _ = e.post("p/"+p.ID+"/host/check", url.Values{"csrf": {csrf}})
	run2 := r.Request.URL.Query().Get("run")
	if run2 == "" || run2 == runID {
		t.Fatal("re-check did not start a new run")
	}
	if st := e.waitRun(run2); st != "success" {
		t.Fatalf("re-check: %s", st)
	}

	// editing with an empty password keeps the saved one
	r, _ = e.post("p/"+p.ID+"/host/save", url.Values{"csrf": {csrf}, "profile": {"fake"}, "url": {fake.URL}, "username": {"kfncspot"}})
	if hh, _ := e.s.store.Get(h.ID); hh.SealedSecret != h.SealedSecret || len(e.s.store.List()) != 1 {
		t.Fatal("edit without a password changed or duplicated the host")
	}
	if st := e.waitRun(r.Request.URL.Query().Get("run")); st != "success" {
		t.Fatalf("check after edit: %s", st)
	}

	// a second project on the same main domain takes the host automatically
	r2, p2 := e.create("سهند", "sah.example.com", "")
	if p2.HostID != h.ID {
		t.Fatal("host not reused for a project on the same main domain")
	}
	if msg := r2.Request.URL.Query().Get("msg"); !strings.Contains(msg, "هاست example.com از پروژه‌ی دیگر استفاده شد") {
		t.Fatalf("reuse not announced: %q", msg)
	}
	page = e.page("p/" + p2.ID + "/host")
	if !strings.Contains(page, `id="hostsummary"`) || !strings.Contains(page, "احمد") {
		t.Error("second project's host page should show the shared host")
	}
	// a project on another domain gets nothing
	if _, p3 := e.create("other", "other.org", ""); p3.HostID != "" {
		t.Fatal("unrelated domain linked to a host")
	}
}

func TestAutoLinkHostByMainDomain(t *testing.T) {
	e := newBareEnv(t)
	// a host saved for ahmad.autopilo.ir that was never checked: its cPanel address names the domain
	h, _ := e.s.store.Add(store.Host{Name: "autopilo.ir", ProfileID: "generic", URL: "https://autopilo.ir:2083", Username: "u"})
	a, _ := e.s.projects.Add(projects.Project{Name: "ahmad", Domain: "ahmad.autopilo.ir", Mode: kindSubdomain, HostID: h.ID})
	r, p := e.create("sah", "sah.autopilo.ir", "")
	if p.HostID != h.ID {
		t.Fatal("host of the same main domain not linked")
	}
	if msg := r.Request.URL.Query().Get("msg"); !strings.Contains(msg, "هاست autopilo.ir از پروژه‌ی دیگر استفاده شد") {
		t.Fatalf("message: %q", msg)
	}
	if pg, _ := e.get(r.Request.URL.String()[len(e.base):]); !strings.Contains(pg, "هاست autopilo.ir از پروژه‌ی دیگر استفاده شد") {
		t.Error("message not shown after creating")
	}
	// an older project without a host gets linked when its pages open
	old, _ := e.s.projects.Add(projects.Project{Name: "old", Domain: "autopilo.ir", Mode: kindRoot})
	wp := e.page("p/" + old.ID + "/wordpress")
	if o, _ := e.s.projects.Get(old.ID); o.HostID != h.ID || !strings.Contains(wp, "از پروژه‌ی دیگر استفاده شد") {
		t.Error("older project not linked on its WordPress page")
	}
	_ = a
	// unit: two hosts on the same main domain are not guessed
	_, _ = e.s.store.Add(store.Host{Name: "x", ProfileID: "generic", URL: "https://cpanel.autopilo.ir:2083", Username: "v"})
	if _, ok := hostForSite(e.s.store.List(), siteAddr{Kind: kindSubdomain, Domain: "z.autopilo.ir", Parent: "autopilo.ir"}); ok {
		t.Fatal("ambiguous host picked")
	}
	if hostMain(store.Host{URL: "https://1.2.3.4:2083", Name: "n"}) != "n" {
		t.Fatal("an IP is not a main domain")
	}
}

func TestToolsPageWithoutHosts(t *testing.T) {
	e := newBareEnv(t)
	tools := e.page("tools")
	if strings.Contains(tools, "هاست‌ها") || strings.Contains(tools, `href="`+e.s.cfg.BasePath+`hosts"`) {
		t.Fatal("tools page still lists هاست‌ها")
	}
	for _, tl := range tools2() {
		if tl.Path == "hosts" {
			t.Fatal("hosts still in the tools registry")
		}
	}
	// the old pages still answer
	e.page("hosts")
}

func tools2() []tool { return tools }

func TestWordPressCardGroups(t *testing.T) {
	by := map[string]*pipeStep{}
	for _, k := range pipelineKeys {
		by[k] = &pipeStep{Key: k, Link: "/x/" + k, State: "todo"}
	}
	keys := func(g wpGroup) (out []string) {
		for _, t := range g.Tasks {
			out = append(out, t.Key)
		}
		return
	}
	gs := wpTaskGroups(wpCtx{bp: "/b/", p: projects.Project{ID: "p1"}, by: by}, false)
	if len(gs) != 2 || gs[0].Title != "راه‌اندازی" || gs[1].Title != "سایت وردپرسی" {
		t.Fatalf("groups: %+v", gs)
	}
	if got := strings.Join(keys(gs[0]), ","); got != "host,wpcheck,wpinstall,autopilot" {
		t.Fatalf("setup cards: %s", got)
	}
	if got := strings.Join(keys(gs[1]), ","); got != "customize,theme,wplist,plugins,themes,seo" {
		t.Fatalf("site cards: %s", got)
	}
	soon := 0
	for _, g := range gs {
		for _, c := range g.Tasks {
			if c.Soon {
				soon++
				if c.Link != "" || c.NeedsHost {
					t.Error("a «به‌زودی» card has a link")
				}
			}
			if c.Key == "host" && (c.NeedsHost || c.Link != "/x/host") {
				t.Error("the host card itself must not wait for a host")
			}
			if c.Key == "customize" && c.NeedsHost {
				t.Error("customize does not need the host")
			}
			if (c.Key == "wpcheck" || c.Key == "theme" || c.Key == "autopilot" || c.Key == "wplist") && !c.NeedsHost {
				t.Errorf("%s should wait for the host", c.Key)
			}
		}
	}
	if soon != 3 || !gs[0].NeedsHost || !gs[1].NeedsHost {
		t.Fatal("soon cards or group host notes")
	}
	gs = wpTaskGroups(wpCtx{bp: "/b/", p: projects.Project{ID: "p1", HostID: "h"}, by: by}, true)
	for _, g := range gs {
		if g.NeedsHost {
			t.Fatal("host notes shown with a host")
		}
	}
	if gs[1].Tasks[2].Link != "/b/wordpress?project=p1&host=h" || gs[0].Tasks[3].Link != "/b/autopilot?project=p1" {
		t.Fatalf("links: %s %s", gs[1].Tasks[2].Link, gs[0].Tasks[3].Link)
	}
}
