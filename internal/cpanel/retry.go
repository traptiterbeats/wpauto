package cpanel

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptrace"
	"regexp"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// Retry policy for transient network trouble (a dropped connection, a TLS handshake that timed
// out, a 502/503/504 from a proxy in front of cPanel). One hiccup must not fail a whole run.
//
// What is retried:
//   - reads (UAPI get_*/list_*/php_get_*/domains_data/single_domain_data, Tokens::list): on any
//     transient error, including ones after the request was sent, and on HTTP 502/503/504;
//   - everything else (login, writes, uploads, API2 Fileman ops): only when the connection was
//     never established (dial / DNS / TLS handshake failed), so the host cannot have seen it.
//
// Never retried: authentication failures (401/403, invalid_login, a login page instead of JSON),
// API errors, a cancelled run context, and anything that is not a network error.
const maxAttempts = 3

// defaultDelays are the waits before the 2nd and 3rd attempts.
var defaultDelays = []time.Duration{2 * time.Second, 5 * time.Second}

// readCall matches UAPI functions that only read, so repeating them is always safe.
var readCall = regexp.MustCompile(`^(get_|list_|php_get_)|^(list|domains_data|single_domain_data)$`)

// IsReadCall reports whether a UAPI module/function only reads.
func IsReadCall(module, function string) bool { return readCall.MatchString(function) }

// response is a fully read HTTP response.
type response struct {
	status int
	body   []byte
}

// NetError is a request that failed on the network (after any retries). Its message is a Persian
// line for the user followed by the technical detail on a second line.
type NetError struct {
	Err     error
	Status  int  // 502/503/504 when the host answered with a gateway error
	Unknown bool // a write whose connection dropped after it was sent: its outcome is unknown
}

func (e *NetError) Unwrap() error { return e.Err }

func (e *NetError) Error() string {
	detail := ""
	if e.Err != nil {
		detail = e.Err.Error()
	} else if e.Status != 0 {
		detail = fmt.Sprintf("HTTP %d", e.Status)
	}
	return NetMessage(e.Err, e.Status, e.Unknown) + "\nجزئیات فنی: " + detail
}

const checkNet = "اینترنت سرور wpauto و در دسترس بودن هاست را بررسی کنید و دوباره اجرا کنید."

// NetMessage is the Persian explanation for a network failure.
func NetMessage(err error, status int, unknown bool) string {
	if status != 0 {
		return fmt.Sprintf("هاست موقتاً در دسترس نیست (خطای HTTP %d). چند دقیقه بعد دوباره اجرا کنید.", status)
	}
	prefix := ""
	if unknown {
		prefix = "اتصال با هاست وسط این عملیات قطع شد؛ معلوم نیست روی هاست انجام شد یا نه، برای همین خودکار تکرار نشد. "
	}
	var dns *net.DNSError
	var cert *tls.CertificateVerificationError
	s := ""
	if err != nil {
		s = strings.ToLower(err.Error())
	}
	switch {
	case strings.Contains(s, "tls handshake timeout"):
		return prefix + "ارتباط با هاست برقرار نشد (زمان اتصال امن تمام شد). " + checkNet
	case errors.As(err, &dns) && dns.IsNotFound:
		return prefix + "نام هاست پیدا نشد (DNS). آدرس cPanel را بررسی کنید؛ اگر درست است، DNS سرور wpauto را بررسی کنید."
	case errors.As(err, &dns):
		return prefix + "ارتباط با هاست برقرار نشد (DNS پاسخ نداد). اینترنت و DNS سرور wpauto را بررسی کنید و دوباره اجرا کنید."
	case errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(s, "connection refused"):
		return prefix + "هاست اتصال را نپذیرفت (پورت cPanel بسته است یا سرویس در دسترس نیست؛ ممکن است IP سرور wpauto در فایروال هاست بلاک شده باشد). آدرس و پورت را بررسی کنید یا کمی بعد دوباره اجرا کنید."
	case errors.As(err, &cert) || strings.Contains(s, "x509") || strings.Contains(s, "certificate"):
		return prefix + "گواهی SSL هاست پذیرفته نشد. آدرس cPanel یا تنظیم «پذیرش گواهی نامعتبر» در پروفایل هاست را بررسی کنید."
	case isTimeout(err):
		return prefix + "هاست در زمان مقرر پاسخ نداد. " + checkNet
	case errors.Is(err, syscall.ECONNRESET) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		strings.Contains(s, "connection reset") || strings.Contains(s, "broken pipe") || strings.Contains(s, "eof"):
		if prefix != "" {
			return strings.TrimSpace(prefix)
		}
		return "اتصال با هاست وسط کار قطع شد. " + checkNet
	}
	return prefix + "ارتباط با هاست برقرار نشد. " + checkNet
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) ||
		strings.Contains(strings.ToLower(fmt.Sprint(err)), "timeout")
}

