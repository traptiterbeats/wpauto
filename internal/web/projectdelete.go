package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/traptiterbeats/wpauto/internal/engine"
	"github.com/traptiterbeats/wpauto/internal/projects"
)

// Deleting a project (home row, project page, or the no-JS confirm page p/{id}/delete) always
// removes the project, its own host record, its runs and its unshared draft. With «wipe_site»
// the project's WordPress is first removed from the host by a normal wp-remove run (site mode:
// only that address; its subdomain only if wpauto created it). The run is DETACHED (no
// project_id; inputs.project names the project), so purging the project's runs never deletes
// it: the run page stays reachable from the home flash («گزارش کامل حذف») and the tools'
// recent runs. Only when the run succeeds is the project deleted (finishWipe, from the run's
// OnDone and again, idempotently, when the run page finishes); on failure the project stays.

// wipeMu serialises finishWipe so the OnDone callback and the run page never purge twice.
var wipeMu sync.Mutex

// delView is what the delete dialog / confirm page shows for one project.
type delView struct {
	BasePath, CSRF string
	P              projects.Project
	CanWipe        bool
	WipeWhy        string // why the host checkbox is disabled
	ErrBack        string // where a refused delete returns
	CancelLink     string
}

func (s *Server) delViewFor(r *http.Request, p projects.Project, errBack, cancel string) delView {
	why := s.wipeBlock(p)
	return delView{BasePath: s.cfg.BasePath, CSRF: s.csrf(r), P: p, CanWipe: why == "", WipeWhy: why,
		ErrBack: errBack, CancelLink: cancel}
}

// isWipeRun reports whether a run removes a project's site before the project is deleted.
func isWipeRun(run engine.Run) bool {
	return run.WorkflowID == "wp-remove" && run.Inputs["project"] != ""
}

// sharedWith returns another project with the same site address.
func (s *Server) sharedWith(p projects.Project) (projects.Project, bool) {
	if p.Domain == "" {
		return projects.Project{}, false
	}
	for _, o := range s.projects.List() {
		if o.ID != p.ID && o.Domain != "" && strings.EqualFold(o.Site(), p.Site()) {
			return o, true
		}
	}
	return projects.Project{}, false
}

// wipeBlock says why the project's site cannot be removed from the host ("" = it can).
func (s *Server) wipeBlock(p projects.Project) string {
	for _, run := range s.runner.List(0) {
		if isWipeRun(run) && run.Inputs["project"] == p.ID && !run.Done() {
			return "حذف سایت این پروژه از هاست همین حالا در حال اجراست."
		}
	}
	if p.Domain == "" {
		return "این پروژه هنوز آدرس سایت ندارد؛ چیزی روی هاست برای پاک کردن نیست."
	}
	if _, ok := s.ownHost(p); !ok {
		return "این پروژه هاست ندارد؛ wpauto به هاست دسترسی ندارد و سایت روی هاست دست نمی‌خورد."
	}
	if o, ok := s.sharedWith(p); ok {
		return "پروژه‌ی «" + o.Name + "» هم به همین سایت (" + p.Site() + ") اشاره می‌کند؛ برای آسیب نزدن به آن، سایت روی هاست پاک نمی‌شود. فقط پروژه را حذف کنید یا اول آن پروژه را پاک کنید."
	}
	installed := false
	for _, st := range s.pipeline(&p) {
		if st.Key == "wpinstall" && st.State == "done" {
			installed = true
		}
	}
	if !installed {
		return "هنوز وردپرسی برای این پروژه نصب نشده (یا بررسی وردپرس پیدایش نکرده)؛ چیزی روی هاست برای پاک کردن نیست."
	}
	return ""
}

// ownSubs lists the project's subdomain when a successful wp-install run created it.
func (s *Server) ownSubs(p projects.Project) string {
	if projectAddr(p).Kind != kindSubdomain {
		return ""
	}
	want := strings.ToLower(p.Domain)
	for _, run := range s.runner.List(0) {
		if run.WorkflowID != "wp-install" || run.Status != engine.StatusSuccess {
			continue
		}
		for _, st := range run.Steps {
			if st.ID == "subdomain" && st.Status == engine.StatusSuccess && strings.EqualFold(st.Outputs["host"], want) {
				return want
			}
		}
	}
	return ""
}

