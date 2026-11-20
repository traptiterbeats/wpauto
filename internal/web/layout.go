package web

import (
	"net/http"
	"strings"

	"github.com/traptiterbeats/wpauto/internal/customize"

	"github.com/traptiterbeats/wpauto/internal/ai"
	"github.com/traptiterbeats/wpauto/internal/engine"
)

// section is one top menu entry.
type section struct {
	Key, Title, Path string
}

var sections = []section{
	{"tasks", "کارها", ""},
	{"hosts", "هاست‌ها", "hosts"},
	{"themes", "قالب‌ها", "themes"},
	{"customize", "شخصی‌سازی", "customize"},
	{"ai", "هوش مصنوعی", "ai"},
	{"runs", "اجراها", "runs"},
}

// sectionFor maps a request path (below the base path) to its menu.
func sectionFor(rel string) section {
	first, _, _ := strings.Cut(rel, "/")
	switch first {
	case "", "run":
		return sections[0]
	case "plugins":
		return sections[2]
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

// pipeStep is one stage of building a client's site, shown in order in the side panel.
type pipeStep struct {
	Key, Title, Link, State, Note string // State: done | todo
	Current                       bool
}

// navItem is one clickable part of the current section, shown in the breadcrumb bar.
type navItem struct {
	Title, Link  string
	Active, Done bool
}

// pipeline builds the site-building path: host → WordPress → customise → theme. The stage the
// page belongs to is marked current; its own step list is shown nested under it.
func (s *Server) pipeline(rel string, d map[string]any, rd readiness) []pipeStep {
	bp := s.cfg.BasePath
	ok := map[string]bool{}
	for _, run := range s.runner.List(200) {
		if run.Status == engine.StatusSuccess {
			ok[run.WorkflowID] = true
		}
	}
	custDone := false
	for _, dr := range engine.Drafts().List() {
		if dr.Generated > 0 || len(dr.Values) > 0 {
			custDone = true
		}
	}
	custLink, themeLink := bp+"customize", bp+"run/theme-install"
	if dr, isDraft := d["D"].(*customize.Draft); isDraft && dr != nil {
		custLink = bp + "customize/" + dr.ID
		themeLink += "?theme=" + dr.Theme + "&draft=" + dr.ID
		custDone = dr.Generated > 0 || len(dr.Values) > 0
	}
	st := func(done bool) string {
		if done {
			return "done"
		}
		return "todo"
	}
	p := []pipeStep{
		{Key: "host", Title: "۱. هاست و بررسی آن", Link: bp + "hosts", State: st(rd.CheckedHosts > 0)},
		{Key: "wp", Title: "۲. نصب وردپرس", Link: bp + "run/wp-install", State: st(ok["wp-install"]), Note: "اگر وردپرس از قبل نصب است، لازم نیست"},
		{Key: "customize", Title: "۳. شخصی‌سازی برای مشتری", Link: custLink, State: st(custDone)},
		{Key: "theme", Title: "۴. نصب قالب و دموی شخصی‌شده", Link: themeLink, State: st(ok["theme-install"])},
	}
	cur := ""
	first, rest, _ := strings.Cut(rel, "/")
	switch {
	case first == "hosts" || rel == "run/host-check" || rel == "run/wp-check-domain":
		cur = "host"
	case rel == "run/wp-install":
		cur = "wp"
	case first == "customize":
		cur = "customize"
	case rel == "run/theme-install":
		cur = "theme"
	case first == "runs" && rest != "":
		if run, isRun := d["Run"].(*engine.Run); isRun && run != nil {
			cur = map[string]string{"host-check": "host", "wp-check-domain": "host", "wp-install": "wp", "theme-install": "theme"}[run.WorkflowID]
		}
	}
	for i := range p {
		p[i].Current = p[i].Key == cur
		if p[i].State == "done" {
			p[i].Note = ""
		}
	}
	return p
}

// layout adds the menu, breadcrumb and side panel data every page uses.
func (s *Server) layout(r *http.Request, d map[string]any) {
	rel := strings.TrimPrefix(r.URL.Path, s.cfg.BasePath)
	sec := sectionFor(rel)
	d["Sections"] = sections
	d["Section"] = sec
	rd := s.readiness()
	d["Ready"] = rd
	d["Pipeline"] = s.pipeline(rel, d, rd)
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
