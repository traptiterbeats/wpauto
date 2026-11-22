package web

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/traptiterbeats/wpauto/internal/catalog"
	"github.com/traptiterbeats/wpauto/internal/engine"
)

// maxUpload bounds theme, demo and plugin uploads.
const maxUpload = 512 << 20

// uploads checks the session before reading a (large) multipart body, then hands over to
// requireAuth, which also checks the CSRF token.
func (s *Server) uploads(h http.HandlerFunc) http.HandlerFunc {
	inner := s.requireAuth(h)
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			http.Redirect(w, r, s.cfg.BasePath+"login", http.StatusSeeOther)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, "upload too large or invalid", http.StatusRequestEntityTooLarge)
			return
		}
		defer r.MultipartForm.RemoveAll()
		inner(w, r)
	}
}

// handleThemes draws «مدیریت قالب‌ها (ادمین)»: the global theme catalog (shared by every
// project) and the plugin library. Opened from a project (p/{id}/wordpress/themes or
// themes?from=<id>) it keeps that project's breadcrumb and Back, and its forms post `from`.
func (s *Server) handleThemes(w http.ResponseWriter, r *http.Request) {
	bp := s.cfg.BasePath
	cat := engine.Catalog()
	data := map[string]any{
		"Themes": cat.List(), "Library": cat.Library(), "Importer": catalog.ImporterSlug,
		"HasImporter": cat.LibraryPath(catalog.ImporterSlug) != "", "Std": standards(), "InLib": inLibrary(cat),
		"Error": r.URL.Query().Get("err"), "Msg": r.URL.Query().Get("msg"), "From": "",
		"Crumbs": []crumb{{"پروژه‌ها", bp}, {"ابزارها", bp + "tools"}, {"مدیریت قالب‌ها", ""}},
	}
	if r.PathValue("id") != "" {
		if _, ok := s.projectOr404(w, r); !ok {
			return
		}
	}
	if p, ok := s.themeProject(r); ok {
		data["From"] = p.ID
		data["FromProject"] = p
		data["Crumbs"] = []crumb{{"پروژه‌ها", bp}, {p.Name, bp + "p/" + p.ID}, {"وردپرس", bp + "p/" + p.ID + "/wordpress"}, {"مدیریت قالب‌ها", ""}}
	}
	s.render(w, "themes.html", s.page(r, data))
}

// inLibrary is the set of plugin slugs whose zip is in the library (theme cards mark each
// plugin «کتابخانه», «wp.org» or «⚠ zip ندارد» from it).
func inLibrary(cat *catalog.Catalog) map[string]bool {
	out := map[string]bool{}
	for _, p := range cat.Library() {
		out[p.Slug] = true
	}
	return out
}

// back redirects to the theme catalog page with a message, inside the project the form was
// posted from (`from`) when there is one.
func (s *Server) back(w http.ResponseWriter, r *http.Request, key, msg string) {
	id := ""
	if p, ok := s.themeProject(r); ok {
		id = p.ID
	}
	http.Redirect(w, r, themesLink(s.cfg.BasePath, id)+"?"+key+"="+url.QueryEscape(msg), http.StatusSeeOther)
}

// formFile saves an uploaded file to a temp file; "" when the field is empty.
func formFile(r *http.Request, field string, i int) (string, error) {
	if r.MultipartForm == nil || len(r.MultipartForm.File[field]) <= i {
		return "", nil
	}
	fh := r.MultipartForm.File[field][i]
	if fh.Size == 0 {
		return "", nil
	}
	if !strings.HasSuffix(strings.ToLower(fh.Filename), ".zip") {
		return "", errNotZip
	}
	f, err := fh.Open()
	if err != nil {
		return "", err
	}
	defer f.Close()
	tmp, err := os.CreateTemp("", "wpauto-upload-*.zip")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(tmp, f); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), tmp.Close()
}

type uploadErr string

func (e uploadErr) Error() string { return string(e) }

const errNotZip = uploadErr("فقط فایل zip پذیرفته می‌شود")