// startSiteWipe checks and starts the detached wp-remove run for a project's site.
func (s *Server) startSiteWipe(id string) (string, error) {
	p, ok := s.projects.Get(id)
	if !ok {
		return "", projects.ErrNotFound
	}
	if why := s.wipeBlock(p); why != "" {
		return "", errors.New(why)
	}
	h, _ := s.ownHost(p)
	wf, ok := engine.FindWorkflow(s.workflows(), "wp-remove")
	if !ok {
		return "", errors.New("کار wp-remove پیدا نشد")
	}
	t, err := s.hostTarget(h.ID)
	if err != nil {
		return "", err
	}
	pid := p.ID
	t.OnDone = func(status string) {
		if status == engine.StatusSuccess {
			s.finishWipe(pid)
		}
	}
	return s.runner.Start(wf, t, map[string]string{
		"site": p.Site(), "own_subs": s.ownSubs(p), "project": p.ID, "project_name": p.Name,
	})
}

// finishWipe deletes the project after its site was removed (idempotent).
func (s *Server) finishWipe(projectID string) {
	wipeMu.Lock()
	defer wipeMu.Unlock()
	if _, ok := s.projects.Get(projectID); ok {
		_, _ = s.deleteProject(projectID, true)
	}
}

// wipeSummary is the home flash after a project was deleted with its site.
func wipeSummary(run engine.Run) string {
	msg := fmt.Sprintf("پروژه‌ی «%s» با اجراها و پیش‌نویس اختصاصی‌اش حذف شد و وردپرس %s روی هاست (فایل‌ها و دیتابیس) پاک شد.",
		run.Inputs["project_name"], run.Inputs["site"])
	var out map[string]string
	for _, st := range run.Steps {
		if st.ID == "remove" {
			out = st.Outputs
		}
	}
	before, err1 := strconv.ParseFloat(out["disk_before"], 64)
	after, err2 := strconv.ParseFloat(out["disk_after"], 64)
	limit, _ := strconv.ParseFloat(out["disk_limit"], 64)
	if err1 == nil && err2 == nil {
		if freed := before - after; freed >= 0.5 {
			msg += fmt.Sprintf(" حدود %.0f MB از فضای هاست آزاد شد.", freed)
		} else {
			msg += " مقدار فضای آزادشده هنوز در گزارش هاست دیده نمی‌شود (cPanel سهمیه را گاهی با تأخیر به‌روز می‌کند)."
		}
		lim := "نامحدود"
		if limit > 0 {
			lim = fmt.Sprintf("%.0f MB", limit)
		}
		msg += fmt.Sprintf(" فضای هاست اکنون: %.0f MB از %s.", after, lim)
	}
	return msg
}

// handleProjectDeletePage is the confirm page (the dialog's no-JS fallback).
func (s *Server) handleProjectDeletePage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectOr404(w, r)
	if !ok {
		return
	}
	bp := s.cfg.BasePath
	s.render(w, "project_delete.html", s.page(r, map[string]any{
		"P":      p,
		"Del":    s.delViewFor(r, p, bp+"p/"+p.ID+"/delete", bp+"p/"+p.ID),
		"Crumbs": []crumb{{"پروژه‌ها", bp}, {p.Name, bp + "p/" + p.ID}, {"حذف پروژه", ""}},
	}))
}

// wipeRunView adds the project-delete banner to a wp-remove run page and finishes the delete
// when the page is reloaded at the end (?finish=1): success → home with the summary.
func (s *Server) wipeRunView(w http.ResponseWriter, r *http.Request, run engine.Run, data map[string]any) bool {
	bp := s.cfg.BasePath
	pid := run.Inputs["project"]
	if run.Status == engine.StatusSuccess && r.URL.Query().Get("finish") != "" {
		s.finishWipe(pid)
		http.Redirect(w, r, bp+"?msg="+url.QueryEscape(wipeSummary(run))+"&wiperun="+url.QueryEscape(run.ID), http.StatusSeeOther)
		return true
	}
	p, exists := s.projects.Get(pid)
	data["Wipe"] = map[string]any{"Name": run.Inputs["project_name"], "Site": run.Inputs["site"], "Exists": exists,
		"Summary": wipeSummary(run)}
	if exists {
		data["RunProject"] = p
		data["BackLink"] = bp + "p/" + p.ID
	} else {
		data["BackLink"] = bp
	}
	return false
}
