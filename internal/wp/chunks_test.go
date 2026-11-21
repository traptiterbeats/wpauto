package wp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestExtractChunks(t *testing.T) {
	old := ChunkRetryWaits
	ChunkRetryWaits = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { ChunkRetryWaits = old })
	const total = 650
	run := func(fails map[int]int, stuck bool) ([]int, int, error) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if q.Get("a") != "extract_chunk" || q.Get("t") != "tok" {
				w.WriteHeader(400)
				return
			}
			from, _ := strconv.Atoi(q.Get("from"))
			n, _ := strconv.Atoi(q.Get("n"))
			if fails[from] > 0 {
				fails[from]--
				w.WriteHeader(502)
				_, _ = w.Write([]byte("<html>bad gateway</html>"))
				return
			}
			next := min(total, from+n)
			if stuck {
				next = from
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "next": next, "total": total, "done": next >= total})
		}))
		defer srv.Close()
		s := &Site{Host: "example.com", Path: "/", Addr: strings.TrimPrefix(srv.URL, "http://"), Scheme: "http"}
		var prog []int
		retries := 0
		err := s.ExtractChunks(context.Background(), "wpauto-x.php", "tok", func(done, tot int) {
			if tot != total {
				t.Errorf("total %d", tot)
			}
			prog = append(prog, done)
		}, func(from, attempt int, err error) { retries++ })
		return prog, retries, err
	}
	prog, retries, err := run(map[int]int{}, false)
	if err != nil || len(prog) != 3 || prog[0] != 300 || prog[2] != 650 || retries != 0 {
		t.Fatalf("plain: %v %d %v", prog, retries, err)
	}
	prog, retries, err = run(map[int]int{300: 2}, false)
	if err != nil || len(prog) != 3 || retries != 2 {
		t.Fatalf("two failures must be retried: %v %d %v", prog, retries, err)
	}
	_, retries, err = run(map[int]int{300: 3}, false)
	if err == nil || retries != 2 || !strings.Contains(err.Error(), "from 300") {
		t.Fatalf("three failures must stop: %d %v", retries, err)
	}
	_, _, err = run(map[int]int{}, true)
	if err == nil || !strings.Contains(err.Error(), "no progress") {
		t.Fatalf("a chunk that does not advance must stop: %v", err)
	}
}
