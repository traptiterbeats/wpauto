package wp

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Site reaches a WordPress site by host name. When the name does not resolve yet (a subdomain
// created a minute ago), it connects to Addr (the account's IP) and still sends the right Host
// header and TLS server name. Certificates are not verified for these internal calls: a new
// subdomain usually has no certificate until AutoSSL runs.
type Site struct {
	Host   string // sub.example.com
	Path   string // "/" or "/blog/"
	Addr   string // optional IP or ip:port override
	Scheme string // set by Detect: https or http
	hc     *http.Client
}

func (s *Site) client() *http.Client {
	if s.hc != nil {
		return s.hc
	}
	d := &net.Dialer{Timeout: 15 * time.Second}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: s.Host}, //nolint:gosec // see type doc
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if s.Addr != "" {
				_, port, _ := net.SplitHostPort(addr)
				target := s.Addr
				if _, _, err := net.SplitHostPort(target); err != nil {
					target = net.JoinHostPort(target, port)
				}
				return d.DialContext(ctx, network, target)
			}
			return d.DialContext(ctx, network, addr)
		},
		ResponseHeaderTimeout: 10 * time.Minute,
	}
	s.hc = &http.Client{Transport: tr, Timeout: 11 * time.Minute}
	return s.hc
}

// URL builds a URL on the site.
func (s *Site) URL(p string) string {
	scheme := s.Scheme
	if scheme == "" {
		scheme = "https"
	}
	base := strings.TrimSuffix(s.Path, "/")
	return scheme + "://" + s.Host + base + "/" + strings.TrimPrefix(p, "/")
}

// Detect picks https when the site answers on it, else http.
func (s *Site) Detect(ctx context.Context) error {
	var lastErr error
	for _, sc := range []string{"https", "http"} {
		s.Scheme = sc
		req, _ := http.NewRequestWithContext(ctx, "HEAD", s.URL("/"), nil)
		resp, err := s.client().Do(req)
		if err == nil {
			resp.Body.Close()
			return nil
		}
		lastErr = err
	}
	s.Scheme = ""
	return fmt.Errorf("site %s is not reachable: %w", s.Host, lastErr)
}

// HasValidTLS reports whether https works with a verified certificate.
func (s *Site) HasValidTLS(ctx context.Context) bool {
	if s.Addr != "" {
		if ips, err := net.DefaultResolver.LookupHost(ctx, s.Host); err != nil || len(ips) == 0 {
			return false
		}
	}
	c := &http.Client{Timeout: 15 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, "HEAD", "https://"+s.Host+s.Path, nil)
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// Helper calls the helper script with an action and decodes {"ok":..., ...}.
func (s *Site) Helper(ctx context.Context, file, token, action string) (map[string]any, error) {
	u := s.URL(file) + "?" + url.Values{"t": {token}, "a": {action}}.Encode()
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("User-Agent", "wpauto")
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out map[string]any
	// Find the JSON object even if the host prepends notices.
	if i := strings.Index(string(raw), "{\"ok\""); i >= 0 {
		raw = raw[i:]
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		snippet := strings.TrimSpace(string(raw))
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return nil, fmt.Errorf("helper %s: HTTP %d, unexpected response: %q", action, resp.StatusCode, snippet)
	}
	if ok, _ := out["ok"].(bool); !ok {
		return out, fmt.Errorf("helper %s: %v", action, out["error"])
	}
	return out, nil
}

// Get fetches a page and returns status and up to 512 KB of body.
func (s *Site) Get(ctx context.Context, p string) (int, string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", s.URL(p), nil)
	req.Header.Set("User-Agent", "wpauto-health")
	resp, err := s.client().Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	return resp.StatusCode, string(b), nil
}
