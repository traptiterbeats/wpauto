package web

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/traptiterbeats/wpauto/internal/catalog"
	"github.com/traptiterbeats/wpauto/internal/catalog/catalogtest"
	"github.com/traptiterbeats/wpauto/internal/engine"
	"github.com/traptiterbeats/wpauto/internal/projects"
)

// postZip posts a multipart form with one theme zip.
func (e *homeEnv) postZip(path string, fields map[string]string, field, zipPath string) *http.Response {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	if zipPath != "" {
		fw, _ := mw.CreateFormFile(field, "theme.zip")
		b, err := os.ReadFile(zipPath)
		if err != nil {
			e.t.Fatal(err)
		}
		_, _ = fw.Write(b)
	}
	_ = mw.Close()
	r, err := e.cl.Post(e.base+path, mw.FormDataContentType(), &buf)
	if err != nil {
		e.t.Fatal(err)
	}
	_, _ = io.ReadAll(r.Body)
	r.Body.Close()
	return r
}

// The «مدیریت قالب‌ها» card is the last active card of «مراحل بیشتر», with the admin badge.
func TestThemeAdminCard(t *testing.T) {
	e := newBareEnv(t)
	p, _ := e.s.projects.Add(projects.Project{Name: "کافه", Domain: "cafe.test"})
	pg := e.page("p/" + p.ID + "/wordpress")
	i := strings.Index(pg, `id="g-site"`)
	j := strings.Index(pg, `id="g-auto"`)
	if i < 0 || j < 0 {
		t.Fatal("groups missing")
	}
	site := pg[i:j]
	link := e.s.cfg.BasePath + "p/" + p.ID + "/wordpress/themes"
	c := strings.Index(site, `data-key="themeadmin"`)
	if c < 0 || !strings.Contains(site, `data-link="`+link+`"`) {
		t.Fatalf("theme admin card missing or wrong link")
	}
	card := site[c:]
	card = card[:strings.Index(card, "</button>")]
	for _, want := range []string{"🛠", "مدیریت قالب‌ها", `class="atag"`, "ادمین", "افزودن قالب جدید، دمو و پیش‌نمایش؛ مشترک بین همه‌ی پروژه‌ها"} {
		if !strings.Contains(card, want) {
			t.Errorf("card lacks %q", want)
		}
	}
	if strings.Contains(card, "ftag") || strings.Contains(card, "اول بررسی هاست") {
		t.Error("admin card must not use the AutoPilot style or wait for a host")
	}
	// last active card: only «به‌زودی» cards after it
	rest := site[c+len(`data-key="themeadmin"`):]
	if strings.Contains(rest, `<button type="submit" class="tile`) {
		t.Error("an active card comes after «مدیریت قالب‌ها»")
	}
	if !strings.Contains(rest, `class="tile soon"`) {
		t.Error("soon cards should stay after it")
	}
	sameCSRF(t, "wordpress page", pg)
	// registry: Admin flag set, last non-Soon card of the group
	var last wpCard
	for _, x := range wpCards {
		if x.Group == "site" && !x.Soon {
			last = x
		}
	}
	if last.Key != "themeadmin" || !last.Admin || last.Icon != "🛠" {
		t.Fatalf("registry: %+v", last)
	}
}

