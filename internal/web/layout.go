package web

import (
	"net/http"
	"strings"
	"time"

	"github.com/traptiterbeats/wpauto/internal/projects"

	"github.com/traptiterbeats/wpauto/internal/ai"
	"github.com/traptiterbeats/wpauto/internal/engine"
)

// section is one top menu entry.
type section struct {
	Key, Title, Path string
}

var sections = []section{
	{"hosts", "هاست‌ها", "hosts"},
	{"tasks", "کارها", ""},
	{"projects", "پروژه‌ها", "projects"},
	{"themes", "قالب‌ها", "themes"},
	{"customize", "شخصی‌سازی", "customize"},
	{"ai", "هوش مصنوعی", "ai"},
	{"wordpress", "وردپرس‌ها", "wordpress"},
	{"autopilot", "🚀 ساخت خودکار سایت", "autopilot"},
}

// sectionFor maps a request path (below the base path) to its menu.
func sectionFor(rel string) section {
	first, _, _ := strings.Cut(rel, "/")
	switch first {
	case "", "run", "runs", "tasks":
		first = ""
	case "plugins":
		first = "themes"
	}
	for _, s := range sections {
		if s.Path == first {
			return s
		}
	}
	return sections[1] // کارها
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
	Marked                        bool   // ticked by hand
	ByRun                         bool   // a successful run completed it
}

// navItem is one clickable part of the current section, shown in the breadcrumb bar.
type navItem struct {
	Title, Link  string
	Active, Done bool
}

// stageOf maps a workflow to the project stage it completes.
var stageOf = map[string]string{"host-check": "host", "wp-install": "wpinstall", "wp-check-domain": "wpcheck", "theme-install": "theme"}

// pipelineKeys are the stages in the order they are done.
var pipelineKeys = []string{"host", "wpcheck", "wpinstall", "customize", "theme"}

// pipeline lights the stages of the active project: dark until a run of that stage succeeds for
// this project or the user ticks it by hand, so a new project starts all dark.
func (s *Server) pipeline(p *projects.Project) []pipeStep {
	bp := s.cfg.BasePath
	steps := []pipeStep{
		{Key: "host", Title: "بررسی هاست", Link: bp + "run/host-check"},
		{Key: "wpcheck", Title: "بررسی وردپرس", Link: bp + "run/wp-check-domain", Note: "کجا وردپرس نصب است"},
		{Key: "wpinstall", Title: "نصب وردپرس", Link: bp + "run/wp-install", Note: "اگر بررسی، وردپرس پیدا کند لازم نیست"},
		{Key: "customize", Title: "شخصی‌سازی برای مشتری", Link: bp + "customize"},
		{Key: "theme", Title: "نصب قالب و دمو", Link: bp + "run/theme-install"},
	}
	for i := range steps {
		steps[i].State = "todo"
	}
	if p == nil {
		return steps
	}
	idx := map[string]int{}
	for i, k := range pipelineKeys {
		idx[k] = i
	}
	q := "?project=" + p.ID
	if p.HostID != "" {
		q += "&host=" + p.HostID
	}
	for i := range steps {
		if steps[i].Key != "customize" {
			steps[i].Link += q
		}
	}
	// the host is entered and checked inside the project
	steps[idx["host"]].Link = bp + "p/" + p.ID + "/host"
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
			st.State, st.ByRun = "done", true
		case !seen[k] && !run.Done():
			st.State = "running"
		case !seen[k] && st.State == "todo":
			st.State = "failed"
		}
		// the check found WordPress: no install needed
		if run.WorkflowID == "wp-check-domain" && run.Status == engine.StatusSuccess && foundWP(run) {
			ins := &steps[idx["wpinstall"]]
			if ins.State != "done" {
				ins.State, ins.ByRun, ins.Note = "done", true, "از قبل نصب بود"
			}
		}
		seen[k] = true
	}
	if p.DraftID != "" {
		// the customisation always starts from its entry page; the project's draft is listed there
		st := &steps[idx["customize"]]
		if d, err := engine.Drafts().Get(p.DraftID); err == nil {
			switch {
			case d.Status == "generating":
				st.State = "running"
			case d.Finished || d.Generated > 0:
				st.State, st.ByRun = "done", true
			}
			steps[idx["theme"]].Link += "&theme=" + d.Theme + "&draft=" + d.ID
		}
	}
	for i := range steps {
		if p.Marked[steps[i].Key] {
			steps[i].Marked = true
			if steps[i].State != "running" {
				steps[i].State = "done"
			}
		}
		if steps[i].State == "done" && steps[i].Note != "از قبل نصب بود" {
			steps[i].Note = ""
		}
	}
	return steps
}

// crumb is one part of the breadcrumb (پروژه‌ها › نام پروژه › وردپرس › کار). The last one is the
// current page and has no link.
type crumb struct {
	Title, Link string
}

// tool is one entry of the small tools menu (every page that is not part of a project).
type tool struct {
	Title, Path, Note string
}

