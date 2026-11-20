// Package cpaneltest provides a fake cPanel server for tests.
package cpaneltest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"testing"
)

// Fake is a fake cPanel. Files holds everything written with save_file_content or upload_files
// (absolute path → content) and Calls lists every Module/function called, in order.
type Fake struct {
	*httptest.Server
	mu    sync.Mutex
	Files map[string]string
	Calls []string
	// Fail makes the named Module/function return an error.
	Fail map[string]bool
}

// Called reports whether Module/function was called.
func (f *Fake) Called(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.Calls {
		if c == name {
			return true
		}
	}
	return false
}

// Find reports whether a written file's path ends with suffix and its content contains substr.
func (f *Fake) Find(suffix, substr string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for p, c := range f.Files {
		if strings.HasSuffix(p, suffix) && strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

// File returns a written file.
func (f *Fake) File(p string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.Files[p]
	return v, ok
}

// New starts a fake cPanel (TLS) for user/pass. The main domain example.com has WordPress 6.8.3
// installed; blog.example.com is empty.
func New(t *testing.T, user, pass string) *Fake {
	t.Helper()
	f := &Fake{Files: map[string]string{}, Fail: map[string]bool{}}
	home := "/home/" + user
	record := func(name string) bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.Calls = append(f.Calls, name)
		return f.Fail[name]
	}
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
		name := strings.TrimPrefix(r.URL.Path, tok+"/execute/")
		if record(name) {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 0, "errors": []string{"forced failure"}})
			return
		}
		switch name {
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
			switch r.FormValue("dir") {
			case home + "/public_html":
				ok(w, []map[string]any{{"file": "wp-config.php", "type": "file"}, {"file": "wp-includes", "type": "dir"}, {"file": "index.php", "type": "file"}, {"file": "shop", "type": "dir"}, {"file": "cgi-bin", "type": "dir"}})
			case home + "/public_html/shop":
				ok(w, []map[string]any{{"file": "wp-config.php", "type": "file"}, {"file": "wp-includes", "type": "dir"}})
			case home + "/blog":
				ok(w, []map[string]any{{"file": "index.html", "type": "file"}})
			default:
				_ = json.NewEncoder(w).Encode(map[string]any{"status": 0, "errors": []string{"No such directory"}})
			}
		case "Fileman/save_file_content":
			f.mu.Lock()
			f.Files[path.Join(r.FormValue("dir"), r.FormValue("file"))] = r.FormValue("content")
			f.mu.Unlock()
			ok(w, map[string]any{})
		case "Fileman/upload_files":
			file, hdr, err := r.FormFile("file-1")
			if err != nil {
				w.WriteHeader(400)
				return
			}
			b, _ := io.ReadAll(file)
			f.mu.Lock()
			f.Files[path.Join(r.FormValue("dir"), hdr.Filename)] = string(b)
			f.mu.Unlock()
			ok(w, map[string]any{"succeeded": 1})
		case "SubDomain/addsubdomain", "LangPHP/php_set_vhost_versions", "SSL/start_autossl_check",
			"Mysql/create_database", "Mysql/create_user", "Mysql/set_privileges_on_database",
			"Mysql/delete_database", "Mysql/delete_user":
			ok(w, nil)
		case "Fileman/get_file_content":
			ok(w, map[string]any{"content": "<?php\n$wp_version = '6.8.3';\n"})
		case "Features/list_features":
			ok(w, map[string]any{"fileman": 1, "mysql": 1, "multiphp": 1, "ssh": 0})
		default:
			w.WriteHeader(404)
		}
	})
	mux.HandleFunc(tok+"/json-api/cpanel", func(w http.ResponseWriter, r *http.Request) {
		name := "api2:" + r.FormValue("cpanel_jsonapi_module") + "/" + r.FormValue("cpanel_jsonapi_func")
		if record(name) {
			_ = json.NewEncoder(w).Encode(map[string]any{"cpanelresult": map[string]any{"error": "forced failure", "event": map[string]any{"result": 0}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"cpanelresult": map[string]any{"data": []any{map[string]any{"result": 1}}, "event": map[string]any{"result": 1}}})
	})
	mux.HandleFunc(tok+"/logout/", func(w http.ResponseWriter, r *http.Request) {})
	f.Server = httptest.NewTLSServer(mux)
	return f
}