// Opened from a project: breadcrumb پروژه › وردپرس › مدیریت قالب‌ها, Back to the project's WordPress
// page, forms carry `from`, add/delete come back to the same context.
func TestThemeAdminProjectContext(t *testing.T) {
	e := newBareEnv(t)
	bp := e.s.cfg.BasePath
	p, _ := e.s.projects.Add(projects.Project{Name: "کافه", Domain: "cafe.test"})
	wpLink := bp + "p/" + p.ID + "/wordpress"
	for _, path := range []string{"p/" + p.ID + "/wordpress/themes", "themes?from=" + p.ID} {
		pg := e.page(path)
		trail := `<a href="` + bp + `p/` + p.ID + `">کافه</a><span class="sep">›</span><a href="` + wpLink + `">وردپرس</a><span class="sep">›</span><strong aria-current="page">مدیریت قالب‌ها</strong>`
		if !strings.Contains(pg, trail) {
			t.Errorf("%s: project breadcrumb missing", path)
		}
		if !strings.Contains(pg, `href="`+wpLink+`" id="backbtn"`) {
			t.Errorf("%s: Back must return to the project's WordPress page", path)
		}
		if !strings.Contains(pg, "<title>مدیریت قالب‌ها (ادمین) | wpauto</title>") || !strings.Contains(pg, `id="themeadmin"`) {
			t.Errorf("%s: title", path)
		}
		if strings.Count(pg, `name="from" value="`+p.ID+`"`) < 2 {
			t.Errorf("%s: forms lack from", path)
		}
		sameCSRF(t, path, pg)
	}
	// global page: tools breadcrumb, Back to tools
	g := e.page("themes")
	if !strings.Contains(g, `<a href="`+bp+`tools">ابزارها</a>`) || !strings.Contains(g, `href="`+bp+`tools" id="backbtn"`) || !strings.Contains(g, `name="from" value=""`) {
		t.Error("global themes page context")
	}
	if _, r := e.get("p/nope/wordpress/themes"); r.StatusCode != 404 {
		t.Errorf("unknown project: %d", r.StatusCode)
	}

	// add from the project → back to the project's page
	src := t.TempDir()
	csrf := csrfOf(t, g)
	r := e.postZip("themes/add", map[string]string{"csrf": csrf, "from": p.ID}, "theme_zip", catalogtest.Theme(t, src, "acme"))
	if r.Request.URL.Path != wpLink+"/themes" || !strings.Contains(r.Request.URL.RawQuery, "msg=") {
		t.Fatalf("add redirect: %s", r.Request.URL)
	}
	if _, err := engine.Catalog().Get("acme"); err != nil {
		t.Fatal("theme not added")
	}
	// error (no file) also keeps the context
	r = e.postZip("themes/add", map[string]string{"csrf": csrf, "from": p.ID}, "", "")
	if r.Request.URL.Path != wpLink+"/themes" || !strings.Contains(r.Request.URL.RawQuery, "err=") {
		t.Fatalf("add error redirect: %s", r.Request.URL)
	}
	// delete from the project → back to it
	r, _ = e.post("themes/acme/delete", url.Values{"csrf": {csrf}, "from": {p.ID}})
	if r.Request.URL.Path != wpLink+"/themes" || !strings.Contains(r.Request.URL.RawQuery, "msg=") {
		t.Fatalf("delete redirect: %s", r.Request.URL)
	}
	if _, err := engine.Catalog().Get("acme"); err == nil {
		t.Fatal("theme not deleted")
	}
	// without from: the global page, as before
	r = e.postZip("themes/add", map[string]string{"csrf": csrf}, "theme_zip", catalogtest.Theme(t, src, "zeta"))
	if r.Request.URL.Path != bp+"themes" {
		t.Fatalf("global add redirect: %s", r.Request.URL)
	}
	// CSRF still required
	if r, _ := e.post("themes/zeta/delete", url.Values{"from": {p.ID}}); r.StatusCode != 400 {
		t.Errorf("delete without csrf: %d", r.StatusCode)
	}
	// «افزودن قالب» links of task pages lead to the project's theme page (empty catalog)
	_ = engine.Catalog().Delete("zeta")
	if pg := e.page("customize"); !strings.Contains(pg, `href="`+wpLink+`/themes" class="themeadmin-link"`) {
		t.Error("customize: «افزودن قالب» link")
	}
	q, _ := http.NewRequest("GET", "/x?project="+p.ID, nil)
	if got := e.s.themesLinkFor(q); got != wpLink+"/themes" {
		t.Errorf("themesLinkFor(project): %s", got)
	}
	_ = e.s.projects.SetActive("")
	q, _ = http.NewRequest("GET", "/x", nil)
	if got := e.s.themesLinkFor(q); got != bp+"themes" {
		t.Errorf("themesLinkFor(no project): %s", got)
	}
}

