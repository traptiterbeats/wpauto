package web

import (
	"encoding/json"
	"github.com/traptiterbeats/wpauto/internal/projects"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/traptiterbeats/wpauto/internal/cpanel"
	"github.com/traptiterbeats/wpauto/internal/engine"
	"github.com/traptiterbeats/wpauto/internal/profile"
	"github.com/traptiterbeats/wpauto/internal/store"
	"github.com/traptiterbeats/wpauto/internal/tasks"
)

func (s *Server) workflows() []*engine.Workflow {
	list, err := engine.LoadWorkflows(s.workflowDir)
	if err != nil {
		list, _ = engine.LoadWorkflows("")
	}
	return list
}

// dashItem is one card on the dashboard: a workflow or a panel page, in site-building order.
type dashItem struct {
	Title, Description, Link string
	Soon                     bool
	order                    int
}

func dashItems(wfs []*engine.Workflow) []dashItem {
	var out []dashItem
	for _, wf := range wfs {
		out = append(out, dashItem{Title: wf.Title, Description: wf.Description, Link: "run/" + wf.ID, order: wf.Order})
	}
	for _, t := range tasks.Catalogue {
		out = append(out, dashItem{Title: t.Title, Description: t.Description, Link: t.Link, Soon: t.Link == "", order: t.Order})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].order < out[j].order })
	return out
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	s.render(w, "dashboard.html", s.page(r, map[string]any{
		"Items": dashItems(s.workflows()), "HostCount": len(s.store.List()), "Hosts": s.store.List(),
		"Cards": s.projectCards(),
	}))
}

