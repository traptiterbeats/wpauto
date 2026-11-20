package web

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/traptiterbeats/wpauto/internal/projects"
)

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	s.render(w, "projects.html", s.page(r, map[string]any{
		"List": s.projects.List(), "Hosts": s.store.List(), "Error": r.URL.Query().Get("err"),
	}))
}

func (s *Server) handleProjectNew(w http.ResponseWriter, r *http.Request) {
	p := projects.Project{Name: r.FormValue("name"), HostID: r.FormValue("host"),
		Domain: strings.ToLower(strings.TrimSpace(r.FormValue("domain")))}
	if d, f, ok := strings.Cut(p.Domain, "/"); ok {
		p.Domain, p.Folder = d, strings.Trim(f, "/")
	}
	if _, err := s.projects.Add(p); err != nil {
		http.Redirect(w, r, s.cfg.BasePath+"projects?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, s.cfg.BasePath+"?msg="+url.QueryEscape("پروژه‌ی «"+strings.TrimSpace(p.Name)+"» ساخته و فعال شد. از مرحله‌ی ۱ شروع کنید."), http.StatusSeeOther)
}

func (s *Server) handleProjectOp(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var err error
	msg := ""
	switch r.PathValue("op") {
	case "activate":
		if id == "none" {
			id = ""
		}
		err = s.projects.SetActive(id)
	case "delete":
		err, msg = s.projects.Delete(id), "پروژه حذف شد؛ اجراها و پیش‌نویس‌هایش سر جایشان هستند."
	default:
		http.NotFound(w, r)
		return
	}
	back := s.safeBack(r.FormValue("back"))
	if back == "" {
		back = s.cfg.BasePath + "projects"
	}
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	if err != nil {
		http.Redirect(w, r, back+sep+"err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	if msg != "" {
		back += sep + "msg=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// projectSite works out which site a run is for, so the project remembers it on success.
func projectSite(wfID string, in map[string]string) (domain, folder string) {
	switch wfID {
	case "wp-install":
		if in["mode"] == "subfolder" {
			return in["domain"], in["name"]
		}
		if in["name"] != "" && in["domain"] != "" {
			return in["name"] + "." + in["domain"], ""
		}
	case "wp-check-domain":
		d, f, _ := strings.Cut(in["domain"], "/")
		return d, strings.Trim(f, "/")
	case "theme-install":
		return in["domain"], strings.Trim(in["folder"], "/")
	}
	return "", ""
}