// canAdmin false: no admin card, no menu entry, 403 on every admin route; screenshots still served.
func TestThemeAdminDisabled(t *testing.T) {
	e := newBareEnv(t)
	bp := e.s.cfg.BasePath
	p, _ := e.s.projects.Add(projects.Project{Name: "کافه", Domain: "cafe.test"})
	csrf := csrfOf(t, e.page("themes"))
	off := false
	e.s.cfg.AdminUI = &off
	if e.s.canAdmin(httptestReq()) {
		t.Fatal("canAdmin should follow admin_ui")
	}
	pg := e.page("p/" + p.ID + "/wordpress")
	if strings.Contains(pg, `data-key="themeadmin"`) || strings.Contains(pg, "/wordpress/themes") {
		t.Error("admin card shown with admin_ui off")
	}
	if strings.Contains(e.page("tools"), `href="`+bp+`themes"`) {
		t.Error("tools lists the admin page")
	}
	if strings.Contains(e.page("customize"), "themeadmin-link") {
		t.Error("«افزودن قالب» link shown with admin_ui off")
	}
	for _, path := range []string{"themes", "themes?from=" + p.ID, "p/" + p.ID + "/wordpress/themes"} {
		b, r := e.get(path)
		if r.StatusCode != 403 || !strings.Contains(b, "دسترسی ندارید") || !strings.Contains(b, `id="backbtn"`) {
			t.Errorf("GET %s: %d", path, r.StatusCode)
		}
	}
	src := t.TempDir()
	if r := e.postZip("themes/add", map[string]string{"csrf": csrf}, "theme_zip", catalogtest.Theme(t, src, "acme")); r.StatusCode != 403 {
		t.Errorf("add: %d", r.StatusCode)
	}
	if _, err := engine.Catalog().Get("acme"); err == nil {
		t.Error("theme added with admin_ui off")
	}
	for _, path := range []string{"themes/acme/delete", "plugins/x/delete"} {
		if r, _ := e.post(path, url.Values{"csrf": {csrf}}); r.StatusCode != 403 {
			t.Errorf("POST %s: %d", path, r.StatusCode)
		}
	}
	if r := e.postZip("plugins/add", map[string]string{"csrf": csrf}, "plugin_zip", catalogtest.Theme(t, src, "p")); r.StatusCode != 403 {
		t.Errorf("plugins/add: %d", r.StatusCode)
	}
	// logged-out still goes to login first
	if r, err := http.Get(e.base + "themes"); err != nil || !strings.HasSuffix(r.Request.URL.Path, "/login") {
		t.Error("logged out: not sent to login")
	}
	// turning it back on restores everything
	on := true
	e.s.cfg.AdminUI = &on
	e.page("themes")
	e.s.cfg.AdminUI = nil // missing = on
	e.page("p/" + p.ID + "/wordpress/themes")
}

func httptestReq() *http.Request {
	r, _ := http.NewRequest("GET", "/", nil)
	return r
}

// postFiles posts a multipart form with several files (field → paths).
func (e *homeEnv) postFiles(path string, fields map[string]string, files map[string][]string) *http.Response {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	for field, paths := range files {
		for _, p := range paths {
			fw, _ := mw.CreateFormFile(field, filepath.Base(p))
			b, err := os.ReadFile(p)
			if err != nil {
				e.t.Fatal(err)
			}
			_, _ = fw.Write(b)
		}
	}
	_ = mw.Close()
	r, err := e.cl.Post(e.base+path, mw.FormDataContentType(), &buf)
	if err != nil {
		e.t.Fatal(err)
	}
	_, _ = io.ReadAll(r.Body)
	r.Body.Close()
	return r
}

