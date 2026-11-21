// Package web serves the wpauto panel.
package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/traptiterbeats/wpauto/internal/ai"
	"github.com/traptiterbeats/wpauto/internal/config"
	"github.com/traptiterbeats/wpauto/internal/engine"
	"github.com/traptiterbeats/wpauto/internal/projects"
	"github.com/traptiterbeats/wpauto/internal/secretbox"
	"github.com/traptiterbeats/wpauto/internal/store"
)

//go:embed templates/*.html
var tplFS embed.FS

//go:embed static/*
var staticFS embed.FS

const cookieName = "wpauto_session"
const sessionTTL = 12 * time.Hour

// Server is the HTTP panel.
type Server struct {
	cfg     *config.Config
	version string
	tpl     *template.Template
	limiter *limiter

	store       *store.Store
	box         *secretbox.Box
	profileDir  string
	runner      *engine.Runner
	workflowDir string
	ai          *ai.Store
	projects    *projects.Store
	ap          *apStore
}

// New builds a server from the configuration.
func New(cfg *config.Config, version string) (*Server, error) {
	t, err := template.ParseFS(tplFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	dir := config.Dir()
	st, err := store.Open(dir)
	if err != nil {
		return nil, err
	}
	box, err := secretbox.Open(dir)
	if err != nil {
		return nil, err
	}
	runner, err := engine.NewRunner(filepath.Join(dir, "runs"))
	if err != nil {
		return nil, err
	}
	aiStore, err := ai.OpenStore(dir, box)
	if err != nil {
		return nil, err
	}
	projStore, err := projects.Open(dir)
	if err != nil {
		return nil, err
	}
	resetStuckDrafts()
	srv0 := &Server{store: st, projects: projStore}
	if err := srv0.migrateHosts(); err != nil {
		return nil, err
	}
	apStore, err := openAP(filepath.Join(dir, "autopilot"))
	if err != nil {
		return nil, err
	}
	return &Server{ap: apStore, cfg: cfg, version: version, tpl: t, limiter: newLimiter(),
		store: st, box: box, profileDir: filepath.Join(dir, "profiles"),
		runner: runner, workflowDir: filepath.Join(dir, "workflows"), ai: aiStore, projects: projStore}, nil
}

// Handler returns the routed handler mounted under the configured base path.
func (s *Server) Handler() http.Handler {
	bp := s.cfg.BasePath
	mux := http.NewServeMux()
	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle(bp+"static/", http.StripPrefix(bp+"static/", http.FileServer(http.FS(sub))))
	mux.HandleFunc(bp+"login", s.handleLogin)
	mux.HandleFunc(bp+"logout", s.handleLogout)
	mux.HandleFunc("GET "+bp+"hosts", s.requireAuth(s.handleHosts))
	mux.HandleFunc("POST "+bp+"hosts/add", s.requireAuth(s.handleHostAdd))
	mux.HandleFunc("GET "+bp+"hosts/{id}", s.requireAuth(s.handleHostView))
	mux.HandleFunc("POST "+bp+"hosts/{id}/test", s.requireAuth(s.handleHostTest))
	mux.HandleFunc("POST "+bp+"hosts/{id}/edit", s.requireAuth(s.handleHostEdit))
	mux.HandleFunc("POST "+bp+"hosts/{id}/delete", s.requireAuth(s.handleHostDelete))
	mux.HandleFunc("GET "+bp+"run/{wf}", s.requireAuth(s.handleRunForm))
	mux.HandleFunc("GET "+bp+"run/{wf}/plan", s.requireAuth(s.handleRunPlan))
	mux.HandleFunc("POST "+bp+"run/{wf}", s.requireAuth(s.handleRunStart))
	mux.HandleFunc("GET "+bp+"runs", s.requireAuth(s.handleRuns))
	mux.HandleFunc("GET "+bp+"runs/{id}", s.requireAuth(s.handleRunView))
	mux.HandleFunc("GET "+bp+"runs/{id}/json", s.requireAuth(s.handleRunJSON))
	mux.HandleFunc("POST "+bp+"runs/{id}/cancel", s.requireAuth(s.handleRunCancel))
	mux.HandleFunc("GET "+bp+"autopilot", s.requireAuth(s.handleAutopilot))
	mux.HandleFunc("POST "+bp+"autopilot/start", s.uploads(s.handleAutopilotStart))
	mux.HandleFunc("GET "+bp+"autopilot/{id}", s.requireAuth(s.handleAutopilotView))
	mux.HandleFunc("GET "+bp+"autopilot/{id}/json", s.requireAuth(s.handleAutopilotJSON))
	mux.HandleFunc("POST "+bp+"autopilot/{id}/{op}", s.requireAuth(s.handleAutopilotOp))
	mux.HandleFunc("GET "+bp+"wordpress", s.requireAuth(s.handleWordPress))
	mux.HandleFunc("POST "+bp+"wordpress/{op}", s.requireAuth(s.handleWordPressOp))
	mux.HandleFunc("GET "+bp+"projects", s.requireAuth(s.handleProjects))
	mux.HandleFunc("GET "+bp+"p/{id}", s.requireAuth(s.handleProjectView))
	mux.HandleFunc("GET "+bp+"p/{id}/wordpress", s.requireAuth(s.handleProjectWP))
	mux.HandleFunc("GET "+bp+"p/{id}/host", s.requireAuth(s.handleProjectHost))
	mux.HandleFunc("POST "+bp+"p/{id}/host/save", s.requireAuth(s.handleProjectHostSave))
	mux.HandleFunc("POST "+bp+"p/{id}/host/check", s.requireAuth(s.handleProjectHostCheck))
	mux.HandleFunc("GET "+bp+"tasks", s.requireAuth(s.handleTasks))
	mux.HandleFunc("GET "+bp+"tools", s.requireAuth(s.handleTools))
	mux.HandleFunc("POST "+bp+"projects/new", s.requireAuth(s.handleProjectNew))
	mux.HandleFunc("POST "+bp+"projects/{id}/{op}", s.requireAuth(s.handleProjectOp))
	mux.HandleFunc("GET "+bp+"themes", s.requireAuth(s.handleThemes))
	mux.HandleFunc("POST "+bp+"themes/add", s.uploads(s.handleThemeAdd))
	mux.HandleFunc("POST "+bp+"themes/{slug}/delete", s.requireAuth(s.handleThemeDelete))
	mux.HandleFunc("GET "+bp+"themes/{slug}/screenshot", s.requireAuth(s.handleThemeShot))
	mux.HandleFunc("POST "+bp+"plugins/add", s.uploads(s.handlePluginAdd))
	mux.HandleFunc("POST "+bp+"plugins/{slug}/delete", s.requireAuth(s.handlePluginDelete))
	mux.HandleFunc("GET "+bp+"ai", s.requireAuth(s.handleAI))
	mux.HandleFunc("POST "+bp+"ai/add", s.requireAuth(s.handleAIAdd))
	mux.HandleFunc("GET "+bp+"ai/{id}/models", s.requireAuth(s.handleAIModels))
	mux.HandleFunc("POST "+bp+"ai/{id}/model", s.requireAuth(s.handleAIModelSet))
	mux.HandleFunc("POST "+bp+"ai/{id}/{op}", s.requireAuth(s.handleAIOp))
	mux.HandleFunc("GET "+bp+"customize", s.requireAuth(s.handleDrafts))
	mux.HandleFunc("POST "+bp+"customize/new", s.requireAuth(s.handleDraftNew))
	mux.HandleFunc("GET "+bp+"customize/{id}", s.requireAuth(s.handleDraft))
	mux.HandleFunc("GET "+bp+"customize/{id}/file/{name}", s.requireAuth(s.handleDraftFile))
	mux.HandleFunc("GET "+bp+"customize/{id}/status", s.requireAuth(s.handleDraftStatus))
	mux.HandleFunc("POST "+bp+"customize/{id}/images", s.uploads(s.handleDraftImages))
	mux.HandleFunc("POST "+bp+"customize/{id}/{op}", s.requireAuth(s.handleDraftOp))
	mux.HandleFunc(bp+"api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"version":%q}`, s.version)
	})
	mux.HandleFunc(bp, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != bp {
			http.NotFound(w, r)
			return
		}
		if !s.authed(r) {
			http.Redirect(w, r, bp+"login", http.StatusSeeOther)
			return
		}
		s.handleHome(w, r)
	})
	// Anything outside the secret base path looks like nothing is here.
	root := http.NewServeMux()
	root.Handle(bp, mux)
	root.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path+"/" == bp {
			http.Redirect(w, r, bp, http.StatusMovedPermanently)
			return
		}
		http.NotFound(w, r)
	})
	return secureHeaders(root)
}

