package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

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

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	s.render(w, "dashboard.html", s.page(r, map[string]any{
		"Workflows": s.workflows(), "Soon": tasks.Catalogue, "Runs": s.runner.List(5), "HostCount": len(s.store.List()),
	}))
}

func (s *Server) handleRunForm(w http.ResponseWriter, r *http.Request) {
	wf, ok := engine.FindWorkflow(s.workflows(), r.PathValue("wf"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	hosts := s.store.List()
	hostID := r.URL.Query().Get("host")
	if hostID == "" && len(hosts) > 0 {
		hostID = hosts[0].ID
	}
	var domains []string
	for _, h := range hosts {
		if h.ID == hostID && h.LastReport != nil {
			domains = h.LastReport.Domains
		}
	}
	s.render(w, "run_form.html", s.page(r, map[string]any{
		"WF": wf, "Hosts": hosts, "HostID": hostID, "Domains": domains, "Error": r.URL.Query().Get("err"),
		"Themes": engine.Catalog().List(),
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
	s.render(w, "run.html", s.page(r, map[string]any{"Run": run}))
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

func (s *Server) handleRunCancel(w http.ResponseWriter, r *http.Request) {
	s.runner.Cancel(r.PathValue("id"))
	http.Redirect(w, r, s.cfg.BasePath+"runs/"+r.PathValue("id"), http.StatusSeeOther)
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	s.render(w, "runs.html", s.page(r, map[string]any{"Runs": s.runner.List(100)}))
}
