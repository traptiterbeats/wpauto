package web

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/traptiterbeats/wpauto/internal/ai"
	"github.com/traptiterbeats/wpauto/internal/catalog"
	"github.com/traptiterbeats/wpauto/internal/catalog/catalogtest"
	"github.com/traptiterbeats/wpauto/internal/config"
	"github.com/traptiterbeats/wpauto/internal/cpanel/cpaneltest"
	"github.com/traptiterbeats/wpauto/internal/engine"
)

const apPass = "a-very-long-pass-123"

// apEnv is a panel wired to a fake cPanel, a fake WordPress site and a fake AI.
type apEnv struct {
	t      *testing.T
	s      *Server
	fake   *cpaneltest.Fake
	cl     *http.Client
	base   string
	csrf   string
	hostID string

	mu        sync.Mutex
	aiFail    int // field-batch requests that still fail
	aiBatches int
	demoSteps int
}

func (e *apEnv) setAIFail(n int) {
	e.mu.Lock()
	e.aiFail = n
	e.mu.Unlock()
}

// fakeAI answers like an OpenAI-compatible API; field batches fail while aiFail > 0.
func (e *apEnv) fakeAI() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		user := body.Messages[len(body.Messages)-1].Content
		reply := func(content string) {
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}, "finish_reason": "stop"}}})
		}
		if !strings.Contains(user, "[\n") { // connection test
			reply(`{"ok":true}`)
			return
		}
		e.mu.Lock()
		e.aiBatches++
		fail := e.aiFail > 0
		if fail {
			e.aiFail--
		}
		e.mu.Unlock()
		if fail {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"message":"model refused"}}`))
			return
		}
		var items []map[string]string
		_ = json.Unmarshal([]byte(user[strings.Index(user, "[\n"):]), &items)
		out := map[string]string{}
		for _, it := range items {
			out[it["key"]] = "متن تازه‌ی مشتری " + it["key"]
		}
		b, _ := json.Marshal(out)
		reply(string(b))
	}))
}

// fakeSite emulates the helper (install and theme actions) and the site's pages.
func (e *apEnv) fakeSite() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "wpauto-") {
			if r.Method == "HEAD" {
				return
			}
			// the home page still shows one demo text (a leftover the check must warn about)
			_, _ = w.Write([]byte(`<html><head><link href="/new/wp-content/themes/acme/style.css"></head><body><p>Ideas into products.</p></body></html>`))
			return
		}
		if !e.fake.Find(r.URL.Path, "'"+r.URL.Query().Get("t")+"'") {
			w.WriteHeader(404)
			return
		}
		out := map[string]any{"ok": true}
		switch r.URL.Query().Get("a") {
		case "ping":
			out = map[string]any{"ok": true, "php": "8.3.20", "zip": true, "curl": true, "mysqli": true, "free_mb": 700, "memory_limit": "128M"}
		case "download":
			out["bytes"] = 1 << 20
		case "extract":
			e.fake.Put("/home/kfncspot/public_html/new/wp-includes/version.php", "<?php $wp_version = '6.9';")
			out["moved"] = 21
		case "install":
			out["version"] = "6.9"
		case "wp_info":
			out = map[string]any{"ok": true, "version": "6.9", "url": "http://example.com/new/", "stylesheet": "twentytwentyfive",
				"themes": map[string]any{"twentytwentyfive": "1.0"}, "php": "8.3", "plugins": map[string]any{}}
		case "plugin_install", "theme_install":
			out["created"] = true
		case "theme_activate":
			out["previous"] = "twentytwentyfive"
		case "demo_prepare":
			out["version"] = "1.0.0"
		case "demo_start":
			out["state"] = map[string]any{"done": false, "label": "تصاویر", "percent": 0}
		case "demo_step":
			e.mu.Lock()
			e.demoSteps++
			n := e.demoSteps
			e.mu.Unlock()
			out["state"] = map[string]any{"done": n%2 == 0, "label": "برگه‌ها", "percent": 50}
		case "demo_remove":
			out["state"] = map[string]any{"done": true}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
}

func newAPEnv(t *testing.T) *apEnv {
	dir := t.TempDir()
	t.Setenv("WPAUTO_DIR", dir)
	oldWaits, oldPoll, oldVerify, oldAI := apRetryWaits, apPoll, apVerifyWaits, ai.RetryWaits
	apRetryWaits, apPoll, apVerifyWaits, ai.RetryWaits = []time.Duration{10 * time.Millisecond, 10 * time.Millisecond}, 10*time.Millisecond, []time.Duration{time.Millisecond}, nil
	t.Cleanup(func() { apRetryWaits, apPoll, apVerifyWaits, ai.RetryWaits = oldWaits, oldPoll, oldVerify, oldAI })

	e := &apEnv{t: t}
	e.fake = cpaneltest.New(t, "kfncspot", "secret")
	t.Cleanup(e.fake.Close)
	site := e.fakeSite()
	t.Cleanup(site.Close)
	api := e.fakeAI()
	t.Cleanup(api.Close)
	_ = os.MkdirAll(filepath.Join(dir, "profiles"), 0o700)
	prof := "id: fake\nname: fake\ninsecure_tls: true\nquirks:\n  site_addr: " + strings.TrimPrefix(site.URL, "http://") + "\n"
	_ = os.WriteFile(filepath.Join(dir, "profiles", "fake.yaml"), []byte(prof), 0o600)

	src := t.TempDir()
	cat := engine.Catalog()
	if _, err := cat.AddPlugin(catalogtest.Plugin(t, src, catalog.ImporterSlug, "")); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.Add(catalog.AddInput{ThemeZip: catalogtest.Theme(t, src, "acme"), DemoZip: catalogtest.RichDemo(t, src, "acme", false)}); err != nil {
		t.Fatal(err)
	}

	cfg, pass, _ := config.New(1)
	s, err := New(cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	e.s = s
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	e.cl = &http.Client{Jar: jar}
	e.base = ts.URL + cfg.BasePath
	if _, err := e.cl.PostForm(e.base+"login", url.Values{"username": {cfg.Username}, "password": {pass}}); err != nil {
		t.Fatal(err)
	}
	e.csrf = csrfOf(t, e.get("ai"))
	e.post("hosts/add", url.Values{"profile": {"fake"}, "url": {e.fake.URL}, "username": {"kfncspot"}, "secret": {"secret"}, "name": {"fakehost"}})
	e.hostID = s.store.List()[0].ID
	e.post("ai/add", url.Values{"kind": {"compatible"}, "base_url": {api.URL}, "key": {"secret-api-key"}, "model": {"m"}})
	if len(s.ai.List()) != 1 {
		t.Fatal("AI provider not added")
	}
	return e
}

func (e *apEnv) get(p string) string {
	e.t.Helper()
	r, err := e.cl.Get(e.base + p)
	if err != nil || r.StatusCode != 200 {
		e.t.Fatalf("GET %s: %v %v", p, err, r.Status)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	return string(b)
}

func (e *apEnv) post(p string, v url.Values) *http.Response {
	e.t.Helper()
	v.Set("csrf", e.csrf)
	r, err := e.cl.PostForm(e.base+p, v)
	if err != nil {
		e.t.Fatal(err)
	}
	r.Body.Close()
	return r
}

// start posts the AutoPilot form (multipart, like the browser) and returns the pipeline id.
func (e *apEnv) start() string { return e.startWith(nil, nil) }

// startWith is start with extra or changed fields and photos for the multi-file input.
func (e *apEnv) startWith(extra map[string]string, photos [][]byte) string {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fields := map[string]string{"csrf": e.csrf, "host": e.hostID, "domain": "example.com", "mode": "subfolder", "name": "new",
		"title": "کافه نارنج", "locale": "fa_IR", "admin_user": "boss", "admin_email": "a@example.com", "admin_pass": apPass,
		"theme": "acme", "provider": e.s.ai.List()[0].ID, "site_name": "کافه نارنج", "business": "کافه",
		"about": "کافه‌ای دنج در شیراز", "services": "قهوه\nکیک", "story": "یک کافه‌ی کوچک", "email": "hi@naranj.test"}
	for k, v := range extra {
		fields[k] = v
	}
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	for i, p := range photos {
		fw, _ := mw.CreateFormFile("photos", fmt.Sprintf("photo-%d.png", i))
		_, _ = fw.Write(p)
	}
	_ = mw.Close()
	r, err := e.cl.Post(e.base+"autopilot/start", mw.FormDataContentType(), &buf)
	if err != nil {
		e.t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if !strings.Contains(r.Request.URL.Path, "/autopilot/") || strings.HasSuffix(r.Request.URL.Path, "/start") {
		e.t.Fatalf("start did not redirect to the progress page: %s\n%s", r.Request.URL, b)
	}
	return r.Request.URL.Path[strings.LastIndex(r.Request.URL.Path, "/")+1:]
}

func (e *apEnv) wait(id string) autoPilot {
	e.t.Helper()
	for i := 0; i < 1500; i++ {
		if p, _ := e.s.ap.get(id); p.Done() {
			return p
		}
		time.Sleep(10 * time.Millisecond)
	}
	p, _ := e.s.ap.get(id)
	e.t.Fatalf("pipeline did not finish: %+v", p.Stages)
	return autoPilot{}
}

func apLogText(p autoPilot) string {
	var sb strings.Builder
	for _, l := range p.Log {
		sb.WriteString(l.Msg + "\n")
	}
	for _, s := range p.Stages {
		sb.WriteString(s.Key + ": " + s.State + " " + s.Error + "\n")
	}
	return sb.String()
}

func TestAutopilotSuccess(t *testing.T) {
	e := newAPEnv(t)
	form := e.get("autopilot")
	for _, want := range []string{`aria-current="page">🚀 AutoPilot<`, "→ بازگشت", `name="story"`, `name="theme" value="acme"`, "آمادگی پنل", `enctype="multipart/form-data"`} {
		if !strings.Contains(form, want) {
			t.Fatalf("form lacks %q", want)
		}
	}
	if strings.Count(form, `method="post"`) != strings.Count(form, `name="csrf"`) {
		t.Fatal("a POST form without csrf")
	}
	id := e.start()
	p := e.wait(id)
	if p.Status != apSuccess {
		t.Fatalf("status %s: %s\n%s", p.Status, p.Error, apLogText(p))
	}
	for _, st := range p.Stages {
		if st.State != apSuccess || st.Ended.IsZero() {
			t.Fatalf("stage %s: %s", st.Key, st.State)
		}
	}
	if p.Stages[0].Attempts != 1 {
		t.Fatalf("attempts %d", p.Stages[0].Attempts)
	}
	if p.SiteURL != "https://example.com/new/" || p.AdminURL() != "https://example.com/new/wp-admin/" {
		t.Fatalf("site url %q", p.SiteURL)
	}
	// the final check warns about the demo text left on the home page, without failing
	if w := strings.Join(p.Stages[3].Warnings, " "); !strings.Contains(w, "Ideas into products.") {
		t.Fatalf("leftover demo text not reported: %q (note %q)", w, p.Stages[3].Note)
	}
	if !strings.Contains(p.Stages[3].Note, "acme") {
		t.Fatalf("theme check note: %q", p.Stages[3].Note)
	}
	// every run is tied to the project, so its path lights up
	proj, ok := e.s.projects.Get(p.ProjectID)
	if !ok || proj.DraftID != p.DraftID || proj.Domain != "example.com" || proj.Folder != "new" {
		t.Fatalf("project %+v", proj)
	}
	for _, k := range []int{1, 2} {
		run, ok := e.s.runner.Get(p.Stages[k].RunID, 0)
		if !ok || run.ProjectID != p.ProjectID || run.Status != engine.StatusSuccess {
			t.Fatalf("run of %s: %+v", p.Stages[k].Key, run.ProjectID)
		}
		if k == 1 && run.Inputs["admin_pass"] != "••••••" {
			t.Fatal("admin password not masked in the run")
		}
	}
	for _, st := range e.s.pipeline(&proj) {
		if st.Key != "host" && st.Key != "wpcheck" && st.State != "done" {
			t.Fatalf("project stage %s is %s", st.Key, st.State)
		}
	}
	// the customised demo was built and uploaded
	data, _ := e.fake.FindFile("/acme-demo.zip")
	zr, err := zip.NewReader(strings.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal("customised demo not uploaded")
	}
	found := false
	for _, f := range zr.File {
		if f.Name == "demo.json" {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			found = strings.Contains(string(b), "متن تازه‌ی مشتری")
		}
	}
	if !found {
		t.Fatal("uploaded demo lacks the AI texts")
	}
	// no secret in the saved state
	raw, _ := os.ReadFile(filepath.Join(config.Dir(), "autopilot", id+".json"))
	if len(raw) == 0 || strings.Contains(string(raw), apPass) || strings.Contains(string(raw), "secret-api-key") {
		t.Fatal("pipeline state missing or holds a secret")
	}
	if !strings.Contains(string(raw), `"total_seconds"`) || p.Ended.IsZero() {
		t.Fatal("total time not stored")
	}
	if e.s.ap.password(id) != "" {
		t.Fatal("password kept in memory after success")
	}
	page := e.get("autopilot/" + id)
	for _, want := range []string{"دیدن سایت", `href="https://example.com/new/wp-admin/"`, "زمان کل", "→ بازگشت به AutoPilot", "Ideas into products.", "آمادگی پنل", `id="apclock"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("progress page lacks %q", want)
		}
	}
	if strings.Contains(page, "ادامه از همین مرحله") || strings.Contains(page, apPass) {
		t.Fatal("finished page offers resume or shows the password")
	}
	var js map[string]any
	if err := json.Unmarshal([]byte(e.get("autopilot/"+id+"/json")), &js); err != nil || js["done"] != true || js["status"] != "success" {
		t.Fatalf("json: %v %v", err, js)
	}
	if !strings.Contains(e.get("autopilot"), "AutoPilotهای قبلی") {
		t.Fatal("pipeline list missing")
	}
}

