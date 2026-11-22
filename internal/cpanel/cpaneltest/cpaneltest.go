// Package cpaneltest provides a fake cPanel server for tests.
package cpaneltest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"sort"
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
	// Fail makes the named Module/function return an error. API2 calls are named
	// "api2:Module/function"; a single Fileman::fileop operation can be failed with
	// "api2:Fileman/fileop:<op>" (e.g. "api2:Fileman/fileop:extract").
	Fail map[string]bool
	// ShallowUnlink makes Fileman::fileop unlink behave like a cPanel that cannot delete a
	// folder that still has files in it.
	ShallowUnlink bool
	// Extracted lists the archive entries Fileman::fileop extract writes (relative to the
	// destination). Nil means a tiny WordPress tree under "wordpress/".
	Extracted []string
}

// DefaultExtracted is what extract writes when Extracted is nil.
var DefaultExtracted = []string{"wordpress/index.php", "wordpress/wp-load.php", "wordpress/wp-includes/version.php", "wordpress/wp-admin/install.php"}

// Under returns the written files at or below p (p itself or p/...), sorted.
func (f *Fake) Under(p string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.under(p)
}

func (f *Fake) under(p string) []string {
	p = strings.TrimSuffix(p, "/")
	var out []string
	for k := range f.Files {
		if k == p || strings.HasPrefix(k, p+"/") {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Path returns the full path of a written file whose path ends with suffix.
func (f *Fake) Path(suffix string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for p := range f.Files {
		if strings.HasSuffix(p, suffix) {
			return p, true
		}
	}
	return "", false
}

// Move renames every written file at or below from to the same place below to.
func (f *Fake) Move(from, to string) {
	f.fileop("move", []string{from}, []string{to})
}

// Delete removes every written file at or below p.
func (f *Fake) Delete(p string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range f.under(p) {
		delete(f.Files, k)
	}
}

// fileop applies one Fileman::fileop operation to the fake file tree.
func (f *Fake) fileop(op string, src, dst []string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch op {
	case "unlink":
		for _, p := range src {
			p = strings.TrimSuffix(p, "/")
			if f.ShallowUnlink {
				delete(f.Files, p) // a file; a folder with files in it stays
				continue
			}
			for _, k := range f.under(p) {
				delete(f.Files, k)
			}
		}
	case "rename", "move":
		for i, p := range src {
			if i >= len(dst) {
				break
			}
			for _, k := range f.under(p) {
				f.Files[dst[i]+strings.TrimPrefix(k, strings.TrimSuffix(p, "/"))] = f.Files[k]
				delete(f.Files, k)
			}
		}
	case "extract":
		if len(src) == 0 || len(dst) == 0 {
			return "missing source or destination"
		}
		if _, ok := f.Files[src[0]]; !ok {
			return "archive not found"
		}
		list := f.Extracted
		if list == nil {
			list = DefaultExtracted
		}
		for _, e := range list {
			f.Files[path.Join(dst[0], e)] = "extracted " + e
		}
	}
	return ""
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

// FindFile returns the content of a written file whose path ends with suffix.
func (f *Fake) FindFile(suffix string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for p, c := range f.Files {
		if strings.HasSuffix(p, suffix) {
			return c, true
		}
	}
	return "", false
}

// CallCount returns how many Module/function calls were made so far.
func (f *Fake) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Calls)
}

// Put stores a file as if the site had written it (e.g. WordPress files a fake helper extracted).
func (f *Fake) Put(p, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Files[p] = content
}

// children lists the files and folders written directly inside dir.
func (f *Fake) children(dir string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := map[string]bool{}
	var out []map[string]any
	prefix := strings.TrimSuffix(dir, "/") + "/"
	for p := range f.Files {
		rest, ok := strings.CutPrefix(p, prefix)
		if !ok || rest == "" {
			continue
		}
		name, _, isDir := strings.Cut(rest, "/")
		if seen[name] {
			continue
		}
		seen[name] = true
		typ := "file"
		if isDir {
			typ = "dir"
		}
		out = append(out, map[string]any{"file": name, "type": typ})
	}
	return out
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
			ok(w, map[string]any{"user": user, "plan": "1GB", "theme": "jupiter", "domain": "example.com"})
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
		case "DomainInfo/single_domain_data":
			roots := map[string]string{"example.com": "/home/" + user + "/public_html", "blog.example.com": "/home/" + user + "/blog"}
			if root, found := roots[r.FormValue("domain")]; found {
				ok(w, map[string]any{"domain": r.FormValue("domain"), "documentroot": root})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 0, "errors": []string{"domain not found"}})
		case "Fileman/list_files":
			switch r.FormValue("dir") {
			case home:
				list := f.children(home)
				if list == nil {
					list = []map[string]any{}
				}
				ok(w, list)
			case home + "/public_html":
				ok(w, []map[string]any{{"file": "wp-config.php", "type": "file"}, {"file": "wp-includes", "type": "dir"}, {"file": "index.php", "type": "file"}, {"file": "shop", "type": "dir"}, {"file": "cgi-bin", "type": "dir"}})
			case home + "/public_html/shop":
				ok(w, []map[string]any{{"file": "wp-config.php", "type": "file"}, {"file": "wp-includes", "type": "dir"}})
			case home + "/blog":
				ok(w, []map[string]any{{"file": "index.html", "type": "file"}})
			default:
				// a folder the tests wrote into lists what was written there
				if list := f.children(r.FormValue("dir")); len(list) > 0 {
					ok(w, list)
					return
				}
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
		case "Fileman/delete_file":
			f.mu.Lock()
			for _, k := range f.under(r.FormValue("path")) {
				delete(f.Files, k)
			}
			f.mu.Unlock()
			ok(w, nil)
		case "Fileman/get_file_content":
			if r.FormValue("file") == "wp-config.php" {
				db := path.Base(r.FormValue("dir"))
				ok(w, map[string]any{"content": "<?php\ndefine( 'DB_NAME', '" + user + "_" + db + "' );\ndefine('DB_USER', \"" + user + "_" + db + "\");\n"})
				return
			}
			f.mu.Lock()
			c, found := f.Files[path.Join(r.FormValue("dir"), r.FormValue("file"))]
			f.mu.Unlock()
			if found && c != "" {
				ok(w, map[string]any{"content": c})
				return
			}
			ok(w, map[string]any{"content": "<?php\n$wp_version = '6.8.3';\n"})
		case "Features/list_features":
			ok(w, map[string]any{"fileman": 1, "mysql": 1, "multiphp": 1, "ssh": 0})
		default:
			w.WriteHeader(404)
		}
	})
	mux.HandleFunc(tok+"/json-api/cpanel", func(w http.ResponseWriter, r *http.Request) {
		name := "api2:" + r.FormValue("cpanel_jsonapi_module") + "/" + r.FormValue("cpanel_jsonapi_func")
		failed := record(name)
		op := r.FormValue("op")
		if name == "api2:Fileman/fileop" && op != "" {
			failed = record(name+":"+op) || failed
		}
		if failed {
			_ = json.NewEncoder(w).Encode(map[string]any{"cpanelresult": map[string]any{"error": "forced failure", "event": map[string]any{"result": 0}}})
			return
		}
		if name == "api2:Fileman/fileop" {
			split := func(v string) []string {
				if v == "" {
					return nil
				}
				return strings.Split(v, ",")
			}
			if msg := f.fileop(op, split(r.FormValue("sourcefiles")), split(r.FormValue("destfiles"))); msg != "" {
				_ = json.NewEncoder(w).Encode(map[string]any{"cpanelresult": map[string]any{"data": []any{map[string]any{"result": 0, "err": msg}}, "event": map[string]any{"result": 1}}})
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"cpanelresult": map[string]any{"data": []any{map[string]any{"result": 1}}, "event": map[string]any{"result": 1}}})
	})
	mux.HandleFunc(tok+"/logout/", func(w http.ResponseWriter, r *http.Request) {})
	f.Server = httptest.NewTLSServer(mux)
	return f
}
