package engine

import (
	"strings"
	"testing"

	"github.com/traptiterbeats/wpauto/internal/catalog"
	"github.com/traptiterbeats/wpauto/internal/catalog/catalogtest"
	"github.com/traptiterbeats/wpauto/internal/cpanel/cpaneltest"
)

const imp = catalog.ImporterSlug

// importerSetup puts importer libVer in the library and adds mytheme whose demo needs min
// (and requires the given wordpress.org plugins).
func importerSetup(t *testing.T, libVer, min string) func(cat *catalog.Catalog, src string) {
	return func(cat *catalog.Catalog, src string) {
		if _, err := cat.AddPlugin(catalogtest.PluginVersion(t, src, imp, libVer)); err != nil {
			t.Fatal(err)
		}
		if _, err := cat.Add(catalog.AddInput{ThemeZip: catalogtest.Theme(t, src, "mytheme"), DemoZip: catalogtest.DemoMinImporter(t, src, "mytheme", min)}); err != nil {
			t.Fatal(err)
		}
	}
}

func siteWith(failAt, ver string, active bool) *fakeWP {
	return &fakeWP{failAt: failAt, plugins: map[string]any{imp: map[string]any{"version": ver, "active": active}}}
}

func mustLog(t *testing.T, run Run, line string) {
	t.Helper()
	if !strings.Contains(runLog(run), line) {
		t.Fatalf("log line missing: %q\n%s", line, runLog(run))
	}
}

func TestPluginUpgradedFromLibrary(t *testing.T) {
	run, wpf, _ := runThemeInstallWP(t, siteWith("", "1.2.0", true), importerSetup(t, "1.3.0", "1.3.0"), nil)
	if run.Status != StatusSuccess {
		t.Fatalf("status %s: %s\n%s", run.Status, run.Error, runLog(run))
	}
	want := "ping wp_info plugin_upgrade:" + imp + " plugin_activate:" + imp + " theme_install:mytheme theme_activate:mytheme demo_prepare:mytheme"
	if got := wpf.seen(); !strings.HasPrefix(got, want) || !strings.Contains(got, "plugin_backup_remove") || strings.Contains(got, "plugin_restore") || strings.Contains(got, "plugin_install:"+imp) {
		t.Fatalf("actions:\n got %s\nwant prefix %s", got, want)
	}
	mustLog(t, run, "افزونه‌ی "+imp+" از نسخه‌ی 1.2.0 به 1.3.0 به‌روز شد (نسخه‌ی قبلی پشتیبان گرفته شد)")
	mustLog(t, run, "افزونه‌ی "+imp+" از قبل فعال است")
	mustLog(t, run, "حذف نسخه‌ی پشتیبان افزونه‌ی "+imp)
	if len(wpf.backups) == 0 || !strings.HasPrefix(wpf.backups[0], "wpauto-backup-"+imp+"-1.2.0-") {
		t.Fatalf("backup names %v", wpf.backups)
	}
}

func TestPluginUpgradeUndoRestoresOldVersion(t *testing.T) {
	run, wpf, _ := runThemeInstallWP(t, siteWith("demo_step", "1.2.0", true), importerSetup(t, "1.3.0", "1.3.0"), nil)
	if run.Status != StatusFailed {
		t.Fatalf("status %s", run.Status)
	}
	got := wpf.seen()
	if !strings.Contains(got, "plugin_restore:"+imp+":active") || strings.Contains(got, "plugin_backup_remove") {
		t.Fatalf("actions: %s", got)
	}
	// restore comes after the theme was switched back (reverse order of the run)
	if strings.Index(got, "plugin_restore") < strings.LastIndex(got, "theme_activate") {
		t.Fatalf("restore ran too early: %s", got)
	}
	mustLog(t, run, "✔ برگرداندن افزونه‌ی "+imp+" به نسخه‌ی 1.2.0")
	if len(wpf.backups) < 2 || wpf.backups[0] != wpf.backups[1] {
		t.Fatalf("restore must use the upgrade's backup: %v", wpf.backups)
	}
}

func TestPluginUpgradeInactiveStaysRestoredInactive(t *testing.T) {
	run, wpf, _ := runThemeInstallWP(t, siteWith("demo_step", "1.2.0", false), importerSetup(t, "1.3.0", ""), nil)
	if run.Status != StatusFailed {
		t.Fatalf("status %s", run.Status)
	}
	got := wpf.seen()
	if !strings.Contains(got, "plugin_restore:"+imp+" ") || strings.Contains(got, "plugin_restore:"+imp+":active") {
		t.Fatalf("actions: %s", got)
	}
	// the activation of this run is undone first, then the old version comes back
	if i, j := strings.Index(got, "plugin_remove:"+imp), strings.Index(got, "plugin_restore"); i < 0 || i > j {
		t.Fatalf("deactivate before restore: %s", got)
	}
}

func TestPluginNeverDowngradedOrUpgradedWhenEqual(t *testing.T) {
	for _, site := range []string{"1.4.0", "1.3.0"} {
		run, wpf, _ := runThemeInstallWP(t, siteWith("", site, true), importerSetup(t, "1.3.0", "1.3.0"), nil)
		if run.Status != StatusSuccess {
			t.Fatalf("%s: status %s: %s", site, run.Status, run.Error)
		}
		if got := wpf.seen(); strings.Contains(got, "plugin_upgrade") || strings.Contains(got, "plugin_install:"+imp) {
			t.Fatalf("%s: actions %s", site, got)
		}
		mustLog(t, run, "افزونه‌ی "+imp+" از قبل نصب است (نسخه‌ی "+site+")")
	}
}

