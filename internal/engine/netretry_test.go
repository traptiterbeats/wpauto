package engine

import (
	"strings"
	"testing"

	"github.com/traptiterbeats/wpauto/internal/cpanel/cpaneltest"
)

// wp.locate falls back to single_domain_data when the domain list cannot be read.
func TestLocateFallsBackToSingleDomain(t *testing.T) {
	t.Setenv("WPAUTO_DIR", t.TempDir())
	fake := cpaneltest.New(t, "kfncspot", "secret")
	t.Cleanup(fake.Close)
	fake.Fail["DomainInfo/domains_data"] = true
	wpf := &fakeWP{plugins: map[string]any{}}
	site := wpf.server(t, fake)
	t.Cleanup(site.Close)
	r, _ := NewRunner(t.TempDir())
	list, err := LoadWorkflows("")
	if err != nil {
		t.Fatal(err)
	}
	wf, _ := FindWorkflow(list, "plugins-status")
	rej := 0
	tg := target(t, fake.URL, "secret", &rej)
	tg.Profile.Quirks = map[string]string{"site_addr": strings.TrimPrefix(site.URL, "http://")}
	id, err := r.Start(wf, tg, map[string]string{"domain": "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	run := wait(t, r, id)
	if run.Status != StatusSuccess {
		t.Fatalf("status %s: %s", run.Status, run.Error)
	}
	if !strings.Contains(runLog(run), "فهرست دامنه‌های هاست خوانده نشد؛ مسیر سایت از اطلاعات همین دامنه گرفته شد.") {
		t.Fatalf("no fallback warning:\n%s", runLog(run))
	}
	if got := stepOut(run, "locate", "docroot"); got != "/home/kfncspot/public_html" {
		t.Fatalf("docroot %q", got)
	}
}
