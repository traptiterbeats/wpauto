package web

import (
	"net/http"
	"strings"

	"github.com/traptiterbeats/wpauto/internal/catalog"
	"github.com/traptiterbeats/wpauto/internal/engine"
	"github.com/traptiterbeats/wpauto/internal/projects"
	"github.com/traptiterbeats/wpauto/internal/store"
)

// taskScope is what a task form opened for a project takes from the project instead of asking:
// the project's own host and the site address entered when the project was made. Those values are
// shown read-only at the top of the form, and the server uses them whatever the form posts.
type taskScope struct {
	P        projects.Project
	Host     store.Host
	HasHost  bool
	Addr     siteAddr        // Kind is "" when the project has no address yet
	Locked   map[string]bool // workflow inputs taken from the project (not shown, posted values ignored)
	Values   map[string]string
	ShowAddr bool   // show the address line although no input is locked (pages without a workflow)
	NeedAddr bool   // the task works on the site address and the project has none yet
	AddrNote string // «آدرس نصب» or «آدرس سایت»
	HostLink string // the project's «بررسی هاست»
	AddrLink string // where the project's address is entered (its page)
}

// HostText is the read-only host line: «autopilo.ir — کاربر kfncspot».
func (sc *taskScope) HostText() string {
	if !sc.HasHost {
		return ""
	}
	t := hostLabel(sc.Host)
	if sc.Host.Username != "" {
		t += " — کاربر " + sc.Host.Username
	}
	return t
}

// Site is the project's address as shown (domain/folder).
func (sc *taskScope) Site() string { return sc.P.Site() }

// taskProject is the project a task form or its POST works for: the one named by the `project`
// field (query or form) when it exists, else the active project.
func (s *Server) taskProject(r *http.Request) (projects.Project, bool) {
	if id := strings.TrimSpace(r.FormValue("project")); id != "" {
		if p, ok := s.projects.Get(id); ok {
			return p, true
		}
	}
	return s.projects.Active()
}

// projectLocks returns the inputs of wf a project decides and their values. wp-install takes its
// mode, main domain and subdomain/folder name from the project's address; any other workflow
// takes `domain` (and `folder` when it has one) from it. Nothing is locked while the project has
// no address, so the form still asks for it.
func projectLocks(wf *engine.Workflow, p projects.Project) map[string]string {
	out := map[string]string{}
	if wf.ID == "wp-install" {
		if mode, dom, name, ok := installInputs(p); ok {
			out["mode"], out["domain"], out["name"] = mode, dom, name
		}
		return out
	}
	if p.Domain == "" {
		return out
	}
	has := map[string]bool{}
	for _, in := range wf.Inputs {
		has[in.Name] = true
	}
	if has["domain"] {
		if has["folder"] {
			out["domain"], out["folder"] = p.Domain, p.Folder
		} else {
			out["domain"] = p.Site()
		}
	}
	return out
}

// scopeFor builds the read-only part of a task form for a project.
func (s *Server) scopeFor(wf *engine.Workflow, p projects.Project) *taskScope {
	sc := &taskScope{P: p, Addr: projectAddr(p), Locked: map[string]bool{}, AddrNote: "آدرس سایت",
		HostLink: s.cfg.BasePath + "p/" + p.ID + "/host"}
	sc.Host, sc.HasHost = s.ownHost(p)
	if wf != nil {
		sc.Values = projectLocks(wf, p)
		for k := range sc.Values {
			sc.Locked[k] = true
		}
		if wf.ID == "wp-install" || wf.ID == "theme-install" {
			sc.AddrNote = "آدرس نصب"
		}
		sc.NeedAddr = needsAddr(wf) && len(sc.Values) == 0
		sc.AddrLink = s.cfg.BasePath + "p/" + p.ID + "#address"
	}
	return sc
}

// applyLocks overwrites what the form posted with the project's values.
func (sc *taskScope) applyLocks(inputs map[string]string) {
	for k, v := range sc.Values {
		inputs[k] = v
	}
}

// titled shows runs under their task's current title from the workflow registry, so a renamed task
// (e.g. «بررسی وردپرس دامنه», once «بررسی وردپرس یک دامنه») never shows its old stored name.
func (s *Server) titled(list []engine.Run) []engine.Run {
	byID := map[string]string{}
	for _, wf := range s.workflows() {
		byID[wf.ID] = wf.Title
	}
	for i := range list {
		if t := byID[list[i].WorkflowID]; t != "" {
			list[i].Title = t
		}
	}
	return list
}

// needsAddr reports whether a workflow works on the site's address (wp-install, or any task with
// a `domain` input). Inside a project that address always comes from the project.
func needsAddr(wf *engine.Workflow) bool {
	if wf.ID == "wp-install" {
		return true
	}
	for _, in := range wf.Inputs {
		if in.Name == "domain" {
			return true
		}
	}
	return false
}

// themePick is the data of the theme picker («themepick»): a grid of radio cards with thumbnails.
type themePick struct {
	Name, Selected, BasePath string
	Themes                   []*catalog.Theme
	Bad                      map[string]bool // not a standard demo (shown as a warning on the card)
}

func (s *Server) themePicker(name, selected string, themes []*catalog.Theme, std map[string]stdView) themePick {
	tp := themePick{Name: name, Selected: selected, BasePath: s.cfg.BasePath, Themes: themes, Bad: map[string]bool{}}
	for slug, v := range std {
		if !v.NoDemo && !v.OK {
			tp.Bad[slug] = true
		}
	}
	return tp
}

// themeInput is the form name of a workflow's theme input ("in_theme").
func themeInput(wf *engine.Workflow) string {
	for _, in := range wf.Inputs {
		if in.Type == "theme" {
			return "in_" + in.Name
		}
	}
	return "in_theme"
}