func (s *Server) handleThemeAdd(w http.ResponseWriter, r *http.Request) {
	themeZip, err := formFile(r, "theme_zip", 0)
	if err != nil || themeZip == "" {
		s.back(w, r, "err", "فایل zip قالب را انتخاب کنید")
		return
	}
	defer os.Remove(themeZip)
	demoZip, err := formFile(r, "demo_zip", 0)
	if err != nil {
		s.back(w, r, "err", err.Error())
		return
	}
	if demoZip != "" {
		defer os.Remove(demoZip)
	}
	// Companion plugins (a theme's own core plugin, the Saayeh importer…) go to the library
	// first, so the demo's requires resolve to them; if adding the theme fails they are rolled back.
	var companions []string
	if r.MultipartForm != nil {
		for i := range r.MultipartForm.File["companion_zip"] {
			p, err := formFile(r, "companion_zip", i)
			if err != nil {
				s.back(w, r, "err", "افزونه‌ی همراه: "+err.Error())
				return
			}
			if p != "" {
				defer os.Remove(p)
				companions = append(companions, p)
			}
		}
	}
	cat := engine.Catalog()
	infos, undo, err := cat.AddPlugins(companions)
	if err != nil {
		s.back(w, r, "err", "افزونه‌ی همراه: "+err.Error()+" (چیزی اضافه نشد)")
		return
	}
	var slugs, names []string
	for _, in := range infos {
		slugs = append(slugs, in.Slug)
		names = append(names, strings.TrimSpace(in.Name+" "+in.Version))
	}
	split := func(v string) []string {
		return strings.FieldsFunc(v, func(c rune) bool { return c == ',' || c == '،' || c == ' ' || c == '\n' })
	}
	t, err := cat.Add(catalog.AddInput{
		ThemeZip: themeZip, DemoZip: demoZip, WPOrg: split(r.FormValue("wporg")),
		Library: r.MultipartForm.Value["library"], Companions: slugs, DemoGroups: r.MultipartForm.Value["groups"],
	})
	if err != nil {
		undo()
		msg := err.Error()
		if len(infos) > 0 {
			msg += " (افزونه‌های همراه هم به کتابخانه اضافه نشدند)"
		}
		s.back(w, r, "err", msg)
		return
	}
	msg := "قالب «" + t.Name + "» به کاتالوگ اضافه شد"
	if len(names) > 0 {
		msg += "؛ افزونه‌های همراه در کتابخانه: " + strings.Join(names, "، ")
	}
	s.back(w, r, "msg", msg)
}

func (s *Server) handleThemeDelete(w http.ResponseWriter, r *http.Request) {
	if err := engine.Catalog().Delete(r.PathValue("slug")); err != nil {
		s.back(w, r, "err", err.Error())
		return
	}
	s.back(w, r, "msg", "قالب از کاتالوگ حذف شد (به سایت‌ها دست نمی‌زند)")
}

func (s *Server) handleThemeShot(w http.ResponseWriter, r *http.Request) {
	t, err := engine.Catalog().Get(r.PathValue("slug"))
	if err != nil || t.Screenshot == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeFile(w, r, filepath.Join(t.Dir, filepath.Base(t.Screenshot)))
}

func (s *Server) handlePluginAdd(w http.ResponseWriter, r *http.Request) {
	n := 0
	if r.MultipartForm != nil {
		n = len(r.MultipartForm.File["plugin_zip"])
	}
	if n == 0 {
		s.back(w, r, "err", "فایل zip افزونه را انتخاب کنید")
		return
	}
	var names []string
	for i := 0; i < n; i++ {
		p, err := formFile(r, "plugin_zip", i)
		if err != nil {
			s.back(w, r, "err", err.Error())
			return
		}
		if p == "" {
			continue
		}
		info, err := engine.Catalog().AddPlugin(p)
		os.Remove(p)
		if err != nil {
			s.back(w, r, "err", err.Error())
			return
		}
		names = append(names, info.Name+" "+info.Version)
	}
	s.back(w, r, "msg", "به کتابخانه اضافه شد: "+strings.Join(names, "، "))
}

func (s *Server) handlePluginDelete(w http.ResponseWriter, r *http.Request) {
	if err := engine.Catalog().DeletePlugin(r.PathValue("slug")); err != nil {
		s.back(w, r, "err", "حذف نشد")
		return
	}
	s.back(w, r, "msg", "افزونه از کتابخانه حذف شد")
}
