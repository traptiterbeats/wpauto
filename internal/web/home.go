package web

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/traptiterbeats/wpauto/internal/engine"
	"github.com/traptiterbeats/wpauto/internal/projects"
)

// projectRow is one project in the home list.
type projectRow struct {
	projects.Project
	Addr        siteAddr
	Done, Total int
	LastAt      time.Time
	LastTitle   string
	LastStatus  string
	Search      string // lower-case name and address, for the live filter
}

func (s *Server) projectRows() []projectRow {
	runs := s.runner.List(500)
	var out []projectRow
	for _, p := range s.projects.List() {
		row := projectRow{Project: p, Addr: projectAddr(p), LastAt: p.Created}
		st := s.pipeline(&p)
		row.Total = len(st)
		for _, x := range st {
			if x.State == "done" {
				row.Done++
			}
		}
		for _, run := range runs { // newest first
			if run.ProjectID == p.ID {
				row.LastAt, row.LastTitle, row.LastStatus = run.StartedAt, run.Title, run.Status
				break
			}
		}
		row.Search = strings.ToLower(p.Name + " " + p.Site())
		out = append(out, row)
	}
	return out
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	suffixes, _ := json.Marshal(multiSuffixes)
	s.render(w, "home.html", s.page(r, map[string]any{
		"IsHome": true, "Rows": s.projectRows(),
		"Suffixes": template.JS(suffixes), "Crumbs": []crumb{{"پروژه‌ها", ""}},
	}))
}

// handleTasks is the earlier home page (every task and the project ribbons), kept as a tool.
func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	s.handleDashboard(w, r)
}

func (s *Server) handleTools(w http.ResponseWriter, r *http.Request) {
	s.render(w, "tools.html", s.page(r, map[string]any{}))
}

// cmsSection is one card on a project's second step.
type cmsSection struct {
	Key, Title, Note string
	Soon             bool
}

var cmsSections = []cmsSection{
	{"wordpress", "وردپرس", "نصب، شخصی‌سازی، قالب و دمو، AutoPilot", false},
	{"joomla", "جوملا", "", true},
	{"static", "بدون CMS", "سایت ساده‌ی HTML", true},
	{"laravel", "لاراول", "", true},
	{"shop", "فروشگاه‌ساز", "", true},
}

func (s *Server) projectOr404(w http.ResponseWriter, r *http.Request) (projects.Project, bool) {
	p, ok := s.projects.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
	}
	return p, ok
}

func (s *Server) handleProjectView(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectOr404(w, r)
	if !ok {
		return
	}
	bp := s.cfg.BasePath
	h, _ := s.store.Get(p.HostID)
	s.render(w, "project.html", s.page(r, map[string]any{
		"P": p, "Addr": projectAddr(p), "PHost": h, "CMS": cmsSections,
		"Crumbs": []crumb{{"پروژه‌ها", bp}, {p.Name, ""}},
	}))
}

// wpTask is one card on a project's WordPress page.
type wpTask struct {
	Key, Title, Note, Link string
	Stage                  *pipeStep // the path stage the task completes (nil for tools)
	Soon                   bool
	NeedsHost              bool // the task works on the project's host and none is linked yet
}

// wpGroup is a titled group of cards on a project's WordPress page.
type wpGroup struct {
	Key, Title string
	Tasks      []wpTask
	NeedsHost  bool // some card of the group waits for the project's host
}

// wpCtx is what a card needs to build its link.
type wpCtx struct {
	bp string
	p  projects.Project
	by map[string]*pipeStep
}

// wpCard is one entry of the WordPress page's card registry. To add a task, add a line here
// (Soon: true until it works); Stage ties it to a path stage, Host says it runs on the host.
type wpCard struct {
	Group, Key, Title, Note string
	Stage                   string
	Host                    bool
	Soon                    bool
	Link                    func(c wpCtx) string
}

// wpGroups are the groups of the WordPress page, in order.
var wpGroups = []struct{ Key, Title string }{
	{"setup", "راه‌اندازی"},
	{"site", "سایت وردپرسی"},
}

func stageLink(key string) func(wpCtx) string {
	return func(c wpCtx) string { return c.by[key].Link }
}

