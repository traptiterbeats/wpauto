package web

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/traptiterbeats/wpauto/internal/cpanel"
	"github.com/traptiterbeats/wpauto/internal/engine"
	"github.com/traptiterbeats/wpauto/internal/profile"
	"github.com/traptiterbeats/wpauto/internal/projects"
	"github.com/traptiterbeats/wpauto/internal/store"
)

// A project's host is the cPanel account of its main domain. Hosts stay records in hosts.json
// (one per cPanel account, shared by every project on the same main domain); the user only
// meets them inside a project, in the «بررسی هاست» task.

// hostMain is the main domain a saved host stands for: the one cPanel reported, else the
// address of its cPanel (without a cpanel./www. prefix), else its name.
func hostMain(h store.Host) string {
	if h.LastReport != nil && h.LastReport.MainDomain != "" {
		return strings.ToLower(h.LastReport.MainDomain)
	}
	if u, err := url.Parse(h.URL); err == nil && u.Hostname() != "" {
		hn := strings.ToLower(u.Hostname())
		hn = strings.TrimPrefix(strings.TrimPrefix(hn, "www."), "cpanel.")
		if strings.Contains(hn, ".") && strings.Trim(hn, "0123456789.") != "" { // not an IP
			return registrable(hn)
		}
	}
	return strings.ToLower(strings.TrimSpace(h.Name))
}

// hostLabel is how a host is named to the user (its main domain).
func hostLabel(h store.Host) string {
	if m := hostMain(h); m != "" {
		return m
	}
	return h.Name
}

// hostForSite returns the one saved host that serves the address: a host whose reported
// main/addon domains fit it, or whose main domain is the address's main domain. Two or more
// candidates means no automatic choice.
func hostForSite(hosts []store.Host, a siteAddr) (store.Host, bool) {
	if a.Kind == "" {
		return store.Host{}, false
	}
	var found []store.Host
	for _, h := range hosts {
		if hostFits(h, a) || (hostMain(h) != "" && hostMain(h) == a.Parent) {
			found = append(found, h)
		}
	}
	if len(found) == 1 {
		return found[0], true
	}
	return store.Host{}, false
}

// autoLinkHost links a project that has no host to the saved host of its main domain. It
// returns the message to show, or "" when nothing was linked.
func (s *Server) autoLinkHost(p *projects.Project) string {
	if p.HostID != "" {
		return ""
	}
	h, ok := hostForSite(s.store.List(), projectAddr(*p))
	if !ok {
		return ""
	}
	if err := s.projects.Update(p.ID, func(x *projects.Project) { x.HostID = h.ID }); err != nil {
		return ""
	}
	p.HostID = h.ID
	for _, o := range s.projects.List() {
		if o.ID != p.ID && o.HostID == h.ID {
			return "هاست " + hostLabel(h) + " از پروژه‌ی دیگر استفاده شد"
		}
	}
	return "هاست ذخیره‌شده‌ی " + hostLabel(h) + " به این پروژه وصل شد"
}

// projectHostURL is the default cPanel address for a project: https://<main domain>:2083.
func projectHostURL(p projects.Project) string {
	a := projectAddr(p)
	if a.Parent == "" {
		return ""
	}
	return "https://" + a.Parent + ":2083"
}

// needHost is the project a task page was opened for (?project=) when that project has no
// host yet; the page then shows a link to the project's «بررسی هاست» instead of its form.
func (s *Server) needHost(r *http.Request) (projects.Project, bool) {
	id := r.URL.Query().Get("project")
	if id == "" {
		return projects.Project{}, false
	}
	p, ok := s.projects.Get(id)
	if !ok {
		return projects.Project{}, false
	}
	if _, linked := s.store.Get(p.HostID); linked {
		return projects.Project{}, false
	}
	p.HostID = ""
	if s.autoLinkHost(&p) != "" {
		return projects.Project{}, false
	}
	return p, true
}

// lastHostCheck returns the run to show on a project's host page: the requested one when it is
// this project's host check, else the newest host check of the project.
func (s *Server) lastHostCheck(projectID, want string) *engine.Run {
	if want != "" {
		if run, ok := s.runner.Get(want, 0); ok && run.ProjectID == projectID && run.WorkflowID == "host-check" {
			return &run
		}
	}
	for _, run := range s.runner.List(500) {
		if run.ProjectID == projectID && run.WorkflowID == "host-check" {
			full, ok := s.runner.Get(run.ID, 0)
			if !ok {
				full = run
			}
			return &full
		}
	}
	return nil
}

