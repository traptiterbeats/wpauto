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
	for p, want := range map[string]string{"": "کارها", "hosts": "هاست‌ها", "themes": "قالب‌ها", "customize": "شخصی‌سازی", "ai": "هوش مصنوعی", "runs": "اجراها"} {
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
	// bad back targets never leave the panel
	if s.safeBack("//evil.com") != "" || s.safeBack("https://evil.com") != "" || s.safeBack(cfg.BasePath+"runs") == "" {
		t.Fatal("safeBack")
	}
}