// ListenAndServe starts the panel, with TLS when certificates are configured.
func (s *Server) ListenAndServe() error {
	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(s.cfg.Port),
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if s.cfg.TLSEnabled() {
		log.Printf("wpauto %s listening on https://0.0.0.0:%d%s", s.version, s.cfg.Port, s.cfg.BasePath)
		return srv.ListenAndServeTLS(s.cfg.CertFile, s.cfg.KeyFile)
	}
	log.Printf("wpauto %s listening on http://0.0.0.0:%d%s", s.version, s.cfg.Port, s.cfg.BasePath)
	return srv.ListenAndServe()
}

func (s *Server) render(w http.ResponseWriter, name string, data map[string]any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("template %s: %v", name, err)
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	bp := s.cfg.BasePath
	ip := clientIP(r)
	data := map[string]any{"BasePath": bp, "Version": s.version}
	if r.Method == http.MethodPost {
		if wait := s.limiter.blocked(ip); wait > 0 {
			data["Error"] = fmt.Sprintf("تلاش زیاد. %d دقیقه‌ی دیگر دوباره امتحان کنید.", int(wait.Minutes())+1)
			w.WriteHeader(http.StatusTooManyRequests)
			s.render(w, "login.html", data)
			return
		}
		u := r.FormValue("username")
		p := r.FormValue("password")
		okUser := subtle.ConstantTimeCompare([]byte(u), []byte(s.cfg.Username)) == 1
		if okUser && s.cfg.CheckPassword(p) {
			s.limiter.reset(ip)
			s.setSession(w, r)
			http.Redirect(w, r, bp, http.StatusSeeOther)
			return
		}
		s.limiter.fail(ip)
		log.Printf("failed login from %s", ip)
		data["Error"] = "نام کاربری یا رمز عبور اشتباه است."
		w.WriteHeader(http.StatusUnauthorized)
	} else if s.authed(r) {
		http.Redirect(w, r, bp, http.StatusSeeOther)
		return
	}
	s.render(w, "login.html", data)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.authed(r) || r.FormValue("csrf") != s.csrf(r) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: s.cfg.BasePath, MaxAge: -1})
	http.Redirect(w, r, s.cfg.BasePath+"login", http.StatusSeeOther)
}