func (s *Server) handleRunForm(w http.ResponseWriter, r *http.Request) {
	wf, ok := engine.FindWorkflow(s.workflows(), r.PathValue("wf"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	hosts := s.store.List()
	q := r.URL.Query()
	proj, hasProj := s.projects.Active()
	hostID := q.Get("host")
	if hostID == "" && hasProj {
		hostID = proj.HostID
	}
	if hostID == "" && len(hosts) > 0 {
		hostID = hosts[0].ID
	}
	var domains []string
	for _, h := range hosts {
		if h.ID == hostID && h.LastReport != nil {
			domains = h.LastReport.Domains
		}
	}
	pre := map[string]string{"theme": q.Get("theme"), "draft": q.Get("draft"), "domain": "", "folder": ""}
	if len(domains) > 0 {
		pre["domain"] = domains[0]
	}
	if hasProj {
		if proj.Domain != "" && wf.ID != "wp-install" {
			pre["domain"], pre["folder"] = proj.Domain, proj.Folder
			if wf.ID == "wp-check-domain" && proj.Folder != "" {
				pre["domain"] = proj.Site()
			}
		}
		if pre["draft"] == "" && proj.DraftID != "" {
			pre["draft"] = proj.DraftID
			if d, err := engine.Drafts().Get(proj.DraftID); err == nil && pre["theme"] == "" {
				pre["theme"] = d.Theme
			}
		}
	}
	s.render(w, "run_form.html", s.page(r, map[string]any{
		"Crumb": wf.Title, "Steps": planSteps(wf, nil), "WF": wf, "Hosts": hosts, "HostID": hostID, "Domains": domains, "Error": q.Get("err"),
		"Themes": engine.Catalog().List(), "Drafts": engine.Drafts().List(), "Q": pre,
	}))
}

func (s *Server) handleRunStart(w http.ResponseWriter, r *http.Request) {
	bp := s.cfg.BasePath
	wfID := r.PathValue("wf")
	wf, ok := engine.FindWorkflow(s.workflows(), wfID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	hostID := r.FormValue("host")
	fail := func(msg string) {
		http.Redirect(w, r, bp+"run/"+wfID+"?host="+url.QueryEscape(hostID)+"&err="+url.QueryEscape(msg), http.StatusSeeOther)
	}
	h, ok := s.store.Get(hostID)
	if !ok {
		fail("یک هاست انتخاب کنید")
		return
	}
	prof, ok := profile.Find(s.profiles(), h.ProfileID)
	if !ok {
		fail("پروفایل این هاست پیدا نشد")
		return
	}
	if h.Locked(prof.MaxLoginAttempts) {
		fail("ورود این هاست قبلاً رد شده؛ اول رمز را در صفحه‌ی هاست اصلاح کنید")
		return
	}
	secret, err := s.box.Open(h.SealedSecret)
	if err != nil {
		fail("رمز ذخیره‌شده قابل خواندن نیست؛ دوباره واردش کنید")
		return
	}
	inputs := map[string]string{}
	for _, in := range wf.Inputs {
		inputs[in.Name] = r.FormValue("in_" + in.Name)
		if in.Type == "checkbox" {
			if inputs[in.Name] != "" {
				inputs[in.Name] = "true"
			} else {
				inputs[in.Name] = "false"
			}
		}
	}
	t := &engine.Target{
		HostID: h.ID, HostName: h.Name, Profile: prof,
		Options: cpanel.Options{BaseURL: h.URL, Username: h.Username, Secret: secret,
			Auth: cpanel.AuthMethod(prof.Auth), InsecureTLS: prof.InsecureTLS},
		OnLoginRejected: func() { _ = s.store.Update(h.ID, func(x *store.Host) { x.FailedLogins++ }) },
		OnReport: func(rep *cpanel.Report) {
			_ = s.store.Update(h.ID, func(x *store.Host) { x.LastReport = rep; x.FailedLogins = 0 })
		},
		OnDomain: func(d string) {
			_ = s.store.Update(h.ID, func(x *store.Host) {
				if x.LastReport != nil && !slices.Contains(x.LastReport.Domains, d) {
					x.LastReport.Domains = append(x.LastReport.Domains, d)
				}
			})
		},
	}
	if p, ok := s.projects.Active(); ok {
		t.ProjectID = p.ID
		dom, folder := projectSite(wf.ID, inputs)
		draft := inputs["draft"]
		t.OnDone = func(status string) {
			if status != engine.StatusSuccess {
				return
			}
			_ = s.projects.Update(p.ID, func(x *projects.Project) {
				x.HostID = h.ID
				if dom != "" {
					x.Domain, x.Folder = strings.ToLower(dom), folder
				}
				if draft != "" {
					x.DraftID = draft
				}
			})
		}
		if p.HostID == "" {
			_ = s.projects.Update(p.ID, func(x *projects.Project) { x.HostID = h.ID })
		}
	}
	id, err := s.runner.Start(wf, t, inputs)
	if err != nil {
		fail(err.Error())
		return
	}
	http.Redirect(w, r, bp+"runs/"+id, http.StatusSeeOther)
}

func (s *Server) handleRunView(w http.ResponseWriter, r *http.Request) {
	run, ok := s.runner.Get(r.PathValue("id"), 0)
	if !ok {
		http.NotFound(w, r)
		return
	}
	data := map[string]any{"Run": run, "Crumb": run.Title}
	if p, ok := s.projects.Get(run.ProjectID); ok {
		data["RunProject"] = p
	}
	s.render(w, "run.html", s.page(r, data))
}

func (s *Server) handleRunJSON(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	run, ok := s.runner.Get(r.PathValue("id"), after)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": run.Status, "error": run.Error, "steps": run.Steps, "log": run.Log, "done": run.Done()})
}

// planSteps lists a workflow's steps with whether each is needed for the inputs.
func planSteps(wf *engine.Workflow, inputs map[string]string) []sideStep {
	plan := engine.StepPlan(wf, inputs)
	out := make([]sideStep, len(wf.Steps))
	for i, st := range wf.Steps {
		out[i] = sideStep{ID: st.ID, Title: st.Title, State: plan[i]}
	}
	return out
}

// handleRunPlan answers which steps a form's current inputs need (read-only).
func (s *Server) handleRunPlan(w http.ResponseWriter, r *http.Request) {
	wf, ok := engine.FindWorkflow(s.workflows(), r.PathValue("wf"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	inputs := map[string]string{}
	for _, in := range wf.Inputs {
		if in.Type == "password" {
			continue
		}
		if v, ok := r.URL.Query()["in_"+in.Name]; ok {
			inputs[in.Name] = v[0]
		} else if in.Type == "checkbox" {
			inputs[in.Name] = "false"
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"steps": planSteps(wf, inputs)})
}

func (s *Server) handleRunCancel(w http.ResponseWriter, r *http.Request) {
	s.runner.Cancel(r.PathValue("id"))
	http.Redirect(w, r, s.cfg.BasePath+"runs/"+r.PathValue("id"), http.StatusSeeOther)
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	s.render(w, "runs.html", s.page(r, map[string]any{"Runs": s.runner.List(100)}))
}
