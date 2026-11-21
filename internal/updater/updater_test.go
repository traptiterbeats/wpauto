package updater

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchProgress(t *testing.T) {
	body := strings.Repeat("x", 300000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "300000")
		w.Write([]byte(body))
	}))
	defer srv.Close()
	var out bytes.Buffer
	got, err := fetchProgress(srv.URL, &out)
	if err != nil || len(got) != len(body) {
		t.Fatalf("got %d bytes, err %v", len(got), err)
	}
	if !strings.Contains(out.String(), "100%") {
		t.Fatalf("no 100%% in progress: %q", out.String())
	}
}

func TestFetchStall(t *testing.T) {
	old := stallTimeout
	stallTimeout = 200 * time.Millisecond
	defer func() { stallTimeout = old }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.Write([]byte("abc"))
		w.(http.Flusher).Flush()
		time.Sleep(time.Second)
	}))
	defer srv.Close()
	if _, err := fetchProgress(srv.URL, nil); err == nil || !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("expected stall error, got %v", err)
	}
}
