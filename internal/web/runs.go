package web

import (
	"encoding/json"
	"errors"
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
		if wf.Hidden {
			continue
		}
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
	q := r.URL.Query()
	proj, hasProj := s.taskProject(r)
	var scope *taskScope
	var hosts []store.Host
	hostID := q.Get("host")
	if hasProj {
		// inside a project: its own host and its address, read-only
		scope = s.scopeFor(wf, proj)
		hostID = ""
		if scope.HasHost {
			hosts, hostID = []store.Host{scope.Host}, scope.Host.ID
		}
	} else {
		hosts = s.store.List()
		if hostID == "" && len(hosts) > 0 {
			hostID = hosts[0].ID
		}
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
	var needHost *projects.Project
	if hasProj {
		if !scope.HasHost {
			needHost = &proj
		}
		for k, v := range scope.Values {
			pre[k] = v
		}
		if wf.ID == "wp-install" {
			pre["title"] = proj.Name
		}
		if pre["draft"] == "" && proj.DraftID != "" {
			pre["draft"] = proj.DraftID
			if d, err := engine.Drafts().Get(proj.DraftID); err == nil && pre["theme"] == "" {
				pre["theme"] = d.Theme
			}
		}
	}
	var planIn map[string]string
	if scope != nil && len(scope.Values) > 0 {
		planIn = scope.Values
	}
	s.render(w, "run_form.html", s.page(r, map[string]any{
		"Crumb": wf.Title, "Steps": planSteps(wf, planIn), "WF": wf, "Hosts": hosts, "HostID": hostID, "Domains": domains, "Error": q.Get("err"),
		"Themes": engine.Catalog().List(), "Drafts": engine.Drafts().List(), "Q": pre, "NeedHost": needHost, "Scope": scope,
		"Pick": s.themePicker(themeInput(wf), pre["theme"], engine.Catalog().List(), nil),
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
	back := bp + "run/" + wfID + "?host=" + url.QueryEscape(hostID)
	projectID := ""
	if p, ok := s.taskProject(r); ok {
		// a project's run always uses the project's own host and address, whatever was posted
		projectID = p.ID
		back = bp + "run/" + wfID + "?project=" + url.QueryEscape(p.ID)
		sc := s.scopeFor(wf, p)
		if !sc.HasHost {
			http.Redirect(w, r, back+"&err="+url.QueryEscape("این پروژه هنوز هاست ندارد؛ اول در «بررسی هاست» پروژه اطلاعات هاست را وارد کنید"), http.StatusSeeOther)
			return
		}
		if sc.NeedAddr {
			http.Redirect(w, r, back+"&err="+url.QueryEscape("آدرس سایت این پروژه هنوز ثبت نشده؛ اول در صفحه‌ی پروژه آدرس را وارد کنید"), http.StatusSeeOther)
			return
		}
		hostID = sc.Host.ID
		sc.applyLocks(inputs)
		_ = s.projects.SetActive(p.ID)
	}
	id, err := s.startWorkflow(wf, hostID, projectID, inputs, nil)
	if err != nil {
		http.Redirect(w, r, back+"&err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, bp+"runs/"+id, http.StatusSeeOther)
}

// startWorkflow starts wf on a saved host, tied to a project (may be ""), and returns the run id.
// On success the project remembers the host, the site and the draft the run used. after (optional)
// is called last, once the run and the project are completely saved.
func (s *Server) startWorkflow(wf *engine.Workflow, hostID, projectID string, inputs map[string]string, after func(status string)) (string, error) {
	if projectID != "" {
		// a project always runs on its own host record
		id, err := s.ensureOwnHost(projectID, hostID)
		if err != nil {
			return "", err
		}
		hostID = id
	}
	h, ok := s.store.Get(hostID)
	if !ok {
		return "", errors.New("یک هاست انتخاب کنید")
	}
	prof, ok := profile.Find(s.profiles(), h.ProfileID)
	if !ok {
		return "", errors.New("پروفایل این هاست پیدا نشد")
	}
	if h.Locked(prof.MaxLoginAttempts) {
		return "", errors.New("ورود این هاست قبلاً رد شده؛ اول رمز را در صفحه‌ی هاست اصلاح کنید")
	}
	secret, err := s.box.Open(h.SealedSecret)
	if err != nil {
		return "", errors.New("رمز ذخیره‌شده قابل خواندن نیست؛ دوباره واردش کنید")
	}
	t := &engine.Target{
		HostID: h.ID, HostName: h.Name, Profile: prof,
		Options: cpanel.Options{BaseURL: h.URL, Username: h.Username, Secret: secret,
			Auth: cpanel.AuthMethod(prof.Auth), InsecureTLS: prof.InsecureTLS},
		OnLoginRejected: func() { _ = s.store.Update(h.ID, func(x *store.Host) { x.FailedLogins++ }) },
		OnReport: func(rep *cpanel.Report) {
			_ = s.store.Update(h.ID, func(x *store.Host) { x.LastReport = rep; x.FailedLogins = 0 })
		},
		OnDisk: s.diskSaver(h.ID),
		OnDomain: func(d string) {
			_ = s.store.Update(h.ID, func(x *store.Host) {
				if x.LastReport != nil && !slices.Contains(x.LastReport.Domains, d) {
					x.LastReport.Domains = append(x.LastReport.Domains, d)
				}
			})
		},
	}
	if p, ok := s.projects.Get(projectID); ok && projectID != "" {
		t.ProjectID = p.ID
		dom, folder := projectSite(wf.ID, inputs)
		mode := ""
		if wf.ID == "wp-install" {
			mode = inputs["mode"]
			if mode != kindSubfolder && inputs["name"] == "" {
				mode = kindRoot
			}
		}
		draft := inputs["draft"]
		t.OnDone = func(status string) {
			if status == engine.StatusSuccess {
				_ = s.projects.Update(p.ID, func(x *projects.Project) {
					x.HostID = h.ID
					if dom != "" {
						x.Domain, x.Folder = strings.ToLower(dom), folder
						if mode != "" {
							x.Mode = mode
						} else if x.Mode != "" && (folder != "") != (x.Mode == kindSubfolder) {
							x.Mode = "" // worked out again from domain/folder
						}
					}
					if draft != "" {
						x.DraftID = draft
					}
				})
			}
			if after != nil {
				after(status)
			}
		}
		if p.HostID == "" {
			_ = s.projects.Update(p.ID, func(x *projects.Project) { x.HostID = h.ID })
		}
	}
	if t.OnDone == nil && after != nil {
		t.OnDone = after
	}
	return s.runner.Start(wf, t, inputs)
}

func (s *Server) handleRunView(w http.ResponseWriter, r *http.Request) {
	run, ok := s.runner.Get(r.PathValue("id"), 0)
	if !ok {
		http.NotFound(w, r)
		return
	}
	run = s.titled([]engine.Run{run})[0]
	data := map[string]any{"Run": run, "Crumb": run.Title}
	if run.WorkflowID == "wp-scan" || run.WorkflowID == "wp-remove" {
		data["BackLink"] = s.cfg.BasePath + "wordpress?host=" + url.QueryEscape(run.HostID)
	}
	p, hasProject := s.projects.Get(run.ProjectID)
	if hasProject {
		data["RunProject"] = p
	}
	if run.Status == engine.StatusSuccess && (run.WorkflowID == "wp-install" || run.WorkflowID == "theme-install") {
		site := p.URL()
		if dom, folder := projectSite(run.WorkflowID, run.Inputs); dom != "" {
			site = projects.Project{Domain: strings.ToLower(dom), Folder: folder}.URL()
		}
		if site != "" {
			data["SiteURL"] = site
			data["Bust"] = run.EndedAt.Unix()
		}
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
	if p, ok := s.taskProject(r); ok {
		s.scopeFor(wf, p).applyLocks(inputs)
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
	s.render(w, "runs.html", s.page(r, map[string]any{"Runs": s.titled(s.runner.List(100))}))
}
