// Package cpaneltest provides a fake cPanel server for tests.
package cpaneltest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// New starts a fake cPanel (TLS) for user/pass. The main domain example.com has WordPress 6.8.3
// installed; blog.example.com is empty.
func New(t *testing.T, user, pass string) *httptest.Server {
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
		case "DomainInfo/domains_data":
			ok(w, map[string]any{
				"main_domain":   map[string]any{"domain": "example.com", "documentroot": "/home/" + user + "/public_html"},
				"addon_domains": []any{},
				"sub_domains":   []any{map[string]any{"domain": "blog.example.com", "documentroot": "/home/" + user + "/blog"}},
			})
		case "Fileman/list_files":
			if r.FormValue("dir") == "/home/"+user+"/public_html" {
				ok(w, []map[string]any{{"file": "wp-config.php", "type": "file"}, {"file": "wp-includes", "type": "dir"}, {"file": "index.php", "type": "file"}})
			} else {
				ok(w, []map[string]any{{"file": "index.html", "type": "file"}})
			}
		case "Fileman/get_file_content":
			ok(w, map[string]any{"content": "<?php\n$wp_version = '6.8.3';\n"})
		case "Features/list_features":
			ok(w, map[string]any{"fileman": 1, "mysql": 1, "multiphp": 1, "ssh": 0})
		default:
			w.WriteHeader(404)
		}
	})
	mux.HandleFunc(tok+"/logout/", func(w http.ResponseWriter, r *http.Request) {})
	return httptest.NewTLSServer(mux)
}
