package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/traptiterbeats/wpauto/internal/config"
	"github.com/traptiterbeats/wpauto/internal/projects"
	"github.com/traptiterbeats/wpauto/internal/store"
)

func TestLayoutAndPlan(t *testing.T) {
	t.Setenv("WPAUTO_DIR", t.TempDir())
	cfg, pass, _ := config.New(1)
	s, err := New(cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar}
	base := ts.URL + cfg.BasePath
	if _, err := cl.PostForm(base+"login", url.Values{"username": {cfg.Username}, "password": {pass}}); err != nil {
		t.Fatal(err)
	}
	get := func(p string) string {
		t.Helper()
		r, err := cl.Get(base + p)
		if err != nil || r.StatusCode != 200 {
			t.Fatalf("GET %s: %v %v", p, err, r.Status)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		return string(b)
	}
	// every inner page: slim breadcrumb ending in its own title, a Back button, the tools menu
	for p, want := range map[string]string{"tasks": "همه‌ی کارها", "hosts": "هاست‌ها", "themes": "مدیریت قالب‌ها", "customize": "شخصی‌سازی", "ai": "هوش مصنوعی", "runs": "اجراها", "projects": "پروژه‌ها", "wordpress": "وردپرس‌های هاست", "tools": "ابزارها"} {
		page := get(p)
		if !strings.Contains(page, `<strong aria-current="page">`+want+`</strong>`) {
			t.Errorf("%q: breadcrumb %q missing", p, want)
		}
		if !strings.Contains(page, `class="trail"`) || !strings.Contains(page, `id="backbtn"`) || !strings.Contains(page, `class="toolmenu"`) {
			t.Errorf("%q: breadcrumb, back button or tools menu missing", p)
		}
		if strings.Contains(page, `class="side"`) && p != "tools" {
			t.Errorf("%q: old fixed side panel still shown", p)
		}
	}
	if !strings.Contains(get("tools"), "وصل نیست") || !strings.Contains(get("tools"), "آمادگی پنل") {
		t.Error("tools: panel readiness (AI not connected) missing")
	}
	if wpPage := get("wordpress"); !strings.Contains(wpPage, "وردپرس‌های هاست") {
		t.Fatal("wordpress page")
	}
	// every POST form on the page carries the CSRF token
	if wpPage := get("wordpress"); strings.Count(wpPage, `method="post"`) != strings.Count(wpPage, `name="csrf"`) {
		t.Fatal("wordpress form without csrf")
	}
	if strings.Contains(get("tasks"), "run/wp-remove") {
		t.Fatal("hidden workflow listed on home")
	}
	form := get("run/wp-install")
	if !strings.Contains(form, `<strong aria-current="page">نصب وردپرس</strong>`) || !strings.Contains(form, `data-id="folder" class="pl-no"`) {
		t.Fatalf("run form: crumb or plan missing")
	}
	var plan struct {
		Steps []sideStep `json:"steps"`
	}
	if err := json.Unmarshal([]byte(get("run/wp-install/plan?in_mode=subfolder")), &plan); err != nil {
		t.Fatal(err)
	}
	st := map[string]string{}
	for _, x := range plan.Steps {
		st[x.ID] = x.State
	}
	if st["subdomain"] != "no" || st["folder"] != "yes" || st["ssl"] != "no" {
		t.Fatalf("subfolder plan: %v", st)
	}
	// a new project starts with every stage dark; the runs drawer is on every page
	if _, err := cl.PostForm(base+"projects/new", url.Values{"name": {"سیس"}, "domain": {"Example.com/Shop"}, "csrf": {csrfOf(t, get(""))}}); err != nil {
		t.Fatal(err)
	}
	home := get("tasks")
	if !strings.Contains(home, "شروع پروژه‌ی جدید") || !strings.Contains(home, "<strong>سیس</strong> <span class=\"badge ok\">فعال</span>") || !strings.Contains(home, "ادامه: بررسی هاست") || strings.Contains(home, "آخرین اجراها</h2>") || strings.Count(get("p/"+s.projectsActiveID()+"/wordpress"), `class="pp-todo"`) != 5 {
		t.Fatalf("project not active or stages not dark")
	}
	// ticking a stage by hand lights it
	p, _ := s.projects.Active()
	if _, err := cl.PostForm(base+"projects/"+p.ID+"/mark", url.Values{"stage": {"wpinstall"}, "on": {"on"}, "csrf": {csrfOf(t, home)}}); err != nil {
		t.Fatal(err)
	}
	if h := get("p/" + p.ID + "/wordpress"); strings.Count(h, `class="pp-todo"`) != 4 || !strings.Contains(h, "دستی علامت خورد") {
		t.Fatal("manual tick did not light the stage")
	}
	if _, err := s.store.Add(store.Host{Name: "h", ProfileID: "generic", URL: "https://example.com:2083", Username: "u"}); err != nil {
		t.Fatal(err)
	}
	// the host is not the project's own, so the project's task form asks for its host check first
	if f := get("run/theme-install"); !strings.Contains(f, `id="needhost"`) || strings.Contains(f, `id="runform"`) {
		t.Fatal("project task form without its own host should point to the host check")
	}
	_ = s.projects.Update(p.ID, func(x *projects.Project) { x.HostID = hs0(s) })
	if f := get("run/theme-install"); !strings.Contains(f, `example.com/shop</span> (پوشه)`) || strings.Contains(f, `name="in_domain"`) || strings.Contains(f, `name="in_folder"`) {
		t.Fatal("theme-install form does not show the project's address read-only")
	}
	// the project's WordPress page links to the site and admin once WordPress is there
	if h := get("p/" + p.ID + "/wordpress"); !strings.Contains(h, `href="https://example.com/shop/wp-admin/"`) {
		t.Fatal("no site/admin link for the project")
	}
	// disk usage shows on the hosts tab, red when nearly full
	hs := s.store.List()
	_ = s.store.Update(hs[0].ID, func(x *store.Host) { x.SetDisk(950, 1024) })
	if h := get("hosts"); !strings.Contains(h, "950 MB از 1.0 GB") || !strings.Contains(h, "پر شده") {
		t.Fatal("disk usage missing on hosts page")
	}
	// deleting a project with purge removes it and reports the count
	r, err := cl.PostForm(base+"projects/"+p.ID+"/delete", url.Values{"purge": {"on"}, "sure": {"1"}, "csrf": {csrfOf(t, home)}})
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if _, ok := s.projects.Get(p.ID); ok || !strings.Contains(r.Request.URL.RawQuery, "msg=") {
		t.Fatal("project not deleted with purge")
	}
	// bad back targets never leave the panel
	if s.safeBack("//evil.com") != "" || s.safeBack("https://evil.com") != "" || s.safeBack(cfg.BasePath+"runs") == "" {
		t.Fatal("safeBack")
	}
}

func (s *Server) projectsActiveID() string {
	p, _ := s.projects.Active()
	return p.ID
}

func csrfOf(t *testing.T, page string) string {
	t.Helper()
	i := strings.Index(page, `name="csrf" value="`)
	if i < 0 {
		t.Fatal("no csrf on page")
	}
	rest := page[i+len(`name="csrf" value="`):]
	return rest[:strings.Index(rest, `"`)]
}

func hs0(s *Server) string { return s.store.List()[0].ID }