// transient reports whether err is a network failure worth another attempt.
func transient(err error) bool {
	if err == nil {
		return false
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return dns.IsTimeout || dns.IsTemporary
	}
	var cert *tls.CertificateVerificationError
	if errors.As(err, &cert) {
		return false
	}
	if isTimeout(err) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return true
	}
	s := strings.ToLower(err.Error())
	for _, m := range []string{"connection reset", "connection refused", "broken pipe", "unexpected eof", "eof", "server closed idle connection"} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

func gatewayStatus(code int) bool {
	return code == http.StatusBadGateway || code == http.StatusServiceUnavailable || code == http.StatusGatewayTimeout
}

// faDigits writes n with Persian digits.
func faDigits(n int) string {
	var b strings.Builder
	for _, r := range fmt.Sprint(n) {
		if r >= '0' && r <= '9' {
			r = '۰' + (r - '0')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// RetryMessage is the run-log line written before attempt n of max.
func RetryMessage(n, max int) string {
	return fmt.Sprintf("اتصال به هاست لحظه‌ای قطع شد؛ تلاش دوباره (%s از %s)…", faDigits(n), faDigits(max))
}

// OnRetry sets where retry notices go (the run log). nil = nowhere.
func (c *Client) OnRetry(fn func(msg string)) { c.onRetry = fn }

// SetRetryDelays replaces the waits between attempts (tests use tiny ones). Its length+1 is the
// number of attempts, at most maxAttempts.
func (c *Client) SetRetryDelays(d ...time.Duration) { c.delays = d }

func (c *Client) wait(ctx context.Context, i int) error {
	delays := c.delays
	if delays == nil {
		delays = defaultDelays
	}
	d := delays[min(i, len(delays)-1)]
	if c.delays == nil && d > 0 {
		d += time.Duration(rand.Int64N(int64(500 * time.Millisecond))) // jitter
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// do sends a request built fresh by build for each attempt (so bodies are recreated) and reads
// the whole response (up to limit bytes). read marks a request that is safe to repeat even after
// it reached the host; other requests are repeated only when no connection was established.
func (c *Client) do(ctx context.Context, hc *http.Client, read bool, limit int64, build func(context.Context) (*http.Request, error)) (*response, error) {
	attempts := maxAttempts
	if c.delays != nil {
		attempts = min(maxAttempts, len(c.delays)+1)
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			if c.onRetry != nil {
				c.onRetry(RetryMessage(i+1, attempts))
			}
			if err := c.wait(ctx, i-1); err != nil {
				return nil, err
			}
		}
		var connected atomic.Bool
		tctx := httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { connected.Store(true) }})
		req, err := build(tctx)
		if err != nil {
			return nil, err
		}
		resp, err := hc.Do(req)
		var body []byte
		if err == nil {
			body, err = io.ReadAll(io.LimitReader(resp.Body, limit))
			resp.Body.Close()
		}
		if ctx.Err() != nil { // the run was cancelled or timed out: stop, do not dress it up
			return nil, ctx.Err()
		}
		if err != nil {
			lastErr = err
			if !transient(err) {
				return nil, &NetError{Err: err}
			}
			if !read && connected.Load() {
				return nil, &NetError{Err: err, Unknown: true}
			}
			continue
		}
		if read && gatewayStatus(resp.StatusCode) {
			lastErr = &NetError{Status: resp.StatusCode}
			continue
		}
		if gatewayStatus(resp.StatusCode) && len(body) > 0 && body[0] != '{' {
			// a proxy error page for a write: it may or may not have reached cPanel
			return nil, &NetError{Status: resp.StatusCode}
		}
		return &response{status: resp.StatusCode, body: body}, nil
	}
	var ne *NetError
	if errors.As(lastErr, &ne) {
		return nil, ne
	}
	return nil, &NetError{Err: lastErr}
}
