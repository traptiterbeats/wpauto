package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/traptiterbeats/wpauto/internal/cpanel"
	"github.com/traptiterbeats/wpauto/internal/engine"
	"github.com/traptiterbeats/wpauto/internal/profile"
	"github.com/traptiterbeats/wpauto/internal/store"
)

// removeWord is what the user types to confirm a permanent delete.
const removeWord = "حذف"

// hostTarget builds a run target for a host (no project is attached).
func (s *Server) hostTarget(hostID string) (*engine.Target, error) {
	h, ok := s.store.Get(hostID)
	if !ok {
		return nil, errors.New("یک هاست انتخاب کنید")
	}
	prof, ok := profile.Find(s.profiles(), h.ProfileID)
	if !ok {
		return nil, errors.New("پروفایل این هاست پیدا نشد")
	}
	if h.Locked(prof.MaxLoginAttempts) {
		return nil, errors.New("ورود این هاست قبلاً رد شده؛ اول رمز را در صفحه‌ی هاست اصلاح کنید")
	}
	secret, err := s.box.Open(h.SealedSecret)
	if err != nil {
		return nil, errors.New("رمز ذخیره‌شده قابل خواندن نیست؛ دوباره واردش کنید")
	}
	return &engine.Target{
		HostID: h.ID, HostName: h.Name, Profile: prof,
		Options: cpanel.Options{BaseURL: h.URL, Username: h.Username, Secret: secret,
			Auth: cpanel.AuthMethod(prof.Auth), InsecureTLS: prof.InsecureTLS},
		OnLoginRejected: func() { _ = s.store.Update(h.ID, func(x *store.Host) { x.FailedLogins++ }) },
		OnDisk:          s.diskSaver(h.ID),
	}, nil
}

// diskSaver stores the disk usage a run read on the host.
func (s *Server) diskSaver(hostID string) func(used, limit float64) {
	return func(used, limit float64) {
		_ = s.store.Update(hostID, func(x *store.Host) { x.SetDisk(used, limit) })
	}
}

// lastScan returns the newest successful wp-scan run of a host and what it found.
func (s *Server) lastScan(hostID string) (*engine.Run, []engine.FoundWP) {
	for _, run := range s.runner.List(0) {
		if run.WorkflowID != "wp-scan" || run.HostID != hostID || run.Status != engine.StatusSuccess {
			continue
		}
		for _, st := range run.Steps {
			if st.ID == "detect" {
				var list []engine.FoundWP
				_ = json.Unmarshal([]byte(st.Outputs["list"]), &list)
				r := run
				return &r, list
			}
		}
	}
	return nil, nil
}

func (s *Server) handleWordPress(w http.ResponseWriter, r *http.Request) {
	hosts := s.store.List()
	hostID := r.URL.Query().Get("host")
	if hostID == "" {
		if p, ok := s.projects.Active(); ok && p.HostID != "" {
			hostID = p.HostID
		} else if len(hosts) > 0 {
			hostID = hosts[0].ID
		}
	}
	run, list := s.lastScan(hostID)
	s.render(w, "wordpress.html", s.page(r, map[string]any{
		"Crumb": "وردپرس‌های هاست", "Hosts": hosts, "HostID": hostID, "Scan": run, "Sites": list, "RemoveWord": removeWord,
		"NeedHost": s.needHostPtr(r),
	}))
}

func (s *Server) handleWordPressOp(w http.ResponseWriter, r *http.Request) {
	bp := s.cfg.BasePath
	hostID := r.FormValue("host")
	back := bp + "wordpress?host=" + url.QueryEscape(hostID)
	fail := func(msg string) { http.Redirect(w, r, back+"&err="+url.QueryEscape(msg), http.StatusSeeOther) }
	t, err := s.hostTarget(hostID)
	if err != nil {
		fail(err.Error())
		return
	}
	var wfID string
	inputs := map[string]string{}
	switch r.PathValue("op") {
	case "scan":
		wfID = "wp-scan"
	case "remove":
		wfID = "wp-remove"
		_, list := s.lastScan(hostID)
		known := map[string]bool{}
		for _, f := range list {
			known[f.Dir] = true
		}
		var dirs []string
		for _, d := range r.Form["site"] {
			if known[d] { // only installs the last scan found on this host
				dirs = append(dirs, d)
			}
		}
		if len(dirs) == 0 {
			fail("هیچ وردپرسی انتخاب نشده")
			return
		}
		if strings.TrimSpace(r.FormValue("confirm")) != removeWord {
			fail("برای حذف، کلمه‌ی «" + removeWord + "» را در کادر تأیید بنویسید")
			return
		}
		inputs["sites"] = strings.Join(dirs, "\n")
	default:
		http.NotFound(w, r)
		return
	}
	wf, ok := engine.FindWorkflow(s.workflows(), wfID)
	if !ok {
		fail("کار " + wfID + " پیدا نشد")
		return
	}
	id, err := s.runner.Start(wf, t, inputs)
	if err != nil {
		fail(err.Error())
		return
	}
	http.Redirect(w, r, bp+"runs/"+id, http.StatusSeeOther)
}
