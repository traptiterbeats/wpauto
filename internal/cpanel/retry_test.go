package cpanel

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// dropConn closes the connection without an answer (the request did reach the server).
func dropConn(t *testing.T, w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		t.Fatal("no hijacker")
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

func okJSON(w http.ResponseWriter, data string) {
	_, _ = w.Write([]byte(`{"status":1,"data":` + data + `,"errors":null}`))
}

func tokenClient(t *testing.T, url string) (*Client, *[]string) {
	t.Helper()
	c, err := New(Options{BaseURL: url, Username: "u", Secret: "tok", Auth: AuthToken, InsecureTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	c.SetRetryDelays(time.Millisecond, time.Millisecond)
	var mu sync.Mutex
	var msgs []string
	c.OnRetry(func(m string) { mu.Lock(); msgs = append(msgs, m); mu.Unlock() })
	return c, &msgs
}

func TestReadRetriesThenSucceeds(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch n.Add(1) {
		case 1:
			dropConn(t, w) // EOF after the request was sent
		case 2:
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			okJSON(w, `{"megabytes_used":10,"megabyte_limit":100}`)
		}
	}))
	defer srv.Close()
	c, msgs := tokenClient(t, srv.URL)
	used, limit, err := c.DiskUsage(context.Background())
	if err != nil || used != 10 || limit != 100 {
		t.Fatalf("got %v %v %v", used, limit, err)
	}
	if n.Load() != 3 || len(*msgs) != 2 || (*msgs)[0] != "اتصال به هاست لحظه‌ای قطع شد؛ تلاش دوباره (۲ از ۳)…" || !strings.Contains((*msgs)[1], "(۳ از ۳)") {
		t.Fatalf("attempts %d, msgs %q", n.Load(), *msgs)
	}
}

func TestDomainsDataPOSTIsARead(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			dropConn(t, w)
			return
		}
		okJSON(w, `{}`)
	}))
	defer srv.Close()
	c, _ := tokenClient(t, srv.URL)
	if _, err := c.Call(context.Background(), "DomainInfo", "domains_data", url.Values{"format": {"hash"}}); err != nil || n.Load() != 2 {
		t.Fatalf("err %v attempts %d", err, n.Load())
	}
}

func TestReadGivesUpWithPersianMessage(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	c, _ := tokenClient(t, srv.URL)
	_, err := c.Call(context.Background(), "Quota", "get_quota_info", nil)
	var ne *NetError
	if !errors.As(err, &ne) || ne.Status != 502 || n.Load() != 3 {
		t.Fatalf("err %v attempts %d", err, n.Load())
	}
	if !strings.HasPrefix(err.Error(), "هاست موقتاً در دسترس نیست (خطای HTTP 502)") || !strings.Contains(err.Error(), "\nجزئیات فنی: HTTP 502") {
		t.Fatalf("message %q", err)
	}
}

func TestAuthFailuresAreNeverRetried(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		if strings.HasPrefix(r.URL.Path, "/login") {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"status":0,"message":"invalid_login"}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	// session login with a wrong password: one request, ErrInvalidLogin
	c, _ := New(Options{BaseURL: srv.URL, Username: "u", Secret: "bad", InsecureTLS: true})
	c.SetRetryDelays(time.Millisecond, time.Millisecond)
	if err := c.Login(context.Background()); err != ErrInvalidLogin || n.Load() != 1 {
		t.Fatalf("login: %v, attempts %d", err, n.Load())
	}
	// token auth: a 403 on a read is not retried either
	n.Store(0)
	tc, _ := tokenClient(t, srv.URL)
	if _, err := tc.Call(context.Background(), "Quota", "get_quota_info", nil); err != ErrInvalidLogin || n.Load() != 1 {
		t.Fatalf("token read: %v, attempts %d", err, n.Load())
	}
}

func TestLoginNotRepeatedAfterReachingHost(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		dropConn(t, w)
	}))
	defer srv.Close()
	c, _ := New(Options{BaseURL: srv.URL, Username: "u", Secret: "p", InsecureTLS: true})
	c.SetRetryDelays(time.Millisecond, time.Millisecond)
	err := c.Login(context.Background())
	var ne *NetError
	if !errors.As(err, &ne) || !ne.Unknown || n.Load() != 1 {
		t.Fatalf("err %v attempts %d", err, n.Load())
	}
}

