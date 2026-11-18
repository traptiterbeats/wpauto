package cpanel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// FakeServer emulates the parts of cPanel the client uses.
func FakeServer(t *testing.T, user, pass string) *httptest.Server {
	t.Helper()
	const tok = "/cpsess0123456789"
	ok := func(w http.ResponseWriter, data any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": 1, "data": data, "errors": nil})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/login/", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("user") != user || r.FormValue("pass") != pass {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"status":0,"message":"invalid_login"}`))
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "cpsession", Value: "abc", Path: "/"})
		_, _ = w.Write([]byte(`{"status":1,"security_token":"` + tok + `","redirect":"` + tok + `/frontend/jupiter/"}`))
	})
	mux.HandleFunc(tok+"/execute/", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("cpsession"); err != nil || c.Value != "abc" {
			w.WriteHeader(401)
			return
		}
		switch strings.TrimPrefix(r.URL.Path, tok+"/execute/") {
		case "Variables/get_user_information":
			ok(w, map[string]any{"user": user, "plan": "1GB", "theme": "jupiter"})
		case "DomainInfo/list_domains":
			ok(w, map[string]any{"main_domain": "example.com", "addon_domains": []string{}, "sub_domains": []string{"blog.example.com"}})
		case "LangPHP/php_get_installed_versions":
			ok(w, map[string]any{"versions": []string{"ea-php74", "ea-php83", "ea-php84", "ea-php85"}})
		case "LangPHP/php_get_vhost_versions":
			ok(w, []map[string]any{{"vhost": "example.com", "version": "ea-php85"}})
		case "Mysql/get_restrictions":
			ok(w, map[string]any{"prefix": "kfncspot_", "max_database_name_length": 64})
		case "Mysql/list_databases":
			ok(w, []any{map[string]any{"database": "kfncspot_a"}})
		case "Quota/get_quota_info":
			ok(w, map[string]any{"megabytes_used": 235.13, "megabyte_limit": 1024})
		case "Tokens/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 0, "errors": []string{"feature not enabled"}})
		case "Features/list_features":
			ok(w, map[string]any{"fileman": 1, "mysql": 1, "multiphp": 1, "ssh": 0})
		default:
			w.WriteHeader(404)
		}
	})
	mux.HandleFunc(tok+"/logout/", func(w http.ResponseWriter, r *http.Request) {})
	return httptest.NewTLSServer(mux)
}

func TestPreflight(t *testing.T) {
	srv := FakeServer(t, "kfncspot", "secret")
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
	srv := FakeServer(t, "u", "p")
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
