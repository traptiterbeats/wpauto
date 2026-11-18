package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/traptiterbeats/wpauto/internal/cpanel"
	"github.com/traptiterbeats/wpauto/internal/profile"
	"github.com/traptiterbeats/wpauto/internal/store"
)

func (s *Server) requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			http.Redirect(w, r, s.cfg.BasePath+"login", http.StatusSeeOther)
			return
		}
		if r.Method == http.MethodPost && r.FormValue("csrf") != s.csrf(r) {
			http.Error(w, "invalid form token, reload the page", http.StatusBadRequest)
			return
		}
		h(w, r)
	}
}

func (s *Server) page(r *http.Request, extra map[string]any) map[string]any {
	d := map[string]any{"BasePath": s.cfg.BasePath, "Version": s.version, "User": s.cfg.Username, "CSRF": s.csrf(r)}
	for k, v := range extra {
		d[k] = v
	}
	return d
}

func (s *Server) profiles() []*profile.Profile {
	list, err := profile.LoadAll(s.profileDir)
	if err != nil {
		// fall back to built-ins only; a broken custom file must not take the panel down
		list, _ = profile.LoadAll("")
	}
	return list
}

func (s *Server) handleHosts(w http.ResponseWriter, r *http.Request) {
	s.render(w, "hosts.html", s.page(r, map[string]any{
		"Hosts": s.store.List(), "Profiles": s.profiles(), "Error": r.URL.Query().Get("err"),
	}))
}

func (s *Server) handleHostAdd(w http.ResponseWriter, r *http.Request) {
	bp := s.cfg.BasePath
	name := strings.TrimSpace(r.FormValue("name"))
	pid := r.FormValue("profile")
	addr := r.FormValue("url")
	user := strings.TrimSpace(r.FormValue("username"))
	secret := r.FormValue("secret")
	prof, ok := profile.Find(s.profiles(), pid)
	if !ok || user == "" || secret == "" || addr == "" {
		http.Redirect(w, r, bp+"hosts?err=همه‌ی فیلدها لازم است", http.StatusSeeOther)
		return
	}
	base, err := cpanel.NormalizeURL(addr, prof.Port)
	if err != nil {
		http.Redirect(w, r, bp+"hosts?err=آدرس cPanel نامعتبر است", http.StatusSeeOther)
		return
	}
	if name == "" {
		name = strings.TrimPrefix(strings.SplitN(base, "://", 2)[1], "www.")
	}
	sealed, err := s.box.Seal(secret)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	h, err := s.store.Add(store.Host{Name: name, ProfileID: prof.ID, URL: base, Username: user, SealedSecret: sealed})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, bp+"hosts/"+h.ID, http.StatusSeeOther)
}

func (s *Server) handleHostView(w http.ResponseWriter, r *http.Request) {
	h, ok := s.store.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	prof, _ := profile.Find(s.profiles(), h.ProfileID)
	locked := prof != nil && h.Locked(prof.MaxLoginAttempts)
	s.render(w, "host.html", s.page(r, map[string]any{"Host": h, "Profile": prof, "Locked": locked, "Profiles": s.profiles()}))
}

// handleHostTest signs in and runs the read-only preflight.
func (s *Server) handleHostTest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	back := s.cfg.BasePath + "hosts/" + id
	h, ok := s.store.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	prof, ok := profile.Find(s.profiles(), h.ProfileID)
	if !ok {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	if h.Locked(prof.MaxLoginAttempts) {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	rep, err := s.preflight(r.Context(), h, prof)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	_ = s.store.Update(id, func(x *store.Host) {
		x.LastReport = rep
		if rep.LoginOK {
			x.FailedLogins = 0
		} else if loginRejected(rep) {
			x.FailedLogins++
		}
	})
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func loginRejected(rep *cpanel.Report) bool {
	for _, c := range rep.Checks {
		if c.ID == "login" && !c.OK && c.Detail == cpanel.ErrInvalidLogin.Error() {
			return true
		}
	}
	return false
}

func (s *Server) preflight(ctx context.Context, h store.Host, prof *profile.Profile) (*cpanel.Report, error) {
	secret, err := s.box.Open(h.SealedSecret)
	if err != nil {
		return nil, errors.New("cannot decrypt the saved password; re-enter it")
	}
	c, err := cpanel.New(cpanel.Options{BaseURL: h.URL, Username: h.Username, Secret: secret,
		Auth: cpanel.AuthMethod(prof.Auth), InsecureTLS: prof.InsecureTLS})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	return cpanel.Preflight(ctx, c, prof.Probes, prof.PreferredPHP), nil
}

// handleHostEdit updates credentials/profile and clears the failed-login lock.
func (s *Server) handleHostEdit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	user := strings.TrimSpace(r.FormValue("username"))
	secret := r.FormValue("secret")
	pid := r.FormValue("profile")
	var sealed string
	if secret != "" {
		var err error
		if sealed, err = s.box.Seal(secret); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	_, profOK := profile.Find(s.profiles(), pid)
	_ = s.store.Update(id, func(h *store.Host) {
		if user != "" {
			h.Username = user
		}
		if sealed != "" {
			h.SealedSecret = sealed
		}
		if profOK {
			h.ProfileID = pid
		}
		h.FailedLogins = 0
	})
	http.Redirect(w, r, s.cfg.BasePath+"hosts/"+id, http.StatusSeeOther)
}

func (s *Server) handleHostDelete(w http.ResponseWriter, r *http.Request) {
	_ = s.store.Delete(r.PathValue("id"))
	http.Redirect(w, r, s.cfg.BasePath+"hosts", http.StatusSeeOther)
}
