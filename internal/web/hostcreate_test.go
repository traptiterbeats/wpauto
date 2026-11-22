package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/traptiterbeats/wpauto/internal/cpanel/cpaneltest"
	"github.com/traptiterbeats/wpauto/internal/projects"
)

// homeCreate posts the Home «پروژه‌ی تازه» form (with the project's host).
func (e *homeEnv) homeCreate(v url.Values) (*http.Response, string) {
	e.t.Helper()
	v.Set("csrf", csrfOf(e.t, e.page("")))
	v.Set("back", e.s.cfg.BasePath)
	v.Set("with_host", "1")
	return e.post("projects/new", v)
}

func projectNamed(e *homeEnv, name string) (projects.Project, bool) {
	for _, p := range e.s.projects.List() {
		if p.Name == name {
			return p, true
		}
	}
	return projects.Project{}, false
}

// G: the host is defined when the project is made, saved as its own record and checked at once.
func TestCreateProjectWithHost(t *testing.T) {
	e := newBareEnv(t)
	bp := e.s.cfg.BasePath
	const secret = "s3cret-Pass"
	fake := cpaneltest.New(t, "kfncspot", secret)
	defer fake.Close()

	home := e.page("")
	if !strings.Contains(home, `:2083`) || !strings.Contains(home, `id="np-hosturl"`) {
		t.Error("home form lacks the prefilled cPanel address logic")
	}
	r, body := e.homeCreate(url.Values{"name": {"احمد"}, "domain": {"ahmad.example.com"}, "host_url": {fake.URL},
		"host_user": {"kfncspot"}, "host_secret": {secret}, "host_profile": {"fake"}})
	p, ok := projectNamed(e, "احمد")
	if !ok {
		t.Fatal("project not created")
	}
	if r.Request.URL.Path != bp+"p/"+p.ID+"/host" || r.Request.URL.Query().Get("run") == "" {
		t.Fatalf("landed on %s", r.Request.URL)
	}
	if !strings.Contains(body, `id="hostrun"`) || !strings.Contains(body, `id="hostsummary"`) || strings.Contains(body, secret) {
		t.Fatal("host page does not show the live check (or echoes the password)")
	}
	if st := e.waitRun(r.Request.URL.Query().Get("run")); st != "success" {
		t.Fatalf("host check: %s", st)
	}
	h, ok := e.s.store.Get(p.HostID)
	if !ok || h.ProjectID != p.ID || h.Username != "kfncspot" || h.SealedSecret == "" || h.SealedSecret == secret || h.Name != "example.com" {
		t.Fatalf("host record not the project's own: %+v", h)
	}
	if h.LastReport == nil || !h.LastReport.ReadyForWP {
		t.Fatal("check report not saved")
	}
	page := e.page("p/" + p.ID + "/host")
	if strings.Contains(page, `id="hostform"`) || strings.Contains(page, `name="url"`) || strings.Contains(page, `name="username"`) ||
		!strings.Contains(page, "به‌روزرسانی رمز/توکن") || !strings.Contains(page, "بررسی دوباره") || strings.Contains(page, "ویرایش اطلاعات اتصال") {
		t.Error("host is not fixed on its page")
	}
	sameCSRF(t, "host page", page)
	csrf := csrfOf(t, page)

	// URL and user cannot be changed afterwards: posted values are ignored, the password is updated
	r, _ = e.post("p/"+p.ID+"/host/save", url.Values{"csrf": {csrf}, "url": {"https://evil.test:2083"}, "username": {"other"}, "profile": {"generic"}, "secret": {secret}})
	if st := e.waitRun(r.Request.URL.Query().Get("run")); st != "success" {
		t.Fatalf("check after password update: %s", st)
	}
	h2, _ := e.s.store.Get(h.ID)
	if h2.URL != h.URL || h2.Username != h.Username || h2.ProfileID != h.ProfileID || len(e.s.store.List()) != 1 {
		t.Fatal("URL/user/profile changed or a record added")
	}
	if h2.SealedSecret == h.SealedSecret {
		t.Fatal("password not updated")
	}
	if pp, _ := e.s.projects.Get(p.ID); pp.HostID != h.ID {
		t.Fatal("project host changed")
	}
	// the old hosts page cannot change a project's user either
	_, _ = e.post("hosts/"+h.ID+"/edit", url.Values{"csrf": {csrf}, "username": {"other"}, "profile": {"generic"}})
	if h3, _ := e.s.store.Get(h.ID); h3.Username != "kfncspot" || h3.ProfileID != h.ProfileID {
		t.Fatal("hosts/{id}/edit changed a project's fixed host")
	}

	// a second project on the same domain gets an independent record
	r, _ = e.homeCreate(url.Values{"name": {"سهند"}, "domain": {"sah.example.com"}, "host_url": {fake.URL},
		"host_user": {"kfncspot"}, "host_secret": {secret}, "host_profile": {"fake"}})
	if st := e.waitRun(r.Request.URL.Query().Get("run")); st != "success" {
		t.Fatalf("second check: %s", st)
	}
	p2, _ := projectNamed(e, "سهند")
	o, ok := e.s.store.Get(p2.HostID)
	if !ok || o.ID == h.ID || o.ProjectID != p2.ID || len(e.s.store.List()) != 2 {
		t.Fatal("second project shares the first one's host")
	}
	r, _ = e.post("p/"+p2.ID+"/host/save", url.Values{"csrf": {csrf}, "secret": {"rotated"}})
	_ = e.waitRun(r.Request.URL.Query().Get("run"))
	if h4, _ := e.s.store.Get(h.ID); h4.SealedSecret != h2.SealedSecret {
		t.Fatal("rotating the second project's password touched the first")
	}
}

