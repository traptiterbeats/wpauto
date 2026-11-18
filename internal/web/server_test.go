package web

import (
	"bytes"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/traptiterbeats/wpauto/internal/config"
)

func TestLoginFlow(t *testing.T) {
	t.Setenv("WPAUTO_DIR", t.TempDir())
	cfg, pass, err := config.New(1)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar}

	// root outside base path is hidden
	r, _ := cl.Get(ts.URL + "/")
	if r.StatusCode != 404 {
		t.Fatalf("root: %d", r.StatusCode)
	}
	// wrong password
	r, _ = cl.PostForm(ts.URL+cfg.BasePath+"login", url.Values{"username": {cfg.Username}, "password": {"nope"}})
	if r.StatusCode != 401 {
		t.Fatalf("bad login: %d", r.StatusCode)
	}
	// right password lands on dashboard
	r, err = cl.PostForm(ts.URL+cfg.BasePath+"login", url.Values{"username": {cfg.Username}, "password": {pass}})
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("login: %v %d", err, r.StatusCode)
	}
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(r.Body)
	if !strings.Contains(buf.String(), "نصب وردپرس") {
		t.Fatal("dashboard not rendered")
	}
}

func TestRateLimit(t *testing.T) {
	l := newLimiter()
	for i := 0; i < 5; i++ {
		l.fail("1.2.3.4")
	}
	if l.blocked("1.2.3.4") == 0 {
		t.Fatal("expected block after 5 failures")
	}
}

func TestHostLockAfterRejectedLogin(t *testing.T) {
	t.Setenv("WPAUTO_DIR", t.TempDir())
	logins := 0
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logins++
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"status":0,"message":"invalid_login"}`))
	}))
	defer fake.Close()

	cfg, pass, _ := config.New(1)
	s, err := New(cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar}
	bp := ts.URL + cfg.BasePath
	if r, _ := cl.PostForm(bp+"login", url.Values{"username": {cfg.Username}, "password": {pass}}); r.StatusCode != 200 {
		t.Fatal("login failed")
	}
	u, _ := url.Parse(ts.URL + cfg.BasePath)
	var csrf string
	for _, c := range jar.Cookies(u) {
		if c.Name == cookieName {
			csrf = s.sign("csrf:" + c.Value)[:32]
		}
	}
	// add host
	r, err := cl.PostForm(bp+"hosts/add", url.Values{"csrf": {csrf}, "profile": {"generic"}, "url": {fake.URL},
		"username": {"u"}, "secret": {"wrong"}})
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("add: %v %d", err, r.StatusCode)
	}
	hosts := s.store.List()
	if len(hosts) != 1 || hosts[0].SealedSecret == "wrong" {
		t.Fatalf("host not stored encrypted: %+v", hosts)
	}
	id := hosts[0].ID
	// two tests: only the first may reach cPanel
	for i := 0; i < 2; i++ {
		if _, err := cl.PostForm(bp+"hosts/"+id+"/test", url.Values{"csrf": {csrf}}); err != nil {
			t.Fatal(err)
		}
	}
	if logins != 1 {
		t.Fatalf("expected exactly 1 login attempt, got %d", logins)
	}
	h, _ := s.store.Get(id)
	if h.FailedLogins != 1 || h.LastReport == nil || h.LastReport.LoginOK {
		t.Fatalf("unexpected host state: %+v", h)
	}
	for _, page := range []string{"hosts", "hosts/" + id, ""} {
		r, _ := cl.Get(bp + page)
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(r.Body)
		if r.StatusCode != 200 || !strings.Contains(b.String(), "</html>") {
			t.Fatalf("page %q did not render fully: %d", page, r.StatusCode)
		}
	}
	// missing csrf is rejected
	r, _ = cl.PostForm(bp+"hosts/"+id+"/delete", url.Values{})
	if r.StatusCode != 400 {
		t.Fatalf("csrf not enforced: %d", r.StatusCode)
	}
}
