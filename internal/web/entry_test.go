package web

import (
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/traptiterbeats/wpauto/internal/catalog"
	"github.com/traptiterbeats/wpauto/internal/catalog/catalogtest"
	"github.com/traptiterbeats/wpauto/internal/engine"
	"github.com/traptiterbeats/wpauto/internal/projects"
)

// entryEnv is a logged-in panel with two demo themes and an active project.
func entryEnv(t *testing.T) *homeEnv {
	t.Helper()
	e := newBareEnv(t)
	src := t.TempDir()
	cat := engine.Catalog()
	if _, err := cat.AddPlugin(catalogtest.Plugin(t, src, catalog.ImporterSlug, "")); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"acme", "zeta"} {
		if _, err := cat.Add(catalog.AddInput{ThemeZip: catalogtest.Theme(t, src, slug), DemoZip: catalogtest.RichDemo(t, src, slug, false)}); err != nil {
			t.Fatal(err)
		}
	}
	e.create("Naranj", "shop.example.test", "")
	return e
}

// The entry page: «پیش‌نویس تازه» form on top, earlier drafts below, each openable.
func TestCustomizeEntryOrder(t *testing.T) {
	e := entryEnv(t)
	d, err := engine.Drafts().New("acme", "کافه قدیمی")
	if err != nil {
		t.Fatal(err)
	}
	pg := e.page("customize")
	form, list := strings.Index(pg, `id="newdraft"`), strings.Index(pg, `id="drafts"`)
	if form < 0 || list < 0 || form > list {
		t.Fatalf("form at %d, list at %d: the form must come first", form, list)
	}
	if strings.Index(pg, "پیش‌نویس تازه") > strings.Index(pg, "پیش‌نویس‌های قبلی") {
		t.Error("headings out of order")
	}
	if !strings.Contains(pg[list:], `href="`+e.s.cfg.BasePath+`customize/`+d.ID+`"`) || !strings.Contains(pg[list:], "ادامه / ویرایش") {
		t.Error("earlier draft is not openable from the list")
	}
	if !strings.Contains(pg, `class="theme-card"`) || !strings.Contains(pg, `id="backbtn"`) {
		t.Error("theme grid or Back missing")
	}
	sameCSRF(t, "customize", pg)
	if b, _ := e.get("customize/" + d.ID + "?step=texts"); !strings.Contains(b, "کافه قدیمی") {
		t.Error("earlier draft does not open")
	}
	// the project's WordPress card starts the customisation from the entry page, even with a draft
	pid := e.mustActive(t)
	if err := e.s.projects.Update(pid, func(x *projects.Project) { x.DraftID = d.ID }); err != nil {
		t.Fatal(err)
	}
	wp := e.page("p/" + pid + "/wordpress")
	if strings.Contains(wp, `href="`+e.s.cfg.BasePath+`customize/`+d.ID+`"`) || !strings.Contains(wp, `href="`+e.s.cfg.BasePath+`customize"`) {
		t.Error("WordPress page links past the entry page")
	}
	if pg := e.page("customize"); !strings.Contains(pg, "پیش‌نویس این پروژه") {
		t.Error("the project's draft is not marked in the list")
	}
}

func (e *homeEnv) mustActive(t *testing.T) string {
	t.Helper()
	p, ok := e.s.projects.Active()
	if !ok {
		t.Fatal("no active project")
	}
	return p.ID
}

