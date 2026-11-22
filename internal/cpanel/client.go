// Package cpanel is a small client for cPanel's UAPI. It signs in either with a cPanel API token
// or, on hosts where API tokens are disabled, with a username/password session (login_only →
// cpsess security token + session cookie). Host-specific behaviour comes from a profile.
package cpanel

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// AuthMethod selects how the client signs in.
type AuthMethod string

const (
	AuthSession AuthMethod = "session"
	AuthToken   AuthMethod = "token"
)

// ErrInvalidLogin is returned when cPanel rejects the credentials. Callers must not retry
// automatically: many hosts run CSF/cPHulk and block the IP after a few failures.
var ErrInvalidLogin = errors.New("cpanel rejected the username or password")

// Options configure a client.
type Options struct {
	BaseURL     string // e.g. https://example.com:2083
	Username    string
	Secret      string // password (session) or API token (token)
	Auth        AuthMethod
	InsecureTLS bool
	Timeout     time.Duration
}

// Client talks to one cPanel account.
type Client struct {
	opt     Options
	http    *http.Client
	token   string // /cpsessNNNN for session auth
	onRetry func(string)
	delays  []time.Duration // nil = defaultDelays (with jitter)
}

// New creates a client. Nothing is sent until Login or Call.
func New(opt Options) (*Client, error) {
	base, err := NormalizeURL(opt.BaseURL, 2083)
	if err != nil {
		return nil, err
	}
	opt.BaseURL = base
	if opt.Auth == "" {
		opt.Auth = AuthSession
	}
	if opt.Timeout == 0 {
		opt.Timeout = 30 * time.Second
	}
	jar, _ := cookiejar.New(nil)
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: opt.InsecureTLS} //nolint:gosec // opt-in per profile
	return &Client{opt: opt, http: &http.Client{Jar: jar, Transport: tr, Timeout: opt.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// BaseURL returns the normalised cPanel URL.
func (c *Client) BaseURL() string { return c.opt.BaseURL }

// NormalizeURL turns "example.com", "example.com:2083" or "https://example.com/cpanel" into
// "https://example.com:2083".
func NormalizeURL(raw string, defPort int) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("cpanel address is empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("invalid cpanel address %q", raw)
	}
	port := u.Port()
	if port == "" {
		port = fmt.Sprint(defPort)
	}
	scheme := u.Scheme
	if port == "2082" {
		scheme = "http"
	}
	return fmt.Sprintf("%s://%s:%s", scheme, u.Hostname(), port), nil
}

// Login opens a session (session auth). With token auth it only verifies the token.
func (c *Client) Login(ctx context.Context) error {
	if c.opt.Auth == AuthToken {
		_, err := c.Call(ctx, "Variables", "get_user_information", nil)
		return err
	}
	form := url.Values{"user": {c.opt.Username}, "pass": {c.opt.Secret}}.Encode()
	// The login is never repeated once it reached the host (CSF counts failed logins); only a
	// connection that never got established is tried again.
	resp, err := c.do(ctx, c.http, false, 1<<20, func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "POST", c.opt.BaseURL+"/login/?login_only=1", strings.NewReader(form))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		return req, err
	})
	if err != nil {
		return err
	}
	body := resp.body
	var r struct {
		Status        int    `json:"status"`
		SecurityToken string `json:"security_token"`
		Message       string `json:"message"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return fmt.Errorf("unexpected login response (HTTP %d)", resp.status)
	}
	if r.Status != 1 || r.SecurityToken == "" {
		if resp.status == http.StatusUnauthorized || r.Message == "invalid_login" {
			return ErrInvalidLogin
		}
		return fmt.Errorf("cpanel login failed: %s (HTTP %d)", r.Message, resp.status)
	}
	c.token = r.SecurityToken
	return nil
}

// Result is a UAPI response.
type Result struct {
	Status   int             `json:"status"`
	Data     json.RawMessage `json:"data"`
	Errors   []string        `json:"errors"`
	Messages []string        `json:"messages"`
}

// APIError is a UAPI call that returned status 0.
type APIError struct {
	Module, Function string
	Errors           []string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s::%s failed: %s", e.Module, e.Function, strings.Join(e.Errors, "; "))
}

// Call runs a UAPI function and returns its data.
func (c *Client) Call(ctx context.Context, module, function string, args url.Values) (json.RawMessage, error) {
	prefix := ""
	if c.opt.Auth == AuthSession {
		if c.token == "" {
			return nil, errors.New("not logged in")
		}
		prefix = c.token
	}
	u := fmt.Sprintf("%s%s/execute/%s/%s", c.opt.BaseURL, prefix, module, function)
	method, form := "GET", ""
	if len(args) > 0 {
		method, form = "POST", args.Encode()
	}
	resp, err := c.do(ctx, c.http, IsReadCall(module, function), 16<<20, func(ctx context.Context) (*http.Request, error) {
		var body io.Reader
		if method == "POST" {
			body = strings.NewReader(form)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, body)
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if c.opt.Auth == AuthToken {
			req.Header.Set("Authorization", "cpanel "+c.opt.Username+":"+c.opt.Secret)
		}
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	raw := resp.body
	if resp.status == http.StatusUnauthorized || resp.status == http.StatusForbidden {
		if c.opt.Auth == AuthToken {
			return nil, ErrInvalidLogin
		}
		return nil, fmt.Errorf("session expired or access denied (HTTP %d)", resp.status)
	}
	var r Result
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("%s::%s: unexpected response (HTTP %d)", module, function, resp.status)
	}
	if r.Status != 1 {
		return nil, &APIError{Module: module, Function: function, Errors: r.Errors}
	}
	return r.Data, nil
}

// Logout ends the session (best effort).
func (c *Client) Logout(ctx context.Context) {
	if c.opt.Auth != AuthSession || c.token == "" {
		return
	}
	// One short attempt: a host that is hard to reach must not hold the end of a run.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", c.opt.BaseURL+c.token+"/logout/", nil)
	if resp, err := c.http.Do(req); err == nil {
		resp.Body.Close()
	}
	c.token = ""
}
