package wp

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
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
	// OnBusy, when set, is told about each wait after a 503 (maintenance mode).
	OnBusy func(attempt int, wait time.Duration)
	hc     *http.Client
}

// BusyWaits are the pauses before repeating a helper call answered with 503.
var BusyWaits = []time.Duration{10 * time.Second, 20 * time.Second, 30 * time.Second, 60 * time.Second}

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

// ErrNotHelper means the request reached a web server but not our helper script: the host's
// default page (a new vhost not live yet, or https served by another vhost before SSL is issued).
type ErrNotHelper struct {
	Scheme string
	Status int
}

func (e *ErrNotHelper) Error() string {
	return fmt.Sprintf("%s: HTTP %d بدون پاسخ helper (صفحه‌ی پیش‌فرض هاست)", e.Scheme, e.Status)
}

// Ping finds the helper script, trying https then http and waiting while a new subdomain's
// vhost goes live. It sets Scheme to the one that reached the helper. wait is the longest
// time to keep trying; notify (optional) is called once when it starts waiting.
func (s *Site) Ping(ctx context.Context, file, token string, wait time.Duration, notify func(error)) (map[string]any, error) {
	deadline := time.Now().Add(wait)
	notified := false
	for {
		var errs []string
		for _, sc := range []string{"https", "http"} {
			s.Scheme = sc
			out, err := s.Helper(ctx, file, token, "ping")
			if err == nil {
				return out, nil
			}
			var nh *ErrNotHelper
			if out != nil && !errors.As(err, &nh) {
				return out, err // the helper answered with an error of its own
			}
			errs = append(errs, sc+": "+err.Error())
		}
		last := errors.New(strings.Join(errs, " · "))
		if time.Now().After(deadline) {
			s.Scheme = ""
			return nil, fmt.Errorf("اسکریپت نصب روی %s در دسترس نیست: %w", s.Host, last)
		}
		if !notified && notify != nil {
			notify(last)
			notified = true
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
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
	return s.HelperArgs(ctx, file, token, action, nil)
}

// HelperArgs is Helper with extra query arguments (slugs and file names only; never secrets).
func (s *Site) HelperArgs(ctx context.Context, file, token, action string, args url.Values) (map[string]any, error) {
	q := url.Values{"t": {token}, "a": {action}}
	for k, v := range args {
		q[k] = v
	}
	u := s.URL(file) + "?" + q.Encode()
	var raw []byte
	var resp *http.Response
	for attempt := 0; ; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
		req.Header.Set("User-Agent", "wpauto")
		r, err := s.client().Do(req)
		if err != nil {
			return nil, err
		}
		raw, _ = io.ReadAll(io.LimitReader(r.Body, 1<<20))
		r.Body.Close()
		resp = r
		// 503 = WordPress maintenance mode (a core/plugin update is running, .maintenance)
		// or the host is briefly overloaded. WordPress stops before loading plugins, so the
		// action did not run and repeating it is safe.
		if r.StatusCode != http.StatusServiceUnavailable || attempt >= len(BusyWaits) {
			break
		}
		if s.OnBusy != nil {
			s.OnBusy(attempt+1, BusyWaits[attempt])
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(BusyWaits[attempt]):
		}
	}
	var out map[string]any
	// Find the JSON object even if the host prepends notices.
	if i := strings.Index(string(raw), "{\"ok\""); i >= 0 {
		raw = raw[i:]
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		if action == "ping" {
			return nil, &ErrNotHelper{Scheme: s.Scheme, Status: resp.StatusCode}
		}
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

// ChunkSize is how many zip entries one extract_chunk request unpacks; ChunkRetryWaits are the
// pauses before retrying a failed chunk (its length is the number of retries).
var (
	ChunkSize       = 300
	ChunkRetryWaits = []time.Duration{3 * time.Second, 10 * time.Second}
)

// ExtractChunks unpacks the WordPress zip next to the helper into its temp folder a chunk of
// entries per request (helper action extract_chunk), so no request runs long enough for a
// shared host to kill it. progress gets the entries done and the total after every chunk; retry
// is told about a failed chunk before it is tried again.
func (s *Site) ExtractChunks(ctx context.Context, file, token string, progress func(done, total int), retry func(from, attempt int, err error)) error {
	from := 0
	for {
		var res map[string]any
		var err error
		for attempt := 0; ; attempt++ {
			res, err = s.HelperArgs(ctx, file, token, "extract_chunk", url.Values{"from": {strconv.Itoa(from)}, "n": {strconv.Itoa(ChunkSize)}})
			if err == nil || attempt >= len(ChunkRetryWaits) || ctx.Err() != nil {
				break
			}
			if retry != nil {
				retry(from, attempt+1, err)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(ChunkRetryWaits[attempt]):
			}
		}
		if err != nil {
			return fmt.Errorf("extract entries from %d: %w", from, err)
		}
		next, _ := res["next"].(float64)
		total, _ := res["total"].(float64)
		done, _ := res["done"].(bool)
		if progress != nil {
			progress(int(next), int(total))
		}
		if done {
			return nil
		}
		if int(next) <= from {
			return fmt.Errorf("extract_chunk made no progress at entry %d", from)
		}
		from = int(next)
	}
}
