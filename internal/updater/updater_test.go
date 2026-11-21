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

func TestCompareVersions(t *testing.T) {
	order := []string{"v0.14.0", "v0.14.1", "v0.15.0-alpha", "v0.15.0-beta.1", "v0.15.0-beta.2", "v0.15.0-beta.10", "v0.15.0-rc.1", "v0.15.0", "0.15.1", "v1.0.0"}
	for i := range order {
		for j := range order {
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := CompareVersions(order[i], order[j]); got != want {
				t.Errorf("CompareVersions(%s, %s) = %d, want %d", order[i], order[j], got, want)
			}
		}
	}
	if CompareVersions("v1.2", "1.2.0") != 0 {
		t.Error("missing patch should be 0")
	}
}

func TestChannels(t *testing.T) {
	releases := `[{"tag_name":"v0.16.0-beta.1","draft":true,"prerelease":true},
		{"tag_name":"v0.15.0-beta.1","prerelease":true},{"tag_name":"v0.14.1"},{"tag_name":"v0.14.0"}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/" + Repo + "/releases/latest":
			w.Write([]byte(`{"tag_name":"v0.14.1"}`)) // GitHub never returns a pre-release here
		case "/repos/" + Repo + "/releases":
			w.Write([]byte(releases))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := apiBase
	apiBase = srv.URL
	defer func() { apiBase = old }()
	if tag, err := Latest(); err != nil || tag != "v0.14.1" {
		t.Fatalf("stable: %s %v", tag, err)
	}
	if tag, err := LatestBeta(); err != nil || tag != "v0.15.0-beta.1" {
		t.Fatalf("beta: %s %v (drafts must be skipped)", tag, err)
	}
	// once the stable release is out, the beta channel moves on to it
	releases = `[{"tag_name":"v0.15.0"},{"tag_name":"v0.15.0-beta.1","prerelease":true}]`
	if tag, _ := LatestBeta(); tag != "v0.15.0" {
		t.Fatalf("beta after stable: %s", tag)
	}
}
