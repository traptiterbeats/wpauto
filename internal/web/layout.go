package web

import (
	"net/http"
	"strings"

	"github.com/traptiterbeats/wpauto/internal/projects"

	"github.com/traptiterbeats/wpauto/internal/ai"
	"github.com/traptiterbeats/wpauto/internal/engine"
)

// section is one top menu entry.
type section struct {
	Key, Title, Path string
}

var sections = []section{
	{"tasks", "کارها", ""},
	{"projects", "پروژه‌ها", "projects"},
	{"hosts", "هاست‌ها", "hosts"},
	{"themes", "قالب‌ها", "themes"},
	{"customize", "شخصی‌سازی", "customize"},
	{"ai", "هوش مصنوعی", "ai"},
}

// sectionFor maps a request path (below the base path) to its menu.
func sectionFor(rel string) section {
	first, _, _ := strings.Cut(rel, "/")
	switch first {
	case "", "run":
		return sections[0]
	case "plugins":
		return sections[3] // themes
	}
	for _, s := range sections {
		if s.Path == first {
			return s
		}
	}
	return sections[0]
}

// readiness is the always-visible checklist in the side panel.
type readiness struct {
	Hosts, CheckedHosts, Themes int
	AI                          *ai.Provider
	AICount                     int
}

// AIState is "none", "nomodel", "ok", "failed" or "untested".
func (r readiness) AIState() string {
	switch {
	case r.AI == nil:
		return "none"
	case r.AI.Model == "":
		return "nomodel"
	case !r.AI.Tested():
		return "untested"
	case r.AI.TestOK:
		return "ok"
	}
	return "failed"
}

func (s *Server) readiness() readiness {
	rd := readiness{Themes: len(engine.Catalog().List())}
	for _, h := range s.store.List() {
		rd.Hosts++
		if h.LastReport != nil {
			rd.CheckedHosts++
		}
	}
	list := s.ai.List()
	rd.AICount = len(list)
	if p, ok := s.ai.Get(""); ok {
		rd.AI = &p
	}
	return rd
}

// sideStep is one line of the contextual step list in the side panel.
type sideStep struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	State string `json:"state"` // yes | no | maybe | done | todo | blocked | optional
	Note  string `json:"note,omitempty"`
	Link  string `json:"link,omitempty"`
}

// pipeStep is one stage of building the active project's site, shown in the fixed side panel.
type pipeStep struct {
	Key, Title, Link, State, Note string // State: todo | running | done | failed
	Alt, AltLink                  string // another way to complete the stage (e.g. just verify)
}

// navItem is one clickable part of the current section, shown in the breadcrumb bar.
type navItem struct {
	Title, Link  string
	Active, Done bool
}

// stageOf maps a workflow to the project stage it completes.
var stageOf = map[string]string{"host-check": "host", "wp-check-domain": "wp", "wp-install": "wp", "theme-install": "theme"}

// pipeline lights the stages of the active project: dark until a run of that stage succeeds for
// this project (a check counts as much as an install), so a new project starts all dark.
func (s *Server) pipeline(p *projects.Project) []pipeStep {
	bp := s.cfg.BasePath
	steps := []pipeStep{
		{Key: "host", Title: "بررسی هاست", Link: bp + "run/host-check"},
		{Key: "wp", Title: "وردپرس", Link: bp + "run/wp-install", Note: "نصب تازه", Alt: "یا تأیید وردپرس موجود", AltLink: bp + "run/wp-check-domain"},
		{Key: "customize", Title: "شخصی‌سازی برای مشتری", Link: bp + "customize"},
		{Key: "theme", Title: "نصب قالب و دمو", Link: bp + "run/theme-install"},
	}
	for i := range steps {
		steps[i].State = "todo"
	}
	if p == nil {
		return steps
	}
	q := "?project=" + p.ID
	if p.HostID != "" {
		q += "&host=" + p.HostID
	}
	for i := range steps {
		if steps[i].Key != "customize" {
			steps[i].Link += q
		}
		if steps[i].AltLink != "" {
			steps[i].AltLink += q
		}
	}
	idx := map[string]int{"host": 0, "wp": 1, "customize": 2, "theme": 3}
	// runs come newest first: the newest run decides running/failed, any success means done
	seen := map[string]bool{}
	for _, run := range s.runner.List(500) {
		if run.ProjectID != p.ID {
			continue
		}
		k, ok := stageOf[run.WorkflowID]
		if !ok {
			continue
		}
		st := &steps[idx[k]]
		switch {
		case run.Status == engine.StatusSuccess:
			st.State = "done"
		case !seen[k] && !run.Done():
			st.State = "running"
		case !seen[k] && st.State == "todo":
			st.State = "failed"
		}
		if run.Status == engine.StatusSuccess && run.WorkflowID == "wp-check-domain" {
			st.Note = "تأیید شد"
		}
		seen[k] = true
	}
	if p.DraftID != "" {
		st := &steps[2]
		st.Link = bp + "customize/" + p.DraftID
		if d, err := engine.Drafts().Get(p.DraftID); err == nil {
			switch {
			case d.Status == "generating":
				st.State = "running"
			case d.Generated > 0:
				st.State = "done"
			}
			steps[3].Link += "&theme=" + d.Theme + "&draft=" + d.ID
		}
	}
	return steps
}

// layout adds the menu, breadcrumb and side panel data every page uses.
func (s *Server) layout(r *http.Request, d map[string]any) {
	rel := strings.TrimPrefix(r.URL.Path, s.cfg.BasePath)
	sec := sectionFor(rel)
	d["Sections"] = sections
	d["Section"] = sec
	d["Ready"] = s.readiness()
	d["Projects"] = s.projects.List()
	if p, ok := s.projects.Active(); ok {
		d["Project"] = &p
		d["Pipeline"] = s.pipeline(&p)
	} else {
		d["Pipeline"] = s.pipeline(nil)
	}
	d["Recent"] = s.runner.List(12)
	d["Here"] = r.URL.Path
	if _, ok := d["Crumb"]; !ok {
		d["Crumb"] = ""
	}
	// pages that show their own messages set Msg/Error; the rest get a flash line
	q := r.URL.Query()
	if _, own := d["Error"]; !own && q.Get("err") != "" {
		d["Flash"], d["FlashKind"] = q.Get("err"), "err"
	} else if _, own := d["Msg"]; !own && q.Get("msg") != "" {
		d["Flash"], d["FlashKind"] = q.Get("msg"), "msg"
	}
}

// safeBack returns a redirect target inside the panel, or "".
func (s *Server) safeBack(back string) string {
	if strings.HasPrefix(back, s.cfg.BasePath) && !strings.HasPrefix(back, "//") && !strings.ContainsAny(back, "\\\r\n") {
		return back
	}
	return ""
}