func TestWriteAfterSendIsNotRetried(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		dropConn(t, w)
	}))
	defer srv.Close()
	c, msgs := tokenClient(t, srv.URL)
	for name, call := range map[string]func() error{
		"save":   func() error { return c.SaveFile(context.Background(), "/home/u", "a.txt", "x") },
		"upload": func() error { return c.Upload(context.Background(), "/home/u", "a.zip", strings.NewReader("zip")) },
		"mkdir":  func() error { return c.Mkdir(context.Background(), "/home/u/d") },
	} {
		n.Store(0)
		err := call()
		var ne *NetError
		if !errors.As(err, &ne) || !ne.Unknown || n.Load() != 1 {
			t.Fatalf("%s: err %v attempts %d", name, err, n.Load())
		}
		if !strings.Contains(err.Error(), "معلوم نیست روی هاست انجام شد یا نه") {
			t.Fatalf("%s: message %q", name, err)
		}
	}
	if len(*msgs) != 0 {
		t.Fatalf("no retry expected: %q", *msgs)
	}
}

// flakyListener drops the first n connections before the TLS handshake (nothing was sent).
type flakyListener struct {
	net.Listener
	drop atomic.Int32
}

func (l *flakyListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil || l.drop.Add(-1) < 0 {
			return conn, err
		}
		conn.Close()
	}
}

func TestWriteRetriedWhenConnectionNeverEstablished(t *testing.T) {
	var n atomic.Int32
	var body string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		_, hdr, err := r.FormFile("file-1")
		if err == nil {
			f, _ := hdr.Open()
			b := make([]byte, 16)
			k, _ := f.Read(b)
			body = string(b[:k])
		}
		okJSON(w, `{}`)
	}))
	fl := &flakyListener{Listener: srv.Listener}
	fl.drop.Store(2)
	srv.Listener = fl
	srv.StartTLS()
	defer srv.Close()
	c, msgs := tokenClient(t, srv.URL)
	if err := c.Upload(context.Background(), "/home/u", "a.zip", strings.NewReader("zipdata")); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 1 || len(*msgs) != 2 || body != "zipdata" {
		t.Fatalf("handled %d, msgs %q, body %q", n.Load(), *msgs, body)
	}
}

func TestCancelStopsRetries(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c, _ := tokenClient(t, srv.URL)
	c.SetRetryDelays(time.Hour, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	c.OnRetry(func(string) { cancel() })
	start := time.Now()
	_, err := c.Call(ctx, "Quota", "get_quota_info", nil)
	if !errors.Is(err, context.Canceled) || n.Load() != 1 || time.Since(start) > 10*time.Second {
		t.Fatalf("err %v attempts %d", err, n.Load())
	}
}

func TestNetMessages(t *testing.T) {
	cases := map[error]string{
		errors.New(`Post "https://h:2083/x": net/http: TLS handshake timeout`):   "ارتباط با هاست برقرار نشد (زمان اتصال امن تمام شد). اینترنت سرور wpauto و در دسترس بودن هاست را بررسی کنید و دوباره اجرا کنید.",
		&net.DNSError{Err: "no such host", Name: "h", IsNotFound: true}:          "نام هاست پیدا نشد (DNS)",
		&net.OpError{Op: "dial", Err: errors.New("connect: connection refused")}: "هاست اتصال را نپذیرفت",
		errors.New("dial tcp 1.2.3.4:2083: i/o timeout"):                         "هاست در زمان مقرر پاسخ نداد",
		errors.New("read tcp: connection reset by peer"):                         "اتصال با هاست وسط کار قطع شد",
	}
	for err, want := range cases {
		if got := (&NetError{Err: err}).Error(); !strings.HasPrefix(got, want) || !strings.Contains(got, "\nجزئیات فنی: "+err.Error()) {
			t.Errorf("%v → %q", err, got)
		}
	}
	if transient(&net.DNSError{Err: "no such host", IsNotFound: true}) {
		t.Error("NXDOMAIN must not be retried")
	}
	if !transient(errors.New("net/http: TLS handshake timeout")) {
		t.Error("TLS handshake timeout must be retried")
	}
}

func TestIsReadCall(t *testing.T) {
	for _, f := range []string{"get_quota_info", "list_files", "domains_data", "single_domain_data", "php_get_installed_versions", "list", "get_file_content"} {
		if !IsReadCall("M", f) {
			t.Errorf("%s should be a read", f)
		}
	}
	for _, f := range []string{"save_file_content", "upload_files", "create_database", "delete_file", "addsubdomain", "php_set_vhost_versions", "start_autossl_check"} {
		if IsReadCall("M", f) {
			t.Errorf("%s must not be a read", f)
		}
	}
}