// --- sessions: payload "user|expiry" signed with HMAC-SHA256 ---

func (s *Server) sign(payload string) string {
	m := hmac.New(sha256.New, []byte(s.cfg.SessionKey+s.cfg.PasswordHash))
	m.Write([]byte(payload))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Server) setSession(w http.ResponseWriter, r *http.Request) {
	exp := time.Now().Add(sessionTTL).Unix()
	payload := s.cfg.Username + "|" + strconv.FormatInt(exp, 10)
	val := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + s.sign(payload)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: val, Path: s.cfg.BasePath, HttpOnly: true,
		Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL.Seconds()),
	})
}

func (s *Server) authed(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	parts := strings.SplitN(c.Value, ".", 2)
	if len(parts) != 2 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	payload := string(raw)
	if !hmac.Equal([]byte(s.sign(payload)), []byte(parts[1])) {
		return false
	}
	f := strings.SplitN(payload, "|", 2)
	if len(f) != 2 || f[0] != s.cfg.Username {
		return false
	}
	exp, err := strconv.ParseInt(f[1], 10, 64)
	return err == nil && time.Now().Unix() < exp
}

func (s *Server) csrf(r *http.Request) string {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	return s.sign("csrf:" + c.Value)[:32]
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func secureHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		h.ServeHTTP(w, r)
	})
}

// --- login rate limiter: 5 failures => 15 minute block per IP ---

type limiter struct {
	mu   sync.Mutex
	hits map[string]*hit
}

type hit struct {
	n     int
	until time.Time
}

func newLimiter() *limiter { return &limiter{hits: map[string]*hit{}} }

func (l *limiter) blocked(ip string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if h, ok := l.hits[ip]; ok && time.Now().Before(h.until) {
		return time.Until(h.until)
	}
	return 0
}

func (l *limiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	h, ok := l.hits[ip]
	if !ok {
		h = &hit{}
		l.hits[ip] = h
	}
	h.n++
	if h.n >= 5 {
		h.until = time.Now().Add(15 * time.Minute)
		h.n = 0
	}
}

func (l *limiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, ip)
}
