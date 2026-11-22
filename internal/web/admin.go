package web

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/traptiterbeats/wpauto/internal/projects"
)

// canAdmin is the single permission hook for admin work (today: the theme catalog — add/delete
// themes and demos, the plugin library). The panel has one logged-in user, who is the admin,
// so this is true unless the config turns admin pages off ("admin_ui": false in config.json).
// When users and roles arrive, decide here from the request's user.
func (s *Server) canAdmin(r *http.Request) bool {
	return s.cfg.AdminUIEnabled()
}

// adminOnly wraps an admin route: login first (as requireAuth), then canAdmin, else a Persian
// 403 page. It runs before the (large) multipart body of an upload is read.
func (s *Server) adminOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			http.Redirect(w, r, s.cfg.BasePath+"login", http.StatusSeeOther)
			return
		}
		if !s.canAdmin(r) {
			s.forbidden(w, r)
			return
		}
		h(w, r)
	}
}

// forbidden draws the «دسترسی ندارید» page with status 403.
func (s *Server) forbidden(w http.ResponseWriter, r *http.Request) {
	bp := s.cfg.BasePath
	cs := []crumb{{"پروژه‌ها", bp}, {"دسترسی ندارید", ""}}
	id := r.PathValue("id")
	if id == "" {
		id = r.URL.Query().Get("from") // never the body: a refused upload is not read
	}
	if p, ok := s.projects.Get(id); ok && id != "" {
		cs = []crumb{{"پروژه‌ها", bp}, {p.Name, bp + "p/" + p.ID}, {"وردپرس", bp + "p/" + p.ID + "/wordpress"}, {"دسترسی ندارید", ""}}
	}
	w.WriteHeader(http.StatusForbidden)
	s.render(w, "forbidden.html", s.page(r, map[string]any{"Crumbs": cs}))
}

// themesLink is the theme catalog page, inside a project when projectID is set.
func themesLink(bp, projectID string) string {
	if projectID == "" {
		return bp + "themes"
	}
	return bp + "p/" + url.PathEscape(projectID) + "/wordpress/themes"
}

// themeProject is the project the theme catalog page was opened from: the {id} of
// p/{id}/wordpress/themes, or `from` (query or posted form) on the global routes.
func (s *Server) themeProject(r *http.Request) (projects.Project, bool) {
	id := r.PathValue("id")
	if id == "" {
		id = strings.TrimSpace(r.URL.Query().Get("from"))
	}
	if id == "" && r.Method == http.MethodPost {
		id = strings.TrimSpace(r.FormValue("from"))
	}
	if id == "" {
		return projects.Project{}, false
	}
	return s.projects.Get(id)
}

// themesLinkFor is the «افزودن قالب» link of a task page: the project's theme page when the
// page works for a project (?project= or the active one), else the global page.
func (s *Server) themesLinkFor(r *http.Request) string {
	bp := s.cfg.BasePath
	if id := strings.TrimSpace(r.URL.Query().Get("project")); id != "" {
		if p, ok := s.projects.Get(id); ok {
			return themesLink(bp, p.ID)
		}
	}
	if p, ok := s.projects.Active(); ok {
		return themesLink(bp, p.ID)
	}
	return themesLink(bp, "")
}
