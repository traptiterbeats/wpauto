package engine

import (
	"strings"
	"testing"

	"github.com/traptiterbeats/wpauto/internal/cpanel/cpaneltest"
)

func TestWPConfigDB(t *testing.T) {
	n, u := wpConfigDB("<?php\ndefine( 'DB_NAME', 'acc_wp1' );\ndefine(\"DB_USER\",\"acc_u1\");\ndefine('DB_PASSWORD', 'x');")
	if n != "acc_wp1" || u != "acc_u1" {
		t.Fatalf("%q %q", n, u)
	}
}

func TestScanListsAndRemove(t *testing.T) {
	srv := cpaneltest.New(t, "kfncspot", "secret")
	defer srv.Close()
	r, _ := NewRunner(t.TempDir())
	list, _ := LoadWorkflows("")
	scan, ok := FindWorkflow(list, "wp-scan")
	if !ok || !scan.Hidden {
		t.Fatal("wp-scan missing or not hidden")
	}
	rej := 0
	id, _ := r.Start(scan, target(t, srv.URL, "secret", &rej), map[string]string{})
	run := wait(t, r, id)
	if run.Status != StatusSuccess || !strings.Contains(run.Steps[1].Outputs["list"], `/home/kfncspot/public_html/shop`) {
		t.Fatalf("scan: %s %v", run.Status, run.Steps[1].Outputs)
	}
	rm, _ := FindWorkflow(list, "wp-remove")
	id, err := r.Start(rm, target(t, srv.URL, "secret", &rej), map[string]string{"sites": "/home/kfncspot/public_html/shop\n/home/kfncspot/public_html"})
	if err != nil {
		t.Fatal(err)
	}
	run = wait(t, r, id)
	full, _ := r.Get(id, 0)
	all := ""
	for _, l := range full.Log {
		all += l.Msg + "\n"
	}
	if run.Status != StatusSuccess {
		t.Fatalf("remove %s:\n%s", run.Status, all)
	}
	for _, want := range []string{"پوشه‌ی /home/kfncspot/public_html/shop حذف شد", "دیتابیس kfncspot_shop حذف شد", "دامنه‌ی example.com (main) خودش حذف نمی‌شود", "فایل‌های وردپرس از /home/kfncspot/public_html حذف شد", "دیتابیس kfncspot_public_html حذف شد"} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %q in:\n%s", want, all)
		}
	}
	if strings.Contains(strings.Join(srv.Calls, ","), "delsubdomain") {
		t.Fatal("main domain must not delete a subdomain")
	}
	// a path outside the account is refused
	id, _ = r.Start(rm, target(t, srv.URL, "secret", &rej), map[string]string{"sites": "/etc"})
	if run = wait(t, r, id); run.Status == StatusSuccess {
		t.Fatal("outside path accepted")
	}
}