// Theme + demo + companion core + Saayeh importer in one submit: companions land in the library,
// the demo's require on the core resolves to the library, the importer is library-only.
func TestThemeAddCompanions(t *testing.T) {
	e := newBareEnv(t)
	bp := e.s.cfg.BasePath
	pg := e.page("themes")
	if !strings.Contains(pg, `name="companion_zip" accept=".zip" multiple`) || !strings.Contains(pg, "افزونه‌های همراه قالب (zip، اختیاری)") {
		t.Fatal("companion field missing (it must show even with an empty library)")
	}
	if strings.Index(pg, `name="demo_zip"`) > strings.Index(pg, `name="companion_zip"`) {
		t.Error("companion field must come right after the demo field")
	}
	if !strings.Contains(pg, "در «افزونه‌های همراه قالب»") {
		t.Error("importer warning must point at the companion field")
	}
	csrf := csrfOf(t, pg)
	src := t.TempDir()
	theme := catalogtest.Theme(t, src, "emart")
	demo := catalogtest.Demo(t, src, "emart", "woocommerce", "emart-core")
	core := catalogtest.Plugin(t, src, "emart-core", "")
	imp := catalogtest.Plugin(t, src, catalog.ImporterSlug, "")

	// a bad companion zip: nothing added anywhere
	bad := catalogtest.Zip(t, src, "bad.zip", map[string]string{"x/readme.txt": "no header"})
	r := e.postFiles("themes/add", map[string]string{"csrf": csrf}, map[string][]string{
		"theme_zip": {theme}, "demo_zip": {demo}, "companion_zip": {core, bad}})
	if r.Request.URL.Path != bp+"themes" || !strings.Contains(r.Request.URL.RawQuery, "err=") {
		t.Fatalf("bad companion: %s", r.Request.URL)
	}
	if len(engine.Catalog().Library()) != 0 || len(engine.Catalog().List()) != 0 {
		t.Fatal("bad companion must add nothing")
	}
	// theme add fails (demo for another theme): the companions are rolled back
	r = e.postFiles("themes/add", map[string]string{"csrf": csrf}, map[string][]string{
		"theme_zip": {theme}, "demo_zip": {catalogtest.Demo(t, t.TempDir(), "other")}, "companion_zip": {core, imp}})
	if !strings.Contains(r.Request.URL.RawQuery, "err=") || len(engine.Catalog().Library()) != 0 {
		t.Fatalf("failed add must roll back companions: %s %v", r.Request.URL, engine.Catalog().Library())
	}

	r = e.postFiles("themes/add", map[string]string{"csrf": csrf}, map[string][]string{
		"theme_zip": {theme}, "demo_zip": {demo}, "companion_zip": {core, imp}})
	if !strings.Contains(r.Request.URL.RawQuery, "msg=") {
		t.Fatalf("add: %s", r.Request.URL)
	}
	th, err := engine.Catalog().Get("emart")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range th.Plugins {
		got = append(got, p.Slug+":"+p.Source)
	}
	if strings.Join(got, ",") != "woocommerce:wporg,emart-core:library,saayeh-demo-importer:library" {
		t.Fatalf("plugins: %v", got)
	}
	if engine.Catalog().LibraryPath("emart-core") == "" || engine.Catalog().LibraryPath(catalog.ImporterSlug) == "" {
		t.Fatal("companions not in the library")
	}
	pg = e.page("themes")
	if !strings.Contains(pg, `<span dir="ltr">emart-core</span> · کتابخانه`) || !strings.Contains(pg, `<span dir="ltr">woocommerce</span> · wp.org`) {
		t.Error("plugin source badges")
	}
	// core removed from the library → red badge on the card
	_ = engine.Catalog().DeletePlugin("emart-core")
	pg = e.page("themes")
	if !strings.Contains(pg, `<span dir="ltr">emart-core</span> · ⚠ zip ندارد`) {
		t.Error("missing-zip badge")
	}
}
