package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/traptiterbeats/wpauto/internal/catalog"
)

func init() {
	Register("theme.check", "checks before touching the site that the theme's demo can be imported (importer version), read-only", stepThemeCheck)
}

// libraryVersion is the version in a library zip's plugin header ("" when unknown).
func libraryVersion(lib string) string {
	if lib == "" {
		return ""
	}
	info, err := catalog.InspectPlugin(lib)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(info.Version)
}

// newer reports whether version a is a real version higher than b.
func newer(a, b string) bool {
	return catalog.ValidVersion(a) && catalog.ValidVersion(b) && catalog.CompareVersions(a, b) > 0
}

// atLeast reports whether version v is known and >= min.
func atLeast(v, min string) bool {
	return catalog.ValidVersion(v) && catalog.CompareVersions(v, min) >= 0
}

// demoMinImporter is the Saayeh Demo Importer version the theme's demo needs ("" = any).
func demoMinImporter(t *catalog.Theme) string {
	if !t.HasDemo() {
		return ""
	}
	di, err := catalog.InspectDemo(t.Path(t.DemoZip))
	if err != nil {
		return ""
	}
	return di.MinImporter
}

// importerCheck returns an error when the demo needs importer >= min and neither the site
// (siteVer, "" = not installed, "?" = unknown) nor the panel library can provide it.
func importerCheck(t *catalog.Theme, siteVer string) error {
	min := demoMinImporter(t)
	if min == "" || !catalog.ValidVersion(min) {
		return nil
	}
	if siteVer == "?" || atLeast(siteVer, min) {
		return nil
	}
	libVer := libraryVersion(Catalog().LibraryPath(catalog.ImporterSlug))
	if atLeast(libVer, min) {
		return nil
	}
	site := "روی سایت نصب نیست"
	if siteVer != "" {
		site = "روی سایت نسخه‌ی " + siteVer + " نصب است"
	}
	lib := "در کتابخانه‌ی پنل نیست"
	if libVer != "" {
		lib = "کتابخانه‌ی پنل نسخه‌ی " + libVer + " را دارد"
	} else if Catalog().LibraryPath(catalog.ImporterSlug) != "" {
		lib = "نسخه‌ی zip کتابخانه‌ی پنل معلوم نیست"
	}
	return fmt.Errorf("دموی این قالب به «درون‌ریز دمو سایه» نسخه‌ی %s یا بالاتر نیاز دارد؛ %s و %s. zip نسخه‌ی %s یا بالاتر را در «مدیریت قالب‌ها» (کتابخانه‌ی افزونه‌ها) بارگذاری کنید و دوباره اجرا کنید. چیزی روی سایت تغییر نکرد.",
		min, site, lib, min)
}

var pluginVersionRe = regexp.MustCompile(`(?mi)^[ \t/*#@]*Version:\s*(\S+)`)

// stepThemeCheck runs right after wp.locate, before anything is written to the site: with the
// demo selected, the importer version the demo needs must be on the site or in the library.
// The site's version is read with cPanel from the plugin's main file; when it cannot be read,
// the check is repeated with WordPress' own list at the start of theme.plugins.
func stepThemeCheck(ctx context.Context, rc *RunContext, a map[string]string) (map[string]string, error) {
	t, err := rc.catalogTheme(a["theme"])
	if err != nil {
		return nil, err
	}
	if a["import"] != "true" || demoMinImporter(t) == "" {
		return nil, nil
	}
	c, err := rc.CPanel(ctx)
	if err != nil {
		return nil, err
	}
	siteVer := "?"
	dir := path.Join(a["docroot"], "wp-content", "plugins", catalog.ImporterSlug)
	if list, err := c.List(ctx, path.Dir(dir)); err == nil {
		siteVer = ""
		for _, e := range list {
			if e.Name == catalog.ImporterSlug && e.Dir {
				siteVer = "?"
				raw, err := c.Call(ctx, "Fileman", "get_file_content", url.Values{"dir": {dir}, "file": {catalog.ImporterSlug + ".php"}})
				var fc struct {
					Content string `json:"content"`
				}
				if err == nil && json.Unmarshal(raw, &fc) == nil {
					if m := pluginVersionRe.FindStringSubmatch(fc.Content); m != nil {
						siteVer = strings.Trim(m[1], `\"',`)
					}
				}
			}
		}
	}
	if err := importerCheck(t, siteVer); err != nil {
		return nil, err
	}
	rc.Logf("نسخه‌ی درون‌ریز برای این دمو کافی است (لازم: %s یا بالاتر)", demoMinImporter(t))
	return nil, nil
}

var backupUnsafe = regexp.MustCompile(`[^a-z0-9.]+`)

// backupName is the folder under wp-content/upgrade that keeps the old version of a plugin.
func backupName(slug, ver string) string {
	v := strings.Trim(backupUnsafe.ReplaceAllString(strings.ToLower(ver), "_"), "._")
	v = strings.ReplaceAll(v, "..", ".")
	if len(v) > 20 {
		v = v[:20]
	}
	if len(slug) > 30 {
		slug = slug[:30]
	}
	return "wpauto-backup-" + slug + "-" + v + "-" + time.Now().Format("20060102-150405")
}

// upgradePlugin replaces an installed plugin with the newer library zip. The old folder is kept
// in wp-content/upgrade; rollback puts it back (active again when it was), a successful run
// removes it at the end.
func (rc *RunContext) upgradePlugin(ctx context.Context, slug, lib, oldVer, newVer string) error {
	if err := rc.upload(ctx, lib, slug+".zip"); err != nil {
		return err
	}
	b := backupName(slug, oldVer)
	res, err := rc.helperCall(ctx, "plugin_upgrade", url.Values{"slug": {slug}, "f": {slug + ".zip"}, "b": {b}})
	if err != nil {
		return fmt.Errorf("به‌روزرسانی افزونه‌ی %s به نسخه‌ی %s: %w", slug, newVer, err)
	}
	wasActive, _ := res["was_active"].(bool)
	backup := fmt.Sprint(res["backup"])
	restored := false
	rc.Undo("برگرداندن افزونه‌ی "+slug+" به نسخه‌ی "+oldVer, func(ctx context.Context) error {
		args := url.Values{"slug": {slug}, "b": {b}}
		if wasActive {
			args.Set("active", "1")
		}
		if _, err := rc.helperCall(ctx, "plugin_restore", args); err != nil {
			return fmt.Errorf("%w (نسخه‌ی قبلی هنوز در %s روی هاست است)", err, backup)
		}
		restored = true
		return nil
	})
	rc.Finally("حذف نسخه‌ی پشتیبان افزونه‌ی "+slug, func(ctx context.Context) error {
		if restored {
			return nil // the backup became the plugin again
		}
		if rc.rolledBack {
			return fmt.Errorf("برگرداندن انجام نشد؛ نسخه‌ی %s در %s نگه داشته شد", oldVer, backup)
		}
		_, err := rc.helperCall(ctx, "plugin_backup_remove", url.Values{"b": {b}})
		if err != nil {
			return fmt.Errorf("%w (پوشه‌ی %s را می‌توانید دستی پاک کنید)", err, backup)
		}
		return nil
	})
	rc.Logf("افزونه‌ی %s از نسخه‌ی %s به %s به‌روز شد (نسخه‌ی قبلی پشتیبان گرفته شد)", slug, oldVer, newVer)
	return nil
}
