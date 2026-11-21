package web

import (
	"fmt"

	"github.com/traptiterbeats/wpauto/internal/engine"
	"net/http"
	"net/url"
	"slices"
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
	back := s.safeBack(r.FormValue("back"))
	if back == "" {
		back = s.cfg.BasePath + "projects"
	}
	np, err := s.projects.Add(p)
	if err != nil {
		sep := "?"
		if strings.Contains(back, "?") {
			sep = "&"
		}
		http.Redirect(w, r, back+sep+"err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	// straight to the first stage of the path
	first := s.pipeline(&np)[0].Link
	http.Redirect(w, r, first+"&msg="+url.QueryEscape("پروژه‌ی «"+np.Name+"» ساخته شد. مرحله‌ی ۱: بررسی هاست."), http.StatusSeeOther)
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
		err, msg = s.deleteProject(id, r.FormValue("purge") != "")
	case "mark":
		stage, on := r.FormValue("stage"), r.FormValue("on") != ""
		if !slices.Contains(pipelineKeys, stage) {
			http.NotFound(w, r)
			return
		}
		err = s.projects.Update(id, func(p *projects.Project) {
			if p.Marked == nil {
				p.Marked = map[string]bool{}
			}
			if on {
				p.Marked[stage] = true
			} else {
				delete(p.Marked, stage)
			}
		})
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
		if in["mode"] == "root" {
			return in["domain"], ""
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

// projectCard is one earlier project on the home page, with how far it got.
type projectCard struct {
	projects.Project
	Stages      []pipeStep
	Done, Total int
	Next        *pipeStep // first stage not done yet; nil when finished
	Active      bool
}

func (s *Server) projectCards() []projectCard {
	act, _ := s.projects.Active()
	var out []projectCard
	for _, p := range s.projects.List() {
		c := projectCard{Project: p, Stages: s.pipeline(&p), Active: p.ID == act.ID}
		c.Total = len(c.Stages)
		for i := range c.Stages {
			if c.Stages[i].State == "done" {
				c.Done++
			} else if c.Next == nil {
				c.Next = &c.Stages[i]
			}
		}
		out = append(out, c)
	}
	return out
}

// deleteProject removes a project and, with purge, its finished runs and its draft
// (unless another project uses the same draft). The site on the host is not touched.
func (s *Server) deleteProject(id string, purge bool) (error, string) {
	p, ok := s.projects.Get(id)
	if !ok {
		return projects.ErrNotFound, ""
	}
	if err := s.projects.Delete(id); err != nil {
		return err, ""
	}
	if !purge {
		return nil, "پروژه حذف شد؛ اجراها و پیش‌نویسش ماندند. سایت روی هاست دست نخورد."
	}
	runs := s.runner.DeleteProjectRuns(id)
	drafts := 0
	if p.DraftID != "" {
		shared := false
		for _, o := range s.projects.List() {
			if o.DraftID == p.DraftID {
				shared = true
			}
		}
		if !shared && engine.Drafts().Delete(p.DraftID) == nil {
			drafts = 1
		}
	}
	return nil, fmt.Sprintf("پروژه حذف شد، با %d اجرا و %d پیش‌نویس. سایت روی هاست دست نخورد.", runs, drafts)
}