// A wordpress.org plugin that is already installed is left as it is (no library zip).
func TestWPOrgPluginNotUpgraded(t *testing.T) {
	wpf := &fakeWP{plugins: map[string]any{"woocommerce": map[string]any{"version": "1.0", "active": true}}}
	run, wpf, _ := runThemeInstallWP(t, wpf, func(cat *catalog.Catalog, src string) {
		if _, err := cat.Add(catalog.AddInput{ThemeZip: catalogtest.Theme(t, src, "mytheme"), DemoZip: catalogtest.Demo(t, src, "mytheme", "woocommerce")}); err != nil {
			t.Fatal(err)
		}
	}, nil)
	if run.Status != StatusSuccess {
		t.Fatalf("status %s: %s", run.Status, run.Error)
	}
	if got := wpf.seen(); strings.Contains(got, "plugin_upgrade") || strings.Contains(got, "plugin_fetch") {
		t.Fatalf("actions %s", got)
	}
	mustLog(t, run, "افزونه‌ی woocommerce از قبل نصب است (نسخه‌ی 1.0)")
}

const importerTooOld = "دموی این قالب به «درون‌ریز دمو سایه» نسخه‌ی 1.3.0 یا بالاتر نیاز دارد؛ روی سایت نسخه‌ی 1.2.0 نصب است و کتابخانه‌ی پنل نسخه‌ی 1.2.0 را دارد. zip نسخه‌ی 1.3.0 یا بالاتر را در «مدیریت قالب‌ها» (کتابخانه‌ی افزونه‌ها) بارگذاری کنید و دوباره اجرا کنید. چیزی روی سایت تغییر نکرد."

// Neither the site nor the library has importer >= 1.3.0: fail before anything is written.
func TestImporterTooOldFailsBeforeTouchingSite(t *testing.T) {
	run, wpf, fake := runThemeInstallWP(t, siteWith("", "1.2.0", true), importerSetup(t, "1.2.0", "1.3.0"), func(f *cpaneltest.Fake) {
		f.Put("/home/kfncspot/public_html/wp-content/plugins/"+imp+"/"+imp+".php", "<?php\n/**\n * Plugin Name: Saayeh\n * Version: 1.2.0\n */\n")
	})
	if run.Status != StatusFailed || !strings.Contains(run.Error, importerTooOld) || !strings.HasPrefix(run.Error, "پیش‌بررسی قالب و دمو: ") {
		t.Fatalf("status %s: %s", run.Status, run.Error)
	}
	if got := wpf.seen(); got != "" {
		t.Fatalf("site was touched: %s", got)
	}
	if fake.Called("Fileman/save_file_content") || fake.Called("Fileman/upload_files") {
		t.Fatal("cPanel writes before the check")
	}
}

// Not installed on the site and the library is too old.
func TestImporterMissingAndLibraryTooOld(t *testing.T) {
	run, wpf, _ := runThemeInstallWP(t, &fakeWP{}, importerSetup(t, "1.1.0", "1.3.0"), func(f *cpaneltest.Fake) {
		f.Put("/home/kfncspot/public_html/wp-content/plugins/akismet/akismet.php", "<?php // Version: 5")
	})
	if run.Status != StatusFailed || !strings.Contains(run.Error, "روی سایت نصب نیست و کتابخانه‌ی پنل نسخه‌ی 1.1.0 را دارد") {
		t.Fatalf("status %s: %s", run.Status, run.Error)
	}
	if wpf.seen() != "" {
		t.Fatalf("site was touched: %s", wpf.seen())
	}
}

// cPanel cannot read the site's plugins folder: WordPress' own list decides at the start of the
// plugins step, before any plugin or theme is installed.
func TestImporterCheckFallsBackToWordPressList(t *testing.T) {
	run, wpf, _ := runThemeInstallWP(t, siteWith("", "1.2.0", true), importerSetup(t, "1.2.0", "1.3.0"), nil)
	if run.Status != StatusFailed || !strings.Contains(run.Error, importerTooOld) || !strings.HasPrefix(run.Error, "نصب و فعال‌سازی افزونه‌ها: ") {
		t.Fatalf("status %s: %s", run.Status, run.Error)
	}
	if got := wpf.seen(); got != "ping wp_info cleanup" {
		t.Fatalf("actions: %s", got)
	}
}

// The library can provide it: the check passes and the plugin step upgrades.
func TestImporterCheckPassesWithNewerLibrary(t *testing.T) {
	run, _, _ := runThemeInstallWP(t, siteWith("", "1.2.0", true), importerSetup(t, "1.3.0", "1.3.0"), func(f *cpaneltest.Fake) {
		f.Put("/home/kfncspot/public_html/wp-content/plugins/"+imp+"/"+imp+".php", "<?php\n/**\n * Version: 1.2.0\n */\n")
	})
	if run.Status != StatusSuccess {
		t.Fatalf("status %s: %s", run.Status, run.Error)
	}
	mustLog(t, run, "نسخه‌ی درون‌ریز برای این دمو کافی است (لازم: 1.3.0 یا بالاتر)")
}
