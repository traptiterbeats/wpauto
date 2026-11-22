package engine

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/traptiterbeats/wpauto/internal/wpplugins"
)

func init() {
	Register("plugins.status", "reads which recommended plugins are installed and active (read-only)", stepPluginsStatus)
	Register("plugins.ensure", "installs (downloaded by the panel) and activates recommended plugins; existing plugin folders are never overwritten", stepPluginsEnsure)
}

// pluginCacheTTL is how long a downloaded wordpress.org zip is reused.
var pluginCacheTTL = time.Hour

// SetWPOrgZipURL replaces where plugin zips are downloaded from (tests) and returns a restore func.
func SetWPOrgZipURL(f func(slug string) string) func() {
	old := wporgZipURL
	wporgZipURL = f
	return func() { wporgZipURL = old }
}

// pluginStates returns the state of every recommended plugin from the attached site's wp_info.
func (rc *RunContext) pluginStates(list []wpplugins.Plugin) map[string]string {
	pl, _ := rc.wpInfo["plugins"].(map[string]any)
	out := map[string]string{}
	for _, p := range list {
		out[p.Slug] = wpplugins.StateMissing
		if m, ok := pl[p.Slug].(map[string]any); ok {
			out[p.Slug] = wpplugins.StateInactive
			if a, _ := m["active"].(bool); a {
				out[p.Slug] = wpplugins.StateActive
			}
		}
	}
	return out
}

func statesJSON(m map[string]string) string {
	b, _ := json.Marshal(m)
	return string(b)
}

func stepPluginsStatus(ctx context.Context, rc *RunContext, a map[string]string) (map[string]string, error) {
	list := wpplugins.List()
	st := rc.pluginStates(list)
	n := 0
	for _, p := range list {
		switch st[p.Slug] {
		case wpplugins.StateActive:
			n++
			rc.Logf("✔ %s: نصب و فعال", p.Name)
		case wpplugins.StateInactive:
			rc.Logf("%s: نصب است ولی فعال نیست", p.Name)
		default:
			rc.Logf("%s: نصب نیست", p.Name)
		}
	}
	rc.Logf("%d افزونه از %d افزونه‌ی پیشنهادی نصب و فعال است", n, len(list))
	return map[string]string{"status": statesJSON(st)}, nil
}

// checkZip makes sure a downloaded file is a zip whose entries sit in the slug's folder.
func checkZip(p, slug string) error {
	z, err := zip.OpenReader(p)
	if err != nil {
		return fmt.Errorf("فایل دانلودشده‌ی %s zip سالم نیست: %w", slug, err)
	}
	defer z.Close()
	for _, f := range z.File {
		n := strings.ReplaceAll(f.Name, "\\", "/")
		if strings.HasPrefix(n, "__MACOSX/") {
			continue
		}
		if !strings.HasPrefix(n, slug+"/") || strings.Contains(n, "..") {
			return fmt.Errorf("zip افزونه‌ی %s مسیر نامنتظره دارد: %s", slug, n)
		}
	}
	return nil
}

func stepPluginsEnsure(ctx context.Context, rc *RunContext, a map[string]string) (map[string]string, error) {
	list := wpplugins.List()
	var want []wpplugins.Plugin
	seen := map[string]bool{}
	for _, s := range strings.FieldsFunc(a["plugins"], func(r rune) bool { return r == ',' || r == '\n' || r == ' ' || r == '\r' }) {
		s = strings.ToLower(strings.TrimSpace(s))
		if seen[s] {
			continue
		}
		seen[s] = true
		p, ok := wpplugins.Find(list, s)
		if !ok {
			return nil, fmt.Errorf("افزونه‌ی «%s» در فهرست افزونه‌های لازم نیست", s)
		}
		want = append(want, p)
	}
	if len(want) == 0 {
		return nil, fmt.Errorf("هیچ افزونه‌ای انتخاب نشده")
	}
	st := rc.pluginStates(list)
	for _, p := range want {
		slug := p.Slug
		created := false
		if st[slug] == wpplugins.StateMissing {
			rc.Logf("دانلود %s روی سرور پنل از wordpress.org…", p.Name)
			local, err := fetchToCacheTTL(ctx, wporgZipURL(slug), "wporg-plugin-"+slug+".zip", pluginCacheTTL)
			if err != nil {
				return nil, fmt.Errorf("دانلود %s ناموفق بود: %w", p.Name, err)
			}
			if err := checkZip(local, slug); err != nil {
				_ = os.Remove(local)
				return nil, err
			}
			if err := rc.upload(ctx, local, slug+".zip"); err != nil {
				return nil, err
			}
			res, err := rc.helperCall(ctx, "plugin_install", url.Values{"slug": {slug}, "f": {slug + ".zip"}})
			if err != nil {
				return nil, fmt.Errorf("نصب %s: %w", p.Name, err)
			}
			created, _ = res["created"].(bool)
			if created {
				rc.Undo("حذف افزونه‌ی "+slug, func(ctx context.Context) error {
					_, err := rc.helperCall(ctx, "plugin_remove", url.Values{"slug": {slug}, "delete": {"1"}})
					return err
				})
				rc.Logf("افزونه‌ی %s در wp-content/plugins/%s نصب شد", p.Name, slug)
			} else {
				// a folder appeared meanwhile: it is not ours, so it is left exactly as it is
				rc.Warnf("پوشه‌ی %s از قبل روی سایت بود؛ بازنویسی نشد و فقط فعال‌سازی امتحان می‌شود", slug)
			}
		} else {
			rc.Logf("افزونه‌ی %s از قبل نصب است؛ فایل‌هایش دست نمی‌خورد", p.Name)
		}
		if st[slug] == wpplugins.StateActive {
			rc.Logf("✔ %s از قبل فعال است", p.Name)
			continue
		}
		res, err := rc.helperCall(ctx, "plugin_activate", url.Values{"slug": {slug}})
		if err != nil {
			return nil, fmt.Errorf("فعال‌سازی %s: %w", p.Name, err)
		}
		if already, _ := res["already"].(bool); !already && !created {
			// we only switched it on: undo switches it off again, files stay
			rc.Undo("غیرفعال‌کردن افزونه‌ی "+slug, func(ctx context.Context) error {
				_, err := rc.helperCall(ctx, "plugin_remove", url.Values{"slug": {slug}})
				return err
			})
		}
		st[slug] = wpplugins.StateActive
		rc.Logf("✔ %s نصب و فعال شد", p.Name)
	}
	return map[string]string{"status": statesJSON(st), "count": fmt.Sprint(len(want))}, nil
}
