package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/traptiterbeats/wpauto/internal/cpanel"
	"github.com/traptiterbeats/wpauto/internal/engine"
	"github.com/traptiterbeats/wpauto/internal/profile"
	"github.com/traptiterbeats/wpauto/internal/projects"
	"github.com/traptiterbeats/wpauto/internal/store"
)

// A project's host is the cPanel connection of that project. Hosts stay records in hosts.json,
// but each record belongs to one project (Host.ProjectID): two projects on the same cPanel
// account have two independent records, each entered and checked inside its project, and
// deleting a project deletes its record. The user meets hosts only in «بررسی هاست».

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

// ownHost returns the project's own host record, if it has one linked.
func (s *Server) ownHost(p projects.Project) (store.Host, bool) {
	h, ok := s.store.Get(p.HostID)
	if !ok || (h.ProjectID != "" && h.ProjectID != p.ID) {
		return store.Host{}, false
	}
	return h, true
}

// ensureOwnHost makes sure a project works on a host record of its own and returns its id: an
// unowned record only this project uses is claimed; a record of another project (or a shared
// legacy one) is copied for this project, keeping one copy per account. The project is
// pointed at the returned record.
func (s *Server) ensureOwnHost(projectID, hostID string) (string, error) {
	h, ok := s.store.Get(hostID)
	if !ok {
		return "", errors.New("یک هاست انتخاب کنید")
	}
	p, ok := s.projects.Get(projectID)
	if !ok || h.ProjectID == projectID {
		return hostID, nil
	}
	id := ""
	if h.ProjectID == "" && !s.hostUsedByOthers(hostID, projectID) {
		if err := s.store.Update(hostID, func(x *store.Host) { x.ProjectID = projectID }); err != nil {
			return "", err
		}
		id = hostID
	} else if cur, ok := s.store.Get(p.HostID); ok && cur.ProjectID == projectID && cur.URL == h.URL && strings.EqualFold(cur.Username, h.Username) {
		id = cur.ID // the project already has its own copy of this account
	} else {
		c, err := s.copyHost(h, projectID)
		if err != nil {
			return "", err
		}
		id = c.ID
	}
	if p.HostID != id {
		if err := s.projects.Update(projectID, func(x *projects.Project) { x.HostID = id }); err != nil {
			return "", err
		}
		// the project's previous own record is replaced, not left behind
		if old, ok := s.store.Get(p.HostID); ok && old.ProjectID == projectID && !s.hostUsedByOthers(old.ID, projectID) {
			_ = s.store.Delete(old.ID)
		}
	}
	return id, nil
}

// copyHost saves a copy of a host record that belongs to projectID.
func (s *Server) copyHost(h store.Host, projectID string) (store.Host, error) {
	c := h
	c.ProjectID, c.FailedLogins = projectID, 0
	if h.LastReport != nil {
		r := *h.LastReport
		c.LastReport = &r
	}
	if h.Disk != nil {
		d := *h.Disk
		c.Disk = &d
	}
	return s.store.Add(c)
}

func (s *Server) hostUsedByOthers(hostID, projectID string) bool {
	for _, o := range s.projects.List() {
		if o.ID != projectID && o.HostID == hostID {
			return true
		}
	}
	return false
}

// migrateHosts gives every project its own host record: a record shared by several projects
// stays with the first (oldest) one and the others get copies; an unowned record used by one
// project becomes that project's. Records no project uses are left alone. Safe to run on
// every start.
func (s *Server) migrateHosts() error {
	list := s.projects.List()
	for i := len(list) - 1; i >= 0; i-- { // oldest first
		p := list[i]
		if p.HostID == "" {
			continue
		}
		h, ok := s.store.Get(p.HostID)
		if !ok || h.ProjectID == p.ID {
			continue
		}
		if h.ProjectID == "" {
			claimed := false
			for j := len(list) - 1; j > i; j-- { // an older project uses it too: it was claimed there
				claimed = claimed || list[j].HostID == p.HostID
			}
			if !claimed {
				if err := s.store.Update(h.ID, func(x *store.Host) { x.ProjectID = p.ID }); err != nil {
					return err
				}
				continue
			}
		}
		c, err := s.copyHost(h, p.ID)
		if err != nil {
			return err
		}
		if err := s.projects.Update(p.ID, func(x *projects.Project) { x.HostID = c.ID }); err != nil {
			return err
		}
	}
	return nil
}

// projectHostURL is the default cPanel address for a project: https://<main domain>:2083.
func projectHostURL(p projects.Project) string {
	a := projectAddr(p)
	if a.Parent == "" {
		return ""
	}
	return "https://" + a.Parent + ":2083"
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
	data := map[string]any{
		"P": p, "Addr": projectAddr(p), "DefaultURL": projectHostURL(p),
		"Msg": msg, "Error": r.URL.Query().Get("err"),
		"Crumbs": []crumb{{"پروژه‌ها", bp}, {p.Name, bp + "p/" + p.ID}, {"وردپرس", bp + "p/" + p.ID + "/wordpress"}, {"بررسی هاست", ""}},
	}
	if h, ok := s.ownHost(p); ok {
		prof, _ := profile.Find(s.profiles(), h.ProfileID)
		data["Host"] = h
		data["HostLabel"] = hostLabel(h)
		data["HostProfile"] = prof
		data["Locked"] = prof != nil && h.Locked(prof.MaxLoginAttempts)
		data["CheckRun"] = s.lastHostCheck(p.ID, r.URL.Query().Get("run"))
	}
	s.render(w, "project_host.html", s.page(r, data))
}