func (s *Server) handleProjectHost(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectOr404(w, r)
	if !ok {
		return
	}
	bp := s.cfg.BasePath
	msg := r.URL.Query().Get("msg")
	if p.HostID != "" {
		if _, ok := s.store.Get(p.HostID); !ok {
			p.HostID = "" // the host was removed from the panel
		}
	}
	if m := s.autoLinkHost(&p); m != "" && msg == "" {
		msg = m
	}
	data := map[string]any{
		"P": p, "Addr": projectAddr(p), "Profiles": s.profiles(), "DefaultURL": projectHostURL(p),
		"Msg": msg, "Error": r.URL.Query().Get("err"),
		"Crumbs": []crumb{{"پروژه‌ها", bp}, {p.Name, bp + "p/" + p.ID}, {"وردپرس", bp + "p/" + p.ID + "/wordpress"}, {"بررسی هاست", ""}},
	}
	if h, ok := s.store.Get(p.HostID); ok {
		prof, _ := profile.Find(s.profiles(), h.ProfileID)
		data["Host"] = h
		data["HostLabel"] = hostLabel(h)
		data["HostProfile"] = prof
		data["Locked"] = prof != nil && h.Locked(prof.MaxLoginAttempts)
		var shared []string
		for _, o := range s.projects.List() {
			if o.ID != p.ID && o.HostID == h.ID {
				shared = append(shared, o.Name)
			}
		}
		data["SharedWith"] = shared
		data["CheckRun"] = s.lastHostCheck(p.ID, r.URL.Query().Get("run"))
	}
	s.render(w, "project_host.html", s.page(r, data))
}

// handleProjectHostSave saves the connection typed on the project's host page (a new host, or
// the linked one edited), links it to the project and starts the host check right away.
func (s *Server) handleProjectHostSave(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectOr404(w, r)
	if !ok {
		return
	}
	back := s.cfg.BasePath + "p/" + p.ID + "/host"
	fail := func(msg string) { http.Redirect(w, r, back+"?err="+url.QueryEscape(msg), http.StatusSeeOther) }
	prof, ok := profile.Find(s.profiles(), r.FormValue("profile"))
	if !ok {
		prof, ok = profile.Find(s.profiles(), "generic")
		if !ok {
			fail("پروفایل هاست پیدا نشد")
			return
		}
	}
	addr := strings.TrimSpace(r.FormValue("url"))
	user := strings.TrimSpace(r.FormValue("username"))
	secret := r.FormValue("secret")
	if addr == "" || user == "" {
		fail("آدرس cPanel و نام کاربری لازم است")
		return
	}
	base, err := cpanel.NormalizeURL(addr, prof.Port)
	if err != nil {
		fail("آدرس cPanel نامعتبر است")
		return
	}
	sealed := ""
	if secret != "" {
		if sealed, err = s.box.Seal(secret); err != nil {
			http.Error(w, "cannot encrypt the password", 500)
			return
		}
	}
	hostID := ""
	if cur, linked := s.store.Get(p.HostID); linked {
		hostID = cur.ID
	} else {
		// the same cPanel account saved before (another project): reuse it
		for _, h := range s.store.List() {
			if h.URL == base && strings.EqualFold(h.Username, user) {
				hostID = h.ID
			}
		}
	}
	if hostID != "" {
		err = s.store.Update(hostID, func(h *store.Host) {
			if h.URL != base || !strings.EqualFold(h.Username, user) {
				h.LastReport, h.Disk = nil, nil // another account: the old report is not about it
			}
			h.URL, h.Username, h.ProfileID = base, user, prof.ID
			if sealed != "" {
				h.SealedSecret = sealed
			}
			h.FailedLogins = 0
		})
		if err != nil {
			fail(err.Error())
			return
		}
	} else {
		if sealed == "" {
			fail("رمز عبور یا API Token لازم است")
			return
		}
		name := projectAddr(p).Parent
		if name == "" {
			name = strings.TrimPrefix(strings.SplitN(base, "://", 2)[1], "www.")
		}
		h, err := s.store.Add(store.Host{Name: name, ProfileID: prof.ID, URL: base, Username: user, SealedSecret: sealed})
		if err != nil {
			fail(err.Error())
			return
		}
		hostID = h.ID
	}
	if err := s.projects.Update(p.ID, func(x *projects.Project) { x.HostID = hostID }); err != nil {
		fail(err.Error())
		return
	}
	s.startHostCheck(w, r, p.ID, hostID, "اطلاعات اتصال ذخیره شد و بررسی هاست شروع شد.")
}

// handleProjectHostCheck runs the host check again on the project's host.
func (s *Server) handleProjectHostCheck(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectOr404(w, r)
	if !ok {
		return
	}
	if _, linked := s.store.Get(p.HostID); !linked {
		http.Redirect(w, r, s.cfg.BasePath+"p/"+p.ID+"/host?err="+url.QueryEscape("اول اطلاعات اتصال هاست را وارد کنید"), http.StatusSeeOther)
		return
	}
	s.startHostCheck(w, r, p.ID, p.HostID, "بررسی دوباره‌ی هاست شروع شد.")
}

func (s *Server) startHostCheck(w http.ResponseWriter, r *http.Request, projectID, hostID, msg string) {
	back := s.cfg.BasePath + "p/" + projectID + "/host"
	wf, ok := engine.FindWorkflow(s.workflows(), "host-check")
	if !ok {
		http.Redirect(w, r, back+"?err="+url.QueryEscape("کار بررسی هاست پیدا نشد"), http.StatusSeeOther)
		return
	}
	_ = s.projects.SetActive(projectID)
	id, err := s.startWorkflow(wf, hostID, projectID, map[string]string{}, nil)
	if err != nil {
		http.Redirect(w, r, back+"?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, back+"?run="+url.QueryEscape(id)+"&msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

// needHostPtr is needHost for templates: nil when the page can show its form.
func (s *Server) needHostPtr(r *http.Request) *projects.Project {
	if p, ok := s.needHost(r); ok {
		return &p
	}
	return nil
}