func TestAutopilotAIRetriesThenSucceeds(t *testing.T) {
	e := newAPEnv(t)
	// each failed attempt makes two field-batch requests (the generator retries a batch once)
	e.setAIFail(4)
	p := e.wait(e.start())
	if p.Status != apSuccess {
		t.Fatalf("status %s: %s\n%s", p.Status, p.Error, apLogText(p))
	}
	if p.Stages[0].Attempts != 3 {
		t.Fatalf("attempts %d\n%s", p.Stages[0].Attempts, apLogText(p))
	}
	if !strings.Contains(apLogText(p), "تلاش 1 از 3 ناموفق") {
		t.Fatal("failed attempt not logged")
	}
}

func TestAutopilotAIFailsStopsWithoutHostChanges(t *testing.T) {
	e := newAPEnv(t)
	e.setAIFail(1000)
	before := e.fake.CallCount()
	id := e.start()
	p := e.wait(id)
	if p.Status != apFailed || p.Stages[0].State != apFailed || p.Stages[0].Attempts != 3 {
		t.Fatalf("status %s stage %s attempts %d", p.Status, p.Stages[0].State, p.Stages[0].Attempts)
	}
	for _, st := range p.Stages[1:] {
		if st.State != apWaiting || st.RunID != "" {
			t.Fatalf("stage %s ran: %s", st.Key, st.State)
		}
	}
	if !strings.Contains(p.Error, "پس از 3 تلاش") {
		t.Fatalf("error: %s", p.Error)
	}
	if n := e.fake.CallCount(); n != before {
		t.Fatalf("cPanel was called %d times after the AI failed", n-before)
	}
	page := e.get("autopilot/" + id)
	if !strings.Contains(page, "ادامه از همین مرحله") || !strings.Contains(page, "customize/"+p.DraftID) {
		t.Fatal("failed page lacks resume or the draft link")
	}
	if strings.Count(page, `method="post"`) != strings.Count(page, `name="csrf"`) {
		t.Fatal("a POST form without csrf")
	}
	// the AI works again: resume runs from the failed stage to the end
	e.setAIFail(0)
	e.post("autopilot/"+id+"/resume", url.Values{})
	time.Sleep(20 * time.Millisecond)
	p = e.wait(id)
	if p.Status != apSuccess {
		t.Fatalf("resume: %s %s\n%s", p.Status, p.Error, apLogText(p))
	}
}

func TestAutopilotInterruptedOnRestart(t *testing.T) {
	dir := t.TempDir()
	st, _ := openAP(dir)
	now := time.Now().Add(-time.Minute)
	p := &autoPilot{ID: "20260101-120000-abcd", Title: "x", Status: apRunning, Stages: newStages(), Created: now, Started: now}
	p.Stages[0].State, p.Stages[0].Started = apRunning, now
	st.add(p)
	st2, err := openAP(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := st2.get(p.ID)
	if !ok || got.Status != apFailed || got.Stages[0].State != apFailed || !strings.Contains(got.Error, "interrupted") || !got.Resumable() {
		t.Fatalf("%+v", got)
	}
	if fmtDur(65) != "01:05" || fmtDur(3725) != "1:02:05" {
		t.Fatal("fmtDur")
	}
}