// Invalid creation input re-shows the form with the typed values (never the password), nothing saved.
func TestCreateProjectWithHostInvalid(t *testing.T) {
	e := newBareEnv(t)
	const secret = "Never-Echo-42"
	for _, c := range []struct {
		name string
		v    url.Values
		want string
	}{
		{"no secret", url.Values{"name": {"Naranj"}, "domain": {"naranj.example.com"}, "host_url": {"https://example.com:2083"}, "host_user": {"naruser"}}, "رمز عبور یا API Token لازم است"},
		{"no user", url.Values{"name": {"Naranj"}, "domain": {"naranj.example.com"}, "host_url": {"https://example.com:2083"}, "host_secret": {secret}}, "آدرس cPanel و نام کاربری لازم است"},
		{"bad url", url.Values{"name": {"Naranj"}, "domain": {"naranj.example.com"}, "host_url": {"ht!tp://::bad"}, "host_user": {"naruser"}, "host_secret": {secret}}, "آدرس cPanel نامعتبر است"},
		{"no domain", url.Values{"name": {"Naranj"}, "host_url": {"https://example.com:2083"}, "host_user": {"naruser"}, "host_secret": {secret}}, "آدرس سایت را بنویسید"},
		{"bad domain", url.Values{"name": {"Naranj"}, "domain": {"a.b.example.com"}, "host_url": {"https://example.com:2083"}, "host_user": {"naruser"}, "host_secret": {secret}}, ""},
	} {
		r, body := e.homeCreate(c.v)
		if r.StatusCode != http.StatusUnprocessableEntity || r.Request.URL.Path != e.s.cfg.BasePath+"projects/new" {
			t.Errorf("%s: %d %s", c.name, r.StatusCode, r.Request.URL)
		}
		if !strings.Contains(body, `id="np-error"`) || (c.want != "" && !strings.Contains(body, c.want)) {
			t.Errorf("%s: error not shown", c.name)
		}
		if strings.Contains(body, secret) {
			t.Errorf("%s: password echoed", c.name)
		}
		for _, k := range []string{"name", "domain", "host_user"} {
			if v := c.v.Get(k); v != "" && !strings.Contains(body, `value="`+v+`"`) {
				t.Errorf("%s: typed %s not kept", c.name, k)
			}
		}
		if len(e.s.projects.List()) != 0 || len(e.s.store.List()) != 0 {
			t.Fatalf("%s: something was saved", c.name)
		}
		sameCSRF(t, c.name, body)
	}
}

// A legacy project without a host gets the inline form once; after saving, the host is fixed.
func TestLegacyProjectHostOnce(t *testing.T) {
	e := newBareEnv(t)
	const secret = "s3cret-Pass"
	fake := cpaneltest.New(t, "kfncspot", secret)
	defer fake.Close()
	_, p := e.create("قدیمی", "old.example.com", "") // no host (as made before beta.4 / by migration)
	if p.HostID != "" {
		t.Fatal("legacy project has a host")
	}
	page := e.page("p/" + p.ID + "/host")
	if !strings.Contains(page, `id="hostform"`) || !strings.Contains(page, `name="url"`) || !strings.Contains(page, `value="https://example.com:2083"`) {
		t.Fatal("legacy project lacks the one-time form")
	}
	csrf := csrfOf(t, page)
	r, _ := e.post("p/"+p.ID+"/host/save", url.Values{"csrf": {csrf}, "profile": {"fake"}, "url": {fake.URL}, "username": {"kfncspot"}, "secret": {secret}})
	if st := e.waitRun(r.Request.URL.Query().Get("run")); st != "success" {
		t.Fatalf("check: %s", st)
	}
	p, _ = e.s.projects.Get(p.ID)
	h, ok := e.s.store.Get(p.HostID)
	if !ok || h.ProjectID != p.ID {
		t.Fatal("host not saved as the project's own")
	}
	page = e.page("p/" + p.ID + "/host")
	if strings.Contains(page, `id="hostform"`) || strings.Contains(page, `name="url"`) || !strings.Contains(page, `id="hostsecret"`) {
		t.Fatal("form still offered after the host was saved")
	}
	r, _ = e.post("p/"+p.ID+"/host/save", url.Values{"csrf": {csrf}, "profile": {"generic"}, "url": {"https://other.test:2083"}, "username": {"x"}, "secret": {secret}})
	_ = e.waitRun(r.Request.URL.Query().Get("run"))
	if h2, _ := e.s.store.Get(h.ID); h2.URL != h.URL || h2.Username != "kfncspot" || len(e.s.store.List()) != 1 {
		t.Fatal("legacy host changed after it was fixed")
	}
}