// hostInput is a checked cPanel connection typed by the user (the secret already sealed).
type hostInput struct {
	URL, User, Sealed string
	Profile           string
}

// parseHostInput checks a typed connection: profile, cPanel address, user name and, when
// needSecret, the password/API token, which it seals. An empty profID picks the profile from the
// cPanel address (profile match: list, else generic); projects always pass "". A named profID
// (admin hosts pages) falls back to the same automatic choice when unknown.
func (s *Server) parseHostInput(addr, user, secret, profID string, needSecret bool) (hostInput, error) {
	prof, ok := profile.Find(s.profiles(), profID)
	if !ok || profID == "" {
		if prof = profile.ForAddr(s.profiles(), addr); prof == nil {
			return hostInput{}, errors.New("پروفایل هاست پیدا نشد")
		}
	}
	addr, user = strings.TrimSpace(addr), strings.TrimSpace(user)
	if addr == "" || user == "" {
		return hostInput{}, errors.New("آدرس cPanel و نام کاربری لازم است")
	}
	base, err := cpanel.NormalizeURL(addr, prof.Port)
	if err != nil {
		return hostInput{}, errors.New("آدرس cPanel نامعتبر است")
	}
	if secret == "" && needSecret {
		return hostInput{}, errors.New("رمز عبور یا API Token لازم است")
	}
	in := hostInput{URL: base, User: user, Profile: prof.ID}
	if secret != "" {
		if in.Sealed, err = s.box.Seal(secret); err != nil {
			return hostInput{}, errors.New("رمز عبور رمزنگاری نشد")
		}
	}
	return in, nil
}

// addProjectHost saves a new host record owned by p and links it to p.
func (s *Server) addProjectHost(p projects.Project, in hostInput) (store.Host, error) {
	name := projectAddr(p).Parent
	if name == "" {
		name = strings.TrimPrefix(strings.SplitN(in.URL, "://", 2)[1], "www.")
	}
	h, err := s.store.Add(store.Host{Name: name, ProfileID: in.Profile, URL: in.URL, Username: in.User, SealedSecret: in.Sealed, ProjectID: p.ID})
	if err != nil {
		return h, err
	}
	if err := s.projects.Update(p.ID, func(x *projects.Project) { x.HostID = h.ID }); err != nil {
		_ = s.store.Delete(h.ID)
		return h, err
	}
	return h, nil
}

// handleProjectHostSave has two jobs. A project without a host (made before hosts were set at
// creation) enters its connection here once: it is saved as the project's own record, linked
// and checked. Once a project has its host, the cPanel address, user name and profile are fixed:
// only a new password/API token is taken (credential rotation); posted url/username are ignored.
func (s *Server) handleProjectHostSave(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectOr404(w, r)
	if !ok {
		return
	}
	back := s.cfg.BasePath + "p/" + p.ID + "/host"
	fail := func(msg string) { http.Redirect(w, r, back+"?err="+url.QueryEscape(msg), http.StatusSeeOther) }
	if cur, linked := s.ownHost(p); linked {
		secret := r.FormValue("secret")
		if secret == "" {
			fail("رمز عبور یا API Token تازه را بنویسید")
			return
		}
		sealed, err := s.box.Seal(secret)
		if err != nil {
			http.Error(w, "cannot encrypt the password", 500)
			return
		}
		if err := s.store.Update(cur.ID, func(h *store.Host) { h.SealedSecret, h.FailedLogins = sealed, 0 }); err != nil {
			fail(err.Error())
			return
		}
		s.startHostCheck(w, r, p.ID, cur.ID, "رمز/توکن به‌روزرسانی شد و بررسی هاست شروع شد.")
		return
	}
	// the profile is picked from the cPanel address; a posted profile is ignored
	in, err := s.parseHostInput(r.FormValue("url"), r.FormValue("username"), r.FormValue("secret"), "", true)
	if err != nil {
		fail(err.Error())
		return
	}
	h, err := s.addProjectHost(p, in)
	if err != nil {
		fail(err.Error())
		return
	}
	s.startHostCheck(w, r, p.ID, h.ID, "اطلاعات اتصال ذخیره شد و بررسی هاست شروع شد.")
}

// handleProjectHostCheck runs the host check again on the project's host.
func (s *Server) handleProjectHostCheck(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectOr404(w, r)
	if !ok {
		return
	}
	if _, linked := s.ownHost(p); !linked {
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
