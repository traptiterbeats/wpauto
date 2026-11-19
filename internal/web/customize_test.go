package web

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/traptiterbeats/wpauto/internal/catalog"
	"github.com/traptiterbeats/wpauto/internal/catalog/catalogtest"
	"github.com/traptiterbeats/wpauto/internal/config"
	"github.com/traptiterbeats/wpauto/internal/engine"
	"github.com/traptiterbeats/wpauto/internal/store"
)

var csrfRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func TestCustomizeFlow(t *testing.T) {
	t.Setenv("WPAUTO_DIR", t.TempDir())
	src := t.TempDir()
	cat := engine.Catalog()
	if _, err := cat.AddPlugin(catalogtest.Plugin(t, src, catalog.ImporterSlug, "")); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.Add(catalog.AddInput{ThemeZip: catalogtest.Theme(t, src, "acme"), DemoZip: catalogtest.RichDemo(t, src, "acme", false)}); err != nil {
		t.Fatal(err)
	}
	// Fake OpenAI-compatible API: rewrites every field.
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		user := body.Messages[1].Content
		var items []map[string]string
		_ = json.Unmarshal([]byte(user[strings.Index(user, "[\n"):]), &items)
		out := map[string]string{}
		for _, it := range items {
			out[it["key"]] = "تازه " + it["original"]
		}
		b, _ := json.Marshal(out)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(b)}, "finish_reason": "stop"}}})
	}))
	defer api.Close()

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
	csrf := csrfRe.FindStringSubmatch(get("ai"))[1]
	post := func(p string, v url.Values) *http.Response {
		t.Helper()
		v.Set("csrf", csrf)
		r, err := cl.PostForm(base+p, v)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r
	}
	post("ai/add", url.Values{"kind": {"compatible"}, "base_url": {api.URL}, "model": {"m"}, "key": {"secret-key"}})
	if body := get("ai"); !strings.Contains(body, api.URL) || strings.Contains(body, "secret-key") {
		t.Fatal("provider not listed or key leaked")
	}
	r := post("customize/new", url.Values{"title": {"کافه نارنج"}, "theme": {"acme"}})
	id := strings.TrimPrefix(r.Request.URL.Path, cfg.BasePath+"customize/")
	if len(id) != 12 {
		t.Fatalf("draft id from %s", r.Request.URL.Path)
	}
	post("customize/"+id+"/brief", url.Values{"site_name": {"کافه نارنج"}, "business": {"کافه"}, "email": {"hi@naranj.test"}})

	// Logo upload (multipart).
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("csrf", csrf)
	fw, _ := mw.CreateFormFile("logo", "logo.png")
	_, _ = fw.Write([]byte("\x89PNG\r\n\x1a\n0000000000000000"))
	mw.Close()
	if r, err := cl.Post(base+"customize/"+id+"/images", mw.FormDataContentType(), &buf); err != nil || r.StatusCode != 200 {
		t.Fatalf("upload: %v", err)
	}

	post("customize/"+id+"/generate", url.Values{})
	for i := 0; ; i++ {
		var st map[string]string
		_ = json.Unmarshal([]byte(get("customize/"+id+"/status")), &st)
		if st["status"] == "ready" {
			break
		}
		if st["status"] == "error" || i > 100 {
			t.Fatalf("generate: %v", st)
		}
		time.Sleep(30 * time.Millisecond)
	}
	page := get("customize/" + id)
	if !strings.Contains(page, "تازه ACME STUDIO") || !strings.Contains(page, "file/logo.png") {
		t.Fatal("draft page does not show generated text and logo")
	}
	post("customize/"+id+"/values", url.Values{"v:theme_mods.brand_text": {"NARANJ"}})
	d, _ := engine.Drafts().Get(id)
	if d.Values["theme_mods.brand_text"] != "NARANJ" || d.Values["theme_mods.contact_email"] != "hi@naranj.test" || d.Logo == "" {
		t.Fatalf("draft: %+v", d)
	}
	if _, err := s.store.Add(store.Host{Name: "h", ProfileID: "generic", URL: "https://example.com:2083", Username: "u"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(get("run/theme-install?theme=acme&draft="+id), `value="`+id+`" selected`) {
		t.Fatal("run form does not preselect the draft")
	}
	r = post("customize/"+id+"/build", url.Values{})
	if r.StatusCode != 200 || !strings.Contains(r.Header.Get("Content-Disposition"), "acme-custom-demo.zip") {
		t.Fatalf("build download: %d %v", r.StatusCode, r.Header)
	}
}
