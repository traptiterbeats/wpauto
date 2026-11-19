package wp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPingWaitsForNewVhost(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) < 2 {
			w.WriteHeader(404)
			_, _ = w.Write([]byte("<!DOCTYPE html><html>default page</html>"))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"php":"8.3"}`))
	}))
	defer srv.Close()
	s := &Site{Host: "new.example.test", Path: "/", Addr: strings.TrimPrefix(srv.URL, "http://")}
	waited := false
	out, err := s.Ping(context.Background(), "h.php", "tok", 30*time.Second, func(error) { waited = true })
	if err != nil || out["php"] != "8.3" || s.Scheme != "http" || !waited {
		t.Fatalf("out=%v err=%v scheme=%s waited=%v", out, err, s.Scheme, waited)
	}
	s2 := &Site{Host: "x.example.test", Path: "/", Addr: "127.0.0.1:1"}
	if _, err := s2.Ping(context.Background(), "h.php", "tok", 0, nil); err == nil {
		t.Fatal("unreachable site must fail")
	}
}