var wpCards = []wpCard{
	{Group: "setup", Key: "host", Title: "بررسی هاست", Note: "اطلاعات cPanel پروژه، PHP، دیتابیس و فضای دیسک", Stage: "host", Link: stageLink("host")},
	{Group: "setup", Key: "wpcheck", Title: "بررسی وردپرس دامنه", Note: "آیا روی این آدرس وردپرس نصب است؟", Stage: "wpcheck", Host: true, Link: stageLink("wpcheck")},
	{Group: "setup", Key: "wpinstall", Title: "نصب وردپرس", Note: "روی دامنه، ساب‌دامین یا پوشه‌ی پروژه", Stage: "wpinstall", Host: true, Link: stageLink("wpinstall")},
	{Group: "setup", Key: "autopilot", Title: "AutoPilot", Note: "همه‌ی مرحله‌ها خودکار و پشت سر هم", Host: true,
		Link: func(c wpCtx) string { return c.bp + "autopilot?project=" + url.QueryEscape(c.p.ID) }},
	{Group: "site", Key: "customize", Title: "شخصی‌سازی برای مشتری (AI)", Note: "متن‌ها و تصاویر دمو برای این مشتری", Stage: "customize", Link: stageLink("customize")},
	{Group: "site", Key: "theme", Title: "نصب قالب و دمو", Note: "قالب از کاتالوگ با دموی شخصی‌شده", Stage: "theme", Host: true, Link: stageLink("theme")},
	{Group: "site", Key: "wplist", Title: "وردپرس‌های نصب‌شده / حذف", Note: "وردپرس‌های روی هاست را ببینید و پاک کنید", Host: true,
		Link: func(c wpCtx) string {
			l := c.bp + "wordpress?project=" + url.QueryEscape(c.p.ID)
			if c.p.HostID != "" {
				l += "&host=" + url.QueryEscape(c.p.HostID)
			}
			return l
		}},
	{Group: "site", Key: "plugins", Title: "بررسی افزونه‌ها", Soon: true},
	{Group: "site", Key: "themes", Title: "بررسی قالب‌ها", Soon: true},
	{Group: "site", Key: "seo", Title: "بررسی سئو", Soon: true},
}

// wpTaskGroups builds the WordPress page's cards from the registry.
func wpTaskGroups(c wpCtx, hasHost bool) []wpGroup {
	var out []wpGroup
	for _, g := range wpGroups {
		grp := wpGroup{Key: g.Key, Title: g.Title}
		for _, card := range wpCards {
			if card.Group != g.Key {
				continue
			}
			t := wpTask{Key: card.Key, Title: card.Title, Note: card.Note, Soon: card.Soon}
			if !card.Soon {
				if card.Link != nil {
					t.Link = card.Link(c)
				}
				if card.Stage != "" {
					t.Stage = c.by[card.Stage]
				}
				t.NeedsHost = card.Host && !hasHost
				grp.NeedsHost = grp.NeedsHost || t.NeedsHost
			}
			grp.Tasks = append(grp.Tasks, t)
		}
		out = append(out, grp)
	}
	return out
}

func (s *Server) handleProjectWP(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectOr404(w, r)
	if !ok {
		return
	}
	bp := s.cfg.BasePath
	msg := s.autoLinkHost(&p)
	_, hasHost := s.store.Get(p.HostID)
	stages := s.pipeline(&p)
	by := map[string]*pipeStep{}
	for i := range stages {
		by[stages[i].Key] = &stages[i]
	}
	groups := wpTaskGroups(wpCtx{bp: bp, p: p, by: by}, hasHost)
	var last *engine.Run
	for _, run := range s.runner.List(500) {
		if run.ProjectID == p.ID {
			run := run
			last = &run
			break
		}
	}
	installed := by["wpinstall"].State == "done" || by["theme"].State == "done"
	data := map[string]any{
		"P": p, "Addr": projectAddr(p), "Groups": groups, "Stages": stages, "Last": last, "Installed": installed,
		"HasHost": hasHost, "HostLink": bp + "p/" + p.ID + "/host",
		"Crumbs": []crumb{{"پروژه‌ها", bp}, {p.Name, bp + "p/" + p.ID}, {"وردپرس", ""}},
	}
	if msg != "" && r.URL.Query().Get("msg") == "" && r.URL.Query().Get("err") == "" {
		data["Flash"], data["FlashKind"] = msg, "msg"
	}
	if h, ok := s.store.Get(p.HostID); ok {
		data["HostLabel"] = hostLabel(h)
	}
	s.render(w, "project_wp.html", s.page(r, data))
}
