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
	for p, want := range map[string]string{"": "کارها", "hosts": "هاست‌ها", "themes": "قالب‌ها", "customize": "شخصی‌سازی", "ai": "هوش مصنوعی", "runs": "کارها", "projects": "پروژه‌ها"} {
		page := get(p)
		if !strings.Contains(page, `class="on" aria-current="page">`+want+`<`) {
			t.Errorf("%q: active menu %q missing", p, want)
		}
		if !strings.Contains(page, "شما این‌جا هستید") || !strings.Contains(page, "آمادگی پنل") {
			t.Errorf("%q: crumbs or side panel missing", p)
		}
		if !strings.Contains(page, "وصل نیست") {
			t.Errorf("%q: AI not-connected state missing", p)
		}
	}
	form := get("run/wp-install")
	if !strings.Contains(form, "<strong>نصب وردپرس</strong>") || !strings.Contains(form, `data-id="folder" class="pl-no"`) {
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
	home := get("")
	if !strings.Contains(home, "پروژه‌ی فعال: <strong>سیس</strong>") || strings.Count(home, `class="pp-todo"`) != 5 || !strings.Contains(home, `id="drawer"`) {
		t.Fatalf("project not active or stages not dark")
	}
	// ticking a stage by hand lights it
	p, _ := s.projects.Active()
	if _, err := cl.PostForm(base+"projects/"+p.ID+"/mark", url.Values{"stage": {"wpinstall"}, "on": {"on"}, "csrf": {csrfOf(t, home)}}); err != nil {
		t.Fatal(err)
	}
	if h := get(""); strings.Count(h, `class="pp-todo"`) != 4 || !strings.Contains(h, "دستی علامت خورد") {
		t.Fatal("manual tick did not light the stage")
	}
	if _, err := s.store.Add(store.Host{Name: "h", ProfileID: "generic", URL: "https://example.com:2083", Username: "u"}); err != nil {
		t.Fatal(err)
	}
	if f := get("run/theme-install"); !strings.Contains(f, `value="shop"`) || !strings.Contains(f, `value="example.com"`) {
		t.Fatal("run form does not take the project's folder")
	}
	// bad back targets never leave the panel
	if s.safeBack("//evil.com") != "" || s.safeBack("https://evil.com") != "" || s.safeBack(cfg.BasePath+"runs") == "" {
		t.Fatal("safeBack")
	}
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