// A name already in the list (any case / spacing) is refused on the same page, values kept.
func TestCustomizeDuplicateName(t *testing.T) {
	e := entryEnv(t)
	if _, err := engine.Drafts().New("acme", "Cafe Naranj"); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Drafts().New("acme", "کافه نارنج"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Cafe Naranj", "cafe naranj", "  CAFE   NARANJ ", "\tCafe Naranj\n", " كافه  نارنج"} {
		pg := e.page("customize")
		r, body := e.post("customize/new", url.Values{"csrf": {csrfOf(t, pg)}, "title": {name}, "theme": {"zeta"}})
		if r.StatusCode != http.StatusConflict {
			t.Errorf("%q: status %d", name, r.StatusCode)
		}
		if r.Request.URL.Path != e.s.cfg.BasePath+"customize/new" {
			t.Errorf("%q: redirected to %s", name, r.Request.URL)
		}
		if !strings.Contains(body, "این نام قبلاً در فهرست پایین هست؛ فهرست را بررسی کنید یا نام دیگری انتخاب کنید.") {
			t.Errorf("%q: Persian error missing", name)
		}
		kept := strings.TrimSpace(name)
		if !strings.Contains(body, `placeholder="مثلاً کافه نارنج" value="`+html.EscapeString(kept)+`"`) {
			t.Errorf("%q: typed name not kept", name)
		}
		if !strings.Contains(body, `value="zeta" checked`) {
			t.Errorf("%q: picked theme not kept", name)
		}
		if strings.Index(body, `id="newdraft"`) > strings.Index(body, `id="drafts"`) {
			t.Errorf("%q: error page lost the order", name)
		}
		sameCSRF(t, "duplicate", body)
		if n := len(engine.Drafts().List()); n != 2 {
			t.Fatalf("%q: a draft was created (%d)", name, n)
		}
	}
}

// A new name makes the draft and goes straight to the brief tab, with the other tabs.
func TestCustomizeNewNameGoesToBrief(t *testing.T) {
	e := entryEnv(t)
	if _, err := engine.Drafts().New("acme", "Cafe Naranj"); err != nil {
		t.Fatal(err)
	}
	pg := e.page("customize")
	r, body := e.post("customize/new", url.Values{"csrf": {csrfOf(t, pg)}, "title": {"Cafe Naranj 2"}, "theme": {"zeta"}})
	if r.StatusCode != 200 || !strings.HasPrefix(r.Request.URL.Path, e.s.cfg.BasePath+"customize/") || r.Request.URL.Query().Get("step") != "brief" {
		t.Fatalf("not on the brief tab: %d %s", r.StatusCode, r.Request.URL)
	}
	if len(engine.Drafts().List()) != 2 {
		t.Fatal("draft not created")
	}
	if !strings.Contains(body, `id="brief"`) || !strings.Contains(body, "۲. لوگو و تصاویر") || !strings.Contains(body, "۴. مرور و ذخیره") {
		t.Error("brief tab or the other tabs missing")
	}
	id := strings.TrimPrefix(r.Request.URL.Path, e.s.cfg.BasePath+"customize/")
	if p, _ := e.s.projects.Active(); p.DraftID != id {
		t.Error("draft not linked to the project")
	}
}

// Deep links without an existing draft go back to the entry page.
func TestCustomizeDeepLinkWithoutDraft(t *testing.T) {
	e := entryEnv(t)
	for _, path := range []string{"customize/abcdef012345?step=brief", "customize/abcdef012345?step=texts", "customize/nope?step=finish", "customize/new"} {
		body, r := e.get(path)
		if r.StatusCode != 200 || r.Request.URL.Path != e.s.cfg.BasePath+"customize" {
			t.Errorf("%s: ended at %d %s", path, r.StatusCode, r.Request.URL)
			continue
		}
		if !strings.Contains(body, "این پیش‌نویس پیدا نشد") || !strings.Contains(body, `id="newdraft"`) {
			t.Errorf("%s: entry page without the message", path)
		}
	}
	// a POST step on a missing draft lands on the entry page too
	pg := e.page("customize")
	r, _ := e.post("customize/abcdef012345/brief", url.Values{"csrf": {csrfOf(t, pg)}, "site_name": {"x"}})
	if r.Request.URL.Path != e.s.cfg.BasePath+"customize" {
		t.Errorf("POST on missing draft ended at %s", r.Request.URL)
	}
}
