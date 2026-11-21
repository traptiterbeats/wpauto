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

// hostOption is a saved host in the new-project form, with the domains the page's script
// matches the typed address against.
type hostOption struct {
	ID, Name, Domains string
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	var hosts []hostOption
	for _, h := range s.store.List() {
		hosts = append(hosts, hostOption{ID: h.ID, Name: h.Name, Domains: strings.Join(hostDomains(h), " ")})
	}
	suffixes, _ := json.Marshal(multiSuffixes)
	s.render(w, "home.html", s.page(r, map[string]any{
		"IsHome": true, "Rows": s.projectRows(), "HostOptions": hosts,
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
	Title, Note, Link string
	Stage             *pipeStep // the path stage the task completes (nil for tools)
	Soon              bool
}

func (s *Server) handleProjectWP(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectOr404(w, r)
	if !ok {
		return
	}
	bp := s.cfg.BasePath
	stages := s.pipeline(&p)
	by := map[string]*pipeStep{}
	for i := range stages {
		by[stages[i].Key] = &stages[i]
	}
	q := "?project=" + url.QueryEscape(p.ID)
	wpList := bp + "wordpress"
	if p.HostID != "" {
		wpList += "?host=" + url.QueryEscape(p.HostID)
	}
	tasks := []wpTask{
		{Title: "بررسی هاست", Note: "ورود به cPanel، PHP، دیتابیس و فضای دیسک", Link: by["host"].Link, Stage: by["host"]},
		{Title: "بررسی وردپرس دامنه", Note: "آیا روی این آدرس وردپرس نصب است؟", Link: by["wpcheck"].Link, Stage: by["wpcheck"]},
		{Title: "نصب وردپرس", Note: "روی دامنه، ساب‌دامین یا پوشه‌ی پروژه", Link: by["wpinstall"].Link, Stage: by["wpinstall"]},
		{Title: "شخصی‌سازی برای مشتری (AI)", Note: "متن‌ها و تصاویر دمو برای این مشتری", Link: by["customize"].Link, Stage: by["customize"]},
		{Title: "نصب قالب و دمو", Note: "قالب از کاتالوگ با دموی شخصی‌شده", Link: by["theme"].Link, Stage: by["theme"]},
		{Title: "AutoPilot", Note: "همه‌ی مرحله‌ها خودکار و پشت سر هم", Link: bp + "autopilot" + q},
		{Title: "وردپرس‌های نصب‌شده / حذف", Note: "وردپرس‌های روی هاست را ببینید و پاک کنید", Link: wpList},
		{Title: "بررسی سئو", Soon: true},
	}
	var last *engine.Run
	for _, run := range s.runner.List(500) {
		if run.ProjectID == p.ID {
			run := run
			last = &run
			break
		}
	}
	installed := by["wpinstall"].State == "done" || by["theme"].State == "done"
	s.render(w, "project_wp.html", s.page(r, map[string]any{
		"P": p, "Addr": projectAddr(p), "Tasks": tasks, "Stages": stages, "Last": last, "Installed": installed,
		"Crumbs": []crumb{{"پروژه‌ها", bp}, {p.Name, bp + "p/" + p.ID}, {"وردپرس", ""}},
	}))
}
