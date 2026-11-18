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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/traptiterbeats/wpauto/internal/config"
	"github.com/traptiterbeats/wpauto/internal/tasks"
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
}

// New builds a server from the configuration.
func New(cfg *config.Config, version string) (*Server, error) {
	t, err := template.ParseFS(tplFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, version: version, tpl: t, limiter: newLimiter()}, nil
}

// Handler returns the routed handler mounted under the configured base path.
func (s *Server) Handler() http.Handler {
	bp := s.cfg.BasePath
	mux := http.NewServeMux()
	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle(bp+"static/", http.StripPrefix(bp+"static/", http.FileServer(http.FS(sub))))
	mux.HandleFunc(bp+"login", s.handleLogin)
	mux.HandleFunc(bp+"logout", s.handleLogout)
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
		s.render(w, "dashboard.html", map[string]any{
			"BasePath": bp, "Version": s.version, "User": s.cfg.Username, "Tasks": tasks.Catalogue,
			"CSRF": s.csrf(r),
		})
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