var tools = []tool{
	{"همه‌ی کارها", "tasks", "فهرست کامل کارها و پروژه‌ها به شکل قبلی"},
	{"اجراها", "runs", "تاریخچه‌ی همه‌ی اجراها"},
	{"فهرست پروژه‌ها", "projects", "ساختن، فعال کردن و حذف پروژه‌ها"},
	{"قالب‌ها و افزونه‌ها", "themes", "کاتالوگ قالب‌ها و کتابخانه‌ی افزونه‌ها"},
	{"شخصی‌سازی", "customize", "پیش‌نویس‌های شخصی‌سازی با هوش مصنوعی"},
	{"هوش مصنوعی", "ai", "سرویس‌ها، کلیدها و مدل‌ها"},
	{"وردپرس‌ها", "wordpress", "وردپرس‌های روی هاست و حذف آن‌ها"},
	{"ساخت خودکار سایت", "autopilot", "AutoPilot — همه‌ی مراحل، خودکار و پشت سر هم"},
	{"آمادگی پنل", "tools", "وضعیت هاست، هوش مصنوعی و قالب‌ها؛ آخرین اجراها"},
}

// tehranZone is Iran Standard Time; Iran has no daylight saving since 2022. The page clock is
// updated by JavaScript with the Asia/Tehran zone; this only renders its first value.
var tehranZone = time.FixedZone("Asia/Tehran", 3*3600+1800)

// projectScoped reports whether a page (path below the base path) is work done for a project.
func projectScoped(rel string) bool {
	first, rest, _ := strings.Cut(rel, "/")
	switch first {
	case "run", "customize", "autopilot", "wordpress":
		return true
	case "runs":
		return rest != ""
	}
	return false
}

// crumbs builds the breadcrumb of a page that did not set its own.
func (s *Server) crumbs(rel string, d map[string]any) []crumb {
	bp := s.cfg.BasePath
	out := []crumb{{"پروژه‌ها", bp}}
	sec := sectionFor(rel)
	first, rest, _ := strings.Cut(rel, "/")
	var p *projects.Project
	if rp, ok := d["RunProject"].(projects.Project); ok {
		p = &rp
	} else if ap, ok := d["Project"].(*projects.Project); ok {
		p = ap
	}
	if projectScoped(rel) && p != nil {
		out = append(out, crumb{p.Name, bp + "p/" + p.ID}, crumb{"وردپرس", bp + "p/" + p.ID + "/wordpress"})
	} else {
		out = append(out, crumb{"ابزارها", bp + "tools"})
	}
	switch {
	case first == "tools":
		out = out[:1]
		out = append(out, crumb{"ابزارها", bp + "tools"})
	case first == "tasks":
		out = append(out, crumb{"همه‌ی کارها", bp + "tasks"})
	case first == "runs" && rest == "":
		out = append(out, crumb{"اجراها", bp + "runs"})
	case first == "runs" || first == "run":
		// the task title is the Crumb
	default:
		out = append(out, crumb{sec.Title, bp + sec.Path})
	}
	if c, _ := d["Crumb"].(string); c != "" {
		link, _ := d["CrumbLink"].(string)
		out = append(out, crumb{c, link})
	}
	if len(out) > 1 && out[len(out)-1].Link == bp+rel {
		out[len(out)-1].Link = ""
	}
	return out
}

// backOf is where the Back button goes: the nearest crumb before the current page.
func backOf(cs []crumb) string {
	for i := len(cs) - 2; i >= 0; i-- {
		if cs[i].Link != "" {
			return cs[i].Link
		}
	}
	return ""
}

// layout adds the top bar, breadcrumb and shared data every page uses.
func (s *Server) layout(r *http.Request, d map[string]any) {
	rel := strings.TrimPrefix(r.URL.Path, s.cfg.BasePath)
	sec := sectionFor(rel)
	d["Sections"] = sections
	d["Section"] = sec
	d["Tools"] = tools
	d["Ready"] = s.readiness()
	d["Projects"] = s.projects.List()
	if p, ok := s.projects.Active(); ok {
		d["Project"] = &p
		d["Pipeline"] = s.pipeline(&p)
	} else {
		d["Pipeline"] = s.pipeline(nil)
	}
	d["Recent"] = s.titled(s.runner.List(12))
	d["Here"] = r.URL.Path
	d["Now"] = time.Now().Unix()
	d["TehranNow"] = time.Now().In(tehranZone).Format("15:04:05")
	if _, ok := d["Crumb"]; !ok {
		d["Crumb"] = ""
	}
	cs, ok := d["Crumbs"].([]crumb)
	if !ok {
		cs = s.crumbs(rel, d)
		d["Crumbs"] = cs
	}
	if _, ok := d["BackLink"]; !ok {
		d["BackLink"] = backOf(cs)
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

// foundWP reports whether a WordPress check run found at least one install.
func foundWP(run engine.Run) bool {
	for _, st := range run.Steps {
		if st.ID == "detect" {
			n := st.Outputs["count"]
			return n != "" && n != "0"
		}
	}
	return false
}
