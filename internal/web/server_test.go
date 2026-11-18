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
