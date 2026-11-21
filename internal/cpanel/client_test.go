package cpanel

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/traptiterbeats/wpauto/internal/cpanel/cpaneltest"
)

func TestPreflight(t *testing.T) {
	srv := cpaneltest.New(t, "kfncspot", "secret")
	defer srv.Close()
	c, err := New(Options{BaseURL: srv.URL, Username: "kfncspot", Secret: "secret", InsecureTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	rep := Preflight(context.Background(), c, nil, []string{"8.3", "8.4"})
	if !rep.LoginOK || !rep.ReadyForWP {
		t.Fatalf("not ready: %+v", rep)
	}
	if rep.PreferredPHP != "8.3" || rep.DBPrefix != "kfncspot_" || rep.MainDomain != "example.com" {
		t.Fatalf("bad report: %+v", rep)
	}
	if rep.APITokens {
		t.Fatal("tokens should be reported unavailable")
	}
	if rep.SitePHP["example.com"] != "ea-php85" {
		t.Fatalf("site php: %v", rep.SitePHP)
	}
}

func TestBadLogin(t *testing.T) {
	srv := cpaneltest.New(t, "u", "p")
	defer srv.Close()
	c, _ := New(Options{BaseURL: srv.URL, Username: "u", Secret: "wrong", InsecureTLS: true})
	if err := c.Login(context.Background()); err != ErrInvalidLogin {
		t.Fatalf("want ErrInvalidLogin, got %v", err)
	}
}

func TestNormalizeURL(t *testing.T) {
	cases := map[string]string{
		"autopilo.ir":                    "https://autopilo.ir:2083",
		"https://autopilo.ir:2083/":      "https://autopilo.ir:2083",
		"http://autopilo.ir/cpanel":      "http://autopilo.ir:2083",
		"autopilo.ir:2082":               "http://autopilo.ir:2082",
		" https://host.example.com:2083": "https://host.example.com:2083",
	}
	for in, want := range cases {
		got, err := NormalizeURL(in, 2083)
		if err != nil || got != want {
			t.Errorf("%q => %q (%v), want %q", in, got, err, want)
		}
	}
}

func newFakeClient(t *testing.T) (*Client, *cpaneltest.Fake) {
	t.Helper()
	srv := cpaneltest.New(t, "kfncspot", "secret")
	t.Cleanup(srv.Close)
	c, err := New(Options{BaseURL: srv.URL, Username: "kfncspot", Secret: "secret", InsecureTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c, srv
}

func TestExtract(t *testing.T) {
	ctx := context.Background()
	c, srv := newFakeClient(t)
	dest := "/home/kfncspot/site/.wpauto-x"
	if err := c.Extract(ctx, "/home/kfncspot/site/.wpauto-wp.zip", dest); err == nil || !strings.Contains(err.Error(), "archive not found") {
		t.Fatalf("missing archive: per-item error not reported: %v", err)
	}
	srv.Put("/home/kfncspot/site/.wpauto-wp.zip", "zip")
	if err := c.Extract(ctx, "/home/kfncspot/site/.wpauto-wp.zip", dest); err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.File(dest + "/wordpress/wp-load.php"); !ok {
		t.Fatalf("not extracted: %v", srv.Under(dest))
	}
	if ok, err := c.Exists(ctx, dest); !ok || err != nil {
		t.Fatalf("Exists(%s) = %v, %v", dest, ok, err)
	}
	if ok, err := c.Exists(ctx, "/home/kfncspot/site/nothing"); ok || err != nil {
		t.Fatalf("Exists(missing) = %v, %v", ok, err)
	}
	if ok, err := c.Exists(ctx, "/home/kfncspot/gone/deeper/x"); ok || err != nil {
		t.Fatalf("Exists under a missing folder = %v, %v", ok, err)
	}
	srv.Fail["api2:Fileman/fileop:extract"] = true
	if err := c.Extract(ctx, "/home/kfncspot/site/.wpauto-wp.zip", dest); err == nil {
		t.Fatal("forced failure not reported")
	}
}

func TestRemoveAll(t *testing.T) {
	ctx := context.Background()
	fill := func(f *cpaneltest.Fake) {
		for _, p := range []string{"/home/kfncspot/s/a.php", "/home/kfncspot/s/wp-admin/x.php", "/home/kfncspot/s/wp-admin/css/y.css", "/home/kfncspot/keep/z"} {
			f.Put(p, "x")
		}
	}
	cases := []struct {
		name   string
		setup  func(*cpaneltest.Fake)
		remain bool
		used   string
	}{
		{"recursive unlink", func(*cpaneltest.Fake) {}, false, ""},
		{"delete_file", func(f *cpaneltest.Fake) { f.ShallowUnlink = true }, false, "Fileman/delete_file"},
		{"bottom-up", func(f *cpaneltest.Fake) { f.ShallowUnlink = true; f.Fail["Fileman/delete_file"] = true }, false, ""},
		{"stuck", func(f *cpaneltest.Fake) {
			f.Fail["api2:Fileman/fileop:unlink"] = true
			f.Fail["Fileman/delete_file"] = true
		}, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, srv := newFakeClient(t)
			fill(srv)
			tc.setup(srv)
			err := c.RemoveAll(ctx, "/home/kfncspot/s")
			var rem *RemainError
			if tc.remain {
				if !errors.As(err, &rem) || len(rem.Paths) != 1 || rem.Paths[0] != "/home/kfncspot/s" {
					t.Fatalf("want RemainError for /home/kfncspot/s, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if left := srv.Under("/home/kfncspot/s"); len(left) > 0 {
				t.Fatalf("left: %v", left)
			}
			if _, ok := srv.File("/home/kfncspot/keep/z"); !ok {
				t.Fatal("removed a sibling folder")
			}
			if tc.used != "" && !srv.Called(tc.used) {
				t.Fatalf("%s not used", tc.used)
			}
		})
	}
}
