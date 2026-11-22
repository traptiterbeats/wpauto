package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/traptiterbeats/wpauto/internal/engine"
	"github.com/traptiterbeats/wpauto/internal/wpplugins"
)

// The «افزونه‌های لازم» page of a project: a curated list of common plugins (internal/wpplugins)
// that are installed and activated on the project's own WordPress (its own host + its address).

const (
	wfPluginsInstall = "plugins-install"
	wfPluginsStatus  = "plugins-status"
)

func isPluginsRun(wfID string) bool { return wfID == wfPluginsInstall || wfID == wfPluginsStatus }

// pluginCard is one plugin on the page.
type pluginCard struct {
	wpplugins.Plugin
	State string // active | inactive | missing | "" (not checked yet)
}

// pluginsLink is the page of a project.
func pluginsLink(bp, projectID string) string { return bp + "p/" + projectID + "/wordpress/plugins" }

// pluginStates returns the plugin states from the newest successful plugins run of a project
// (a status check or an install), and that run.
func (s *Server) pluginStates(projectID string) (map[string]string, *engine.Run) {
	for _, run := range s.runner.List(500) {
		if run.ProjectID != projectID || !isPluginsRun(run.WorkflowID) || run.Status != engine.StatusSuccess {
			continue
		}
		full, ok := s.runner.Get(run.ID, 0)
		if !ok {
			continue
		}
		for _, st := range full.Steps {
			if raw := st.Outputs["status"]; raw != "" {
				m := map[string]string{}
				if json.Unmarshal([]byte(raw), &m) == nil {
					return m, &full
				}
			}
		}
	}
	return map[string]string{}, nil
}

// pluginsRun is the run shown on the page: the requested one when it is this project's plugins
// run, else the newest plugins run of the project.
func (s *Server) pluginsRun(projectID, want string) *engine.Run {
	if want != "" {
		if run, ok := s.runner.Get(want, 0); ok && run.ProjectID == projectID && isPluginsRun(run.WorkflowID) {
			run = s.titled([]engine.Run{run})[0]
			return &run
		}
	}
	for _, run := range s.runner.List(500) {
		if run.ProjectID == projectID && isPluginsRun(run.WorkflowID) {
			full, ok := s.runner.Get(run.ID, 0)
			if !ok {
				full = run
			}
			full = s.titled([]engine.Run{full})[0]
			return &full
		}
	}
	return nil
}

func (s *Server) handleProjectPlugins(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectOr404(w, r)
	if !ok {
		return
	}
	bp := s.cfg.BasePath
	sc := s.scopeFor(nil, p)
	sc.ShowAddr, sc.NeedAddr = p.Domain != "", p.Domain == ""
	sc.AddrLink = bp + "p/" + p.ID + "#address"
	states, checked := s.pluginStates(p.ID)
	var cards []pluginCard
	selectable := 0
	for _, pl := range wpplugins.List() {
		c := pluginCard{Plugin: pl, State: states[pl.Slug]}
		if c.State != wpplugins.StateActive {
			selectable++
		}
		cards = append(cards, c)
	}
	run := s.pluginsRun(p.ID, r.URL.Query().Get("run"))
	busy := run != nil && !run.Done()
	data := map[string]any{
		"P": p, "Scope": sc, "Plugins": cards, "Checked": checked, "Run": run, "Busy": busy, "Selectable": selectable,
		"CanRun": sc.HasHost && !sc.NeedAddr && !busy, "Error": r.URL.Query().Get("err"), "Msg": r.URL.Query().Get("msg"),
		"Action": pluginsLink(bp, p.ID),
		"Crumbs": []crumb{{"پروژه‌ها", bp}, {p.Name, bp + "p/" + p.ID}, {"وردپرس", bp + "p/" + p.ID + "/wordpress"}, {"افزونه‌های لازم", ""}},
	}
	if !sc.HasHost {
		data["NeedHost"] = &p
	}
	s.render(w, "project_plugins.html", s.page(r, data))
}

// handleProjectPluginsOp starts an install (op=install) or a status check (op=check) on the
// project's own host and address; posted host/domain/folder values are never read.
func (s *Server) handleProjectPluginsOp(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectOr404(w, r)
	if !ok {
		return
	}
	back := pluginsLink(s.cfg.BasePath, p.ID)
	fail := func(msg string) { http.Redirect(w, r, back+"?err="+url.QueryEscape(msg), http.StatusSeeOther) }
	var wfID, msg string
	inputs := map[string]string{}
	switch r.PathValue("op") {
	case "install":
		wfID = wfPluginsInstall
		list := wpplugins.List()
		picked := r.Form["sel"]
		if one := strings.TrimSpace(r.FormValue("plugin")); one != "" {
			picked = []string{one} // the «نصب» / «فعال‌سازی» button of one card
		}
		var slugs []string
		seen := map[string]bool{}
		for _, v := range picked {
			v = strings.ToLower(strings.TrimSpace(v))
			if _, ok := wpplugins.Find(list, v); ok && !seen[v] {
				seen[v] = true
				slugs = append(slugs, v)
			}
		}
		if len(slugs) == 0 {
			fail("هیچ افزونه‌ای از فهرست انتخاب نشده")
			return
		}
		inputs["plugins"] = strings.Join(slugs, ",")
		msg = "نصب و فعال‌سازی شروع شد."
	case "check":
		wfID = wfPluginsStatus
		msg = "بررسی وضعیت افزونه‌ها شروع شد (فقط خواندنی)."
	default:
		http.NotFound(w, r)
		return
	}
	h, has := s.ownHost(p)
	if !has {
		fail("این پروژه هنوز هاست ندارد؛ اول در «بررسی هاست» پروژه اطلاعات هاست را وارد کنید")
		return
	}
	if p.Domain == "" {
		fail("آدرس سایت این پروژه هنوز ثبت نشده؛ اول در صفحه‌ی پروژه آدرس را وارد کنید")
		return
	}
	if run := s.pluginsRun(p.ID, ""); run != nil && !run.Done() {
		fail("یک کار افزونه برای این پروژه در حال اجراست؛ صبر کنید تا تمام شود")
		return
	}
	wf, ok := engine.FindWorkflow(s.workflows(), wfID)
	if !ok {
		fail("کار " + wfID + " پیدا نشد")
		return
	}
	s.scopeFor(wf, p).applyLocks(inputs) // domain/folder from the project's address
	_ = s.projects.SetActive(p.ID)
	id, err := s.startWorkflow(wf, h.ID, p.ID, inputs, nil)
	if err != nil {
		fail(err.Error())
		return
	}
	http.Redirect(w, r, back+"?run="+url.QueryEscape(id)+"&msg="+url.QueryEscape(msg), http.StatusSeeOther)
}
