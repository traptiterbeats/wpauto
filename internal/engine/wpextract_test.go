package engine

import (
	"strings"
	"testing"

	"github.com/traptiterbeats/wpauto/internal/cpanel/cpaneltest"
)

const testDocroot = "/home/kfncspot/test.example.com"

func hasCall(calls []string, a string) bool {
	for _, c := range calls {
		if c == a {
			return true
		}
	}
	return false
}

// noTempFiles fails when the helper, its job file, the zip or the extract folder is left behind.
func noTempFiles(t *testing.T, fake *cpaneltest.Fake, dir string) {
	t.Helper()
	for _, f := range fake.Under(dir) {
		if strings.Contains(f, "/.wpauto") || strings.Contains(f, "/wpauto-") {
			t.Errorf("temporary file left behind: %s", f)
		}
	}
}

func TestWPInstallExtractsWithCPanel(t *testing.T) {
	var calls []string
	run, fake := runInstallWith(t, installInputs(), siteOpts{calls: &calls})
	logs := runLogs(run)
	if run.Status != StatusSuccess {
		t.Fatalf("status %s: %s\n%s", run.Status, run.Error, logs)
	}
	if !fake.Called("api2:Fileman/fileop:extract") || hasCall(calls, "extract_chunk") || !hasCall(calls, "place") {
		t.Fatalf("expected cPanel extract + place, helper calls %v", calls)
	}
	if !strings.Contains(logs, "File Manager خود cPanel") {
		t.Fatalf("method not logged:\n%s", logs)
	}
	if _, ok := fake.File(testDocroot + "/wp-load.php"); !ok {
		t.Fatalf("WordPress files not placed in the docroot: %v", fake.Under(testDocroot))
	}
	noTempFiles(t, fake, testDocroot)
}

func TestWPInstallFallsBackToChunks(t *testing.T) {
	var calls []string
	run, fake := runInstallWith(t, installInputs(), siteOpts{calls: &calls, entries: 700,
		setup: func(f *cpaneltest.Fake) { f.Fail["api2:Fileman/fileop:extract"] = true }})
	logs := runLogs(run)
	if run.Status != StatusSuccess {
		t.Fatalf("status %s: %s\n%s", run.Status, run.Error, logs)
	}
	for _, want := range []string{"تکه‌تکه با PHP", "باز کردن فایل‌ها: ۳۰۰ از ۷۰۰", "باز کردن فایل‌ها: ۶۰۰ از ۷۰۰", "باز کردن فایل‌ها: ۷۰۰ از ۷۰۰"} {
		if !strings.Contains(logs, want) {
			t.Errorf("log lacks %q:\n%s", want, logs)
		}
	}
	n := 0
	for _, c := range calls {
		if c == "extract_chunk" {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("extract_chunk called %d times, want 3", n)
	}
	if _, ok := fake.File(testDocroot + "/wp-content/f699.php"); !ok {
		t.Fatal("last chunk not placed")
	}
	noTempFiles(t, fake, testDocroot)
}

func TestWPInstallRedoesIncompleteCPanelExtract(t *testing.T) {
	// cPanel "succeeds" but leaves no wordpress/ folder → the helper extracts it in chunks
	var calls []string
	run, fake := runInstallWith(t, installInputs(), siteOpts{calls: &calls,
		setup: func(f *cpaneltest.Fake) { f.Extracted = []string{"something-else/readme.txt"} }})
	logs := runLogs(run)
	if run.Status != StatusSuccess {
		t.Fatalf("status %s: %s\n%s", run.Status, run.Error, logs)
	}
	if !hasCall(calls, "extract_chunk") || !strings.Contains(logs, "کامل نبود") {
		t.Fatalf("no chunk fallback after incomplete cPanel output: %v\n%s", calls, logs)
	}
	if _, ok := fake.File(testDocroot + "/wp-load.php"); !ok {
		t.Fatal("WordPress files not placed")
	}
}

func TestWPInstallRetriesFailedChunk(t *testing.T) {
	run, _ := runInstallWith(t, installInputs(), siteOpts{chunkFails: map[int]int{300: 2},
		setup: func(f *cpaneltest.Fake) { f.Fail["api2:Fileman/fileop:extract"] = true }})
	logs := runLogs(run)
	if run.Status != StatusSuccess {
		t.Fatalf("status %s: %s\n%s", run.Status, run.Error, logs)
	}
	if strings.Count(logs, "تلاش دوباره") != 2 {
		t.Fatalf("want 2 retries:\n%s", logs)
	}

	// a chunk that keeps failing stops the run after two retries and rolls back
	run, fake := runInstallWith(t, installInputs(), siteOpts{chunkFails: map[int]int{300: 3},
		setup: func(f *cpaneltest.Fake) { f.Fail["api2:Fileman/fileop:extract"] = true }})
	if run.Status != StatusFailed || !strings.Contains(run.Error, "باز کردن فایل‌های وردپرس") {
		t.Fatalf("expected failure, got %s: %s", run.Status, run.Error)
	}
	if !fake.Called("api2:SubDomain/delsubdomain") {
		t.Fatal("no rollback")
	}
	if left := fake.Under(testDocroot); len(left) > 0 {
		t.Fatalf("docroot not removed on rollback: %v", left)
	}
}

func TestWPInstallRollbackVerifiesDocrootRemoval(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*cpaneltest.Fake)
		warn  bool
	}{
		{"recursive unlink", nil, false},
		// unlink cannot delete a folder with files and there is no delete_file: emptied bottom-up
		{"shallow unlink", func(f *cpaneltest.Fake) { f.ShallowUnlink = true; f.Fail["Fileman/delete_file"] = true }, false},
		// nothing can be deleted: the run says which path is left
		{"cannot delete", func(f *cpaneltest.Fake) {
			f.Fail["api2:Fileman/fileop:unlink"] = true
			f.Fail["Fileman/delete_file"] = true
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run, fake := runInstallWith(t, installInputs(), siteOpts{failInstall: true, setup: tc.setup})
			logs := runLogs(run)
			if run.Status != StatusFailed {
				t.Fatalf("status %s", run.Status)
			}
			left := fake.Under(testDocroot)
			warned := strings.Contains(logs, "warn پوشه‌ی ساب‌دامین "+testDocroot+" کامل پاک نشد")
			if tc.warn {
				if len(left) == 0 || !warned {
					t.Fatalf("expected leftovers and a warning with the path:\n%s", logs)
				}
				return
			}
			if len(left) > 0 || warned {
				t.Fatalf("docroot not removed: %v\n%s", left, logs)
			}
		})
	}
}
