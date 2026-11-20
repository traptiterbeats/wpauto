package wp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestHelperRetriesMaintenance503(t *testing.T) {
	old := BusyWaits
	BusyWaits = []time.Duration{time.Millisecond, time.Millisecond}
	defer func() { BusyWaits = old }()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(503)
			w.Write([]byte("<!DOCTYPE html><html dir='rtl'>maintenance"))
			return
		}
		w.Write([]byte(`{"ok":true,"state":{}}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	busy := 0
	s := &Site{Host: u.Host, Path: "/", Scheme: "http", OnBusy: func(int, time.Duration) { busy++ }}
	if _, err := s.HelperArgs(context.Background(), "h.php", "tok", "demo_start", nil); err != nil {
		t.Fatal(err)
	}
	if n != 2 || busy != 1 {
		t.Fatalf("calls=%d busy=%d", n, busy)
	}
}
