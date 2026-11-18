package cpanel

import (
	"context"
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
