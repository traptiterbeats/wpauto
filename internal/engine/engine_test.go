package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/traptiterbeats/wpauto/internal/cpanel"
	"github.com/traptiterbeats/wpauto/internal/cpanel/cpaneltest"
	"github.com/traptiterbeats/wpauto/internal/profile"
)

func target(t *testing.T, url, pass string, rejected *int) *Target {
	profs, _ := profile.LoadAll("")
	p, _ := profile.Find(profs, "generic")
	cp := *p
	cp.InsecureTLS = true
	return &Target{HostID: "h1", HostName: "test", Profile: &cp,
		Options:         cpanel.Options{BaseURL: url, Username: "kfncspot", Secret: pass, InsecureTLS: true},
		OnLoginRejected: func() { *rejected++ }}
}

func wait(t *testing.T, r *Runner, id string) Run {
	t.Helper()
	for i := 0; i < 200; i++ {
		run, _ := r.Get(id, 0)
		if run.Done() {
			return run
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("run did not finish")
	return Run{}
}

func TestBuiltinWorkflowsLoad(t *testing.T) {
	list, err := LoadWorkflows("")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := FindWorkflow(list, "host-check"); !ok {
		t.Fatal("host-check missing")
	}
}

func TestHostCheckFindsWordPress(t *testing.T) {
	srv := cpaneltest.New(t, "kfncspot", "secret")
	defer srv.Close()
	r, _ := NewRunner(t.TempDir())
	list, _ := LoadWorkflows("")
	wf, _ := FindWorkflow(list, "host-check")
	rej := 0
	var gotReport *cpanel.Report
	tg := target(t, srv.URL, "secret", &rej)
	tg.OnReport = func(rep *cpanel.Report) { gotReport = rep }
	id, err := r.Start(wf, tg, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	run := wait(t, r, id)
	if run.Status != StatusSuccess {
		t.Fatalf("status %s: %s", run.Status, run.Error)
	}
	if run.Steps[2].Outputs["count"] != "1" || run.Steps[2].Outputs["domains"] != "example.com" {
		t.Fatalf("detect outputs: %v", run.Steps[2].Outputs)
	}
	if gotReport == nil || !gotReport.ReadyForWP {
		t.Fatal("report not delivered")
	}
	all := ""
	for _, l := range run.Log {
		all += l.Msg + "\n"
	}
	if !strings.Contains(all, "6.8.3") {
		t.Fatalf("version not logged:\n%s", all)
	}
}

func TestInputsTemplatingAndRequired(t *testing.T) {
	srv := cpaneltest.New(t, "kfncspot", "secret")
	defer srv.Close()
	r, _ := NewRunner(t.TempDir())
	list, _ := LoadWorkflows("")
	wf, _ := FindWorkflow(list, "wp-check-domain")
	rej := 0
	if _, err := r.Start(wf, target(t, srv.URL, "secret", &rej), map[string]string{}); err == nil {
		t.Fatal("missing required input accepted")
	}
	id, err := r.Start(wf, target(t, srv.URL, "secret", &rej), map[string]string{"domain": "blog.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	run := wait(t, r, id)
	if run.Status != StatusSuccess || run.Steps[1].Outputs["count"] != "0" {
		t.Fatalf("run: %s %v", run.Status, run.Steps)
	}
	last := run.Log[len(run.Log)-2].Msg
	if !strings.Contains(last, "blog.example.com: 0") {
		t.Fatalf("templated log line wrong: %q", last)
	}
}

func TestRejectedLoginStopsAndLocks(t *testing.T) {
	srv := cpaneltest.New(t, "kfncspot", "secret")
	defer srv.Close()
	r, _ := NewRunner(t.TempDir())
	list, _ := LoadWorkflows("")
	wf, _ := FindWorkflow(list, "host-check")
	rej := 0
	id, _ := r.Start(wf, target(t, srv.URL, "bad", &rej), map[string]string{})
	run := wait(t, r, id)
	if run.Status != StatusFailed || rej != 1 {
		t.Fatalf("status %s rejected %d", run.Status, rej)
	}
	if run.Steps[1].Status != StatusSkipped || run.Steps[2].Status != StatusSkipped {
		t.Fatalf("later steps should be skipped: %v", run.Steps)
	}
}

func TestUAPIStepIsReadOnly(t *testing.T) {
	_, err := ParseWorkflow([]byte("id: x\nsteps:\n  - id: a\n    uses: nope\n"), "t")
	if err == nil {
		t.Fatal("unknown step type accepted")
	}
	if readOnlyUAPI.MatchString("delete_database") || !readOnlyUAPI.MatchString("list_files") {
		t.Fatal("read-only allow-list wrong")
	}
}
