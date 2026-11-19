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

func (s *Server) handleThemes(w http.ResponseWriter, r *http.Request) {
	cat := engine.Catalog()
	s.render(w, "themes.html", s.page(r, map[string]any{
		"Themes": cat.List(), "Library": cat.Library(), "Importer": catalog.ImporterSlug,
		"HasImporter": cat.LibraryPath(catalog.ImporterSlug) != "",
		"Error": r.URL.Query().Get("err"), "Msg": r.URL.Query().Get("msg"),
	}))
}

func (s *Server) back(w http.ResponseWriter, r *http.Request, key, msg string) {
	http.Redirect(w, r, s.cfg.BasePath+"themes?"+key+"="+url.QueryEscape(msg), http.StatusSeeOther)
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
	split := func(v string) []string {
		return strings.FieldsFunc(v, func(c rune) bool { return c == ',' || c == '،' || c == ' ' || c == '\n' })
	}
	t, err := engine.Catalog().Add(catalog.AddInput{
		ThemeZip: themeZip, DemoZip: demoZip, WPOrg: split(r.FormValue("wporg")),
		Library: r.MultipartForm.Value["library"], DemoGroups: r.MultipartForm.Value["groups"],
	})
	if err != nil {
		s.back(w, r, "err", err.Error())
		return
	}
	s.back(w, r, "msg", "قالب «"+t.Name+"» به کاتالوگ اضافه شد")
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
