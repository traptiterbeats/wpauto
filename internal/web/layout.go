package web

import (
	"net/http"
	"strings"

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

// layout adds the menu, breadcrumb and side panel data every page uses.
func (s *Server) layout(r *http.Request, d map[string]any) {
	rel := strings.TrimPrefix(r.URL.Path, s.cfg.BasePath)
	sec := sectionFor(rel)
	d["Sections"] = sections
	d["Section"] = sec
	d["Ready"] = s.readiness()
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
