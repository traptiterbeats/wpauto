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

// removeSite runs wp-remove for one project address (site mode) and returns the run and its log.
func removeSite(t *testing.T, srv *cpaneltest.Fake, in map[string]string) (Run, string) {
	t.Helper()
	r, _ := NewRunner(t.TempDir())
	list, _ := LoadWorkflows("")
	rm, _ := FindWorkflow(list, "wp-remove")
	rej := 0
	id, err := r.Start(rm, target(t, srv.URL, "secret", &rej), in)
	if err != nil {
		t.Fatal(err)
	}
	run := wait(t, r, id)
	full, _ := r.Get(id, 0)
	all := ""
	for _, l := range full.Log {
		all += l.Msg + "\n"
	}
	return run, all
}

func TestRemoveProjectSite(t *testing.T) {
	// a folder site: the whole folder (WordPress only) and its database go, nothing else
	srv := cpaneltest.New(t, "kfncspot", "secret")
	defer srv.Close()
	srv.QuotaMB = []float64{300, 250}
	run, log := removeSite(t, srv, map[string]string{"site": "example.com/shop"})
	if run.Status != StatusSuccess {
		t.Fatalf("folder site: %s\n%s", run.Status, log)
	}
	for _, want := range []string{"پوشه‌ی /home/kfncspot/public_html/shop حذف شد", "دیتابیس kfncspot_shop حذف شد"} {
		if !strings.Contains(log, want) {
			t.Errorf("missing %q in:\n%s", want, log)
		}
	}
	if strings.Contains(log, "kfncspot_public_html") || strings.Contains(strings.Join(srv.Calls, ","), "delsubdomain") {
		t.Error("the main site was touched")
	}
	out := run.Steps[1].Outputs
	if out["disk_before"] != "300.00" || out["disk_after"] != "250.00" || out["disk_limit"] != "1024.00" {
		t.Errorf("disk outputs: %v", out)
	}

	// no WordPress at the address: refused, nothing deleted
	srv2 := cpaneltest.New(t, "kfncspot", "secret")
	defer srv2.Close()
	run, log = removeSite(t, srv2, map[string]string{"site": "blog.example.com", "own_subs": "blog.example.com"})
	if run.Status == StatusSuccess || !strings.Contains(run.Error+log, "وردپرسی پیدا نشد") {
		t.Fatalf("empty site: %s %s", run.Status, run.Error)
	}
	for _, c := range srv2.CallList() {
		if strings.Contains(c, "delete") || strings.Contains(c, "delsubdomain") || strings.Contains(c, "fileop") {
			t.Fatalf("something was deleted: %v", srv2.Calls)
		}
	}
	// an address not on the account
	run, _ = removeSite(t, srv2, map[string]string{"site": "other.test"})
	if run.Status == StatusSuccess || !strings.Contains(run.Error, "روی این حساب cPanel نیست") {
		t.Fatalf("foreign domain: %s %s", run.Status, run.Error)
	}

	// a subdomain site wpauto did NOT create: subdomain and folder stay, only WordPress files go
	srv3 := cpaneltest.New(t, "kfncspot", "secret")
	defer srv3.Close()
	for _, f := range []string{"wp-config.php", "wp-includes/version.php", "index.php", "keep.html"} {
		srv3.Put("/home/kfncspot/blog/"+f, "x")
	}
	run, log = removeSite(t, srv3, map[string]string{"site": "blog.example.com"})
	if run.Status != StatusSuccess {
		t.Fatalf("foreign sub: %s\n%s", run.Status, log)
	}
	if srv3.Called("api2:SubDomain/delsubdomain") || !strings.Contains(log, "را wpauto نساخته") || !strings.Contains(log, "فایل‌های وردپرس از /home/kfncspot/blog حذف شد") {
		t.Fatalf("foreign subdomain handled wrong:\n%s", log)
	}
	if _, ok := srv3.File("/home/kfncspot/blog/keep.html"); !ok {
		t.Error("a non-WordPress file in the subdomain was deleted")
	}

	// the same subdomain created by wpauto: subdomain + whole folder + database go
	srv4 := cpaneltest.New(t, "kfncspot", "secret")
	defer srv4.Close()
	for _, f := range []string{"wp-config.php", "wp-includes/version.php", "index.php"} {
		srv4.Put("/home/kfncspot/blog/"+f, "x")
	}
	run, log = removeSite(t, srv4, map[string]string{"site": "blog.example.com", "own_subs": "Blog.Example.com"})
	if run.Status != StatusSuccess || !srv4.Called("api2:SubDomain/delsubdomain") || !strings.Contains(log, "پوشه‌ی /home/kfncspot/blog حذف شد") ||
		!strings.Contains(log, "دیتابیس kfncspot_blog حذف شد") {
		t.Fatalf("own subdomain: %s\n%s", run.Status, log)
	}
	if len(srv4.Under("/home/kfncspot/blog")) != 0 {
		t.Error("own subdomain folder left behind")
	}
}
