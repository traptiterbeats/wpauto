package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/traptiterbeats/wpauto/internal/catalog"
	"github.com/traptiterbeats/wpauto/internal/config"
	"github.com/traptiterbeats/wpauto/internal/customize"
	"github.com/traptiterbeats/wpauto/internal/wp"
)

func init() {
	Register("wp.locate", "finds an existing WordPress by domain and optional folder (read-only)", stepLocate)
	Register("wp.attach", "uploads the temporary helper into an existing WordPress and reads its themes and plugins", stepAttach)
	Register("theme.plugins", "installs and activates the plugins a catalogue theme needs (rolled back on failure)", stepThemePlugins)
	Register("theme.install", "installs a catalogue theme and optionally activates it (rolled back on failure)", stepThemeInstall)
	Register("theme.demo", "imports the theme's demo with Saayeh Demo Importer (removed again on failure)", stepThemeDemo)
}

// Drafts returns the customisation drafts.
func Drafts() *customize.Store { return customize.Open(filepath.Join(config.Dir(), "drafts")) }

// Catalog returns the panel's theme catalogue.
func Catalog() *catalog.Catalog { return catalog.Open(filepath.Join(config.Dir(), "catalog")) }

var folderRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

func stepLocate(ctx context.Context, rc *RunContext, a map[string]string) (map[string]string, error) {
	domain, folder := strings.ToLower(strings.TrimSpace(a["domain"])), strings.Trim(strings.TrimSpace(a["folder"]), "/")
	if folder != "" && (!folderRe.MatchString(folder) || strings.Contains(folder, "..")) {
		return nil, fmt.Errorf("نام پوشه‌ی «%s» مجاز نیست", folder)
	}
	c, err := rc.CPanel(ctx)
	if err != nil {
		return nil, err
	}
	roots, err := DocRoots(ctx, c)
	if err != nil {
		return nil, err
	}
	var root string
	for _, r := range roots {
		if r.Domain == domain {
			root = r.Root
		}
	}
	if root == "" {
		return nil, fmt.Errorf("دامنه‌ی %s روی این حساب نیست", domain)
	}
	out := map[string]string{"host": domain, "docroot": root, "path": "/"}
	if folder != "" {
		out["docroot"], out["path"] = path.Join(root, folder), "/"+folder+"/"
	}
	ok, ver, err := DetectWordPress(ctx, c, out["docroot"])
	if err != nil {
		return nil, fmt.Errorf("پوشه‌ی %s خوانده نشد: %w", out["docroot"], err)
	}
	if !ok {
		return nil, fmt.Errorf("در %s وردپرسی پیدا نشد", out["docroot"])
	}
	out["version"] = ver
	out["site_addr"] = siteAddr(ctx, rc, c, domain)
	rc.Logf("وردپرس %s در %s%s (%s)", ver, domain, out["path"], out["docroot"])
	return out, nil
}

// helperCall runs a helper action with arguments on the attached site.
func (rc *RunContext) helperCall(ctx context.Context, action string, args url.Values) (map[string]any, error) {
	s, ok := rc.siteObj.(*wp.Site)
	token := rc.Vars["helper_token"]
	if !ok || token == "" {
		return nil, errors.New("helper is not attached (wp.attach must run first)")
	}
	return s.HelperArgs(ctx, wp.HelperName(token), token, action, args)
}

func stepAttach(ctx context.Context, rc *RunContext, a map[string]string) (map[string]string, error) {
	c, err := rc.CPanel(ctx)
	if err != nil {
		return nil, err
	}
	docroot := a["docroot"]
	token := wp.RandHex(24)
	rc.Vars["helper_token"], rc.Vars["docroot"] = token, docroot
	helper, job, pkg := wp.HelperName(token), wp.JobName(token), wp.PkgDir(token)
	if err := c.SaveFile(ctx, docroot, job, "{}"); err != nil {
		return nil, fmt.Errorf("cannot write to %s: %w", docroot, err)
	}
	if err := c.SaveFile(ctx, docroot, helper, wp.HelperPHP(token)); err != nil {
		_ = c.Remove(ctx, path.Join(docroot, job))
		return nil, err
	}
	s := rc.site(a)
	rc.Finally("حذف فایل‌های موقت", func(ctx context.Context) error {
		if _, err := s.Helper(ctx, helper, token, "cleanup"); err == nil {
			return nil
		}
		return c.Remove(ctx, path.Join(docroot, helper), path.Join(docroot, job), path.Join(docroot, pkg))
	})
	if err := c.Mkdir(ctx, path.Join(docroot, pkg)); err != nil {
		return nil, fmt.Errorf("cannot create %s: %w", pkg, err)
	}
	if err := s.Detect(ctx); err != nil {
		return nil, err
	}
	if _, err := s.Helper(ctx, helper, token, "ping"); err != nil {
		return nil, err
	}
	info, err := rc.helperCall(ctx, "wp_info", nil)
	if err != nil {
		return nil, err
	}
	rc.wpInfo = info
	n := 0
	if p, ok := info["plugins"].(map[string]any); ok {
		n = len(p)
	}
	rc.Logf("وردپرس %v · PHP %v · قالب فعال: %v · %d افزونه", info["version"], info["php"], info["stylesheet"], n)
	return map[string]string{"url": strings.TrimSuffix(fmt.Sprint(info["url"]), "/"), "stylesheet": fmt.Sprint(info["stylesheet"])}, nil
}

func (rc *RunContext) catalogTheme(slug string) (*catalog.Theme, error) {
	t, err := Catalog().Get(strings.TrimSpace(slug))
	if err != nil {
		return nil, fmt.Errorf("قالب «%s» در کاتالوگ نیست", slug)
	}
	return t, nil
}

// upload sends a local file into the helper's package folder on the site.
func (rc *RunContext) upload(ctx context.Context, local, name string) error {
	c, err := rc.CPanel(ctx)
	if err != nil {
		return err
	}
	f, err := os.Open(local)
	if err != nil {
		return err
	}
	defer f.Close()
	dir := path.Join(rc.Vars["docroot"], wp.PkgDir(rc.Vars["helper_token"]))
	if err := c.Upload(ctx, dir, name, f); err != nil {
		return fmt.Errorf("آپلود %s ناموفق بود: %w", name, err)
	}
	return nil
}

func (rc *RunContext) installedPlugin(slug string) (version string, ok bool) {
	pl, _ := rc.wpInfo["plugins"].(map[string]any)
	p, ok := pl[slug].(map[string]any)
	if !ok {
		return "", false
	}
	return fmt.Sprint(p["version"]), true
}

// wporgZipURL is replaced in tests.
var wporgZipURL = func(slug string) string {
	return "https://downloads.wordpress.org/plugin/" + slug + ".latest-stable.zip"
}

func stepThemePlugins(ctx context.Context, rc *RunContext, a map[string]string) (map[string]string, error) {
	t, err := rc.catalogTheme(a["theme"])
	if err != nil {
		return nil, err
	}
	if len(t.Plugins) == 0 {
		rc.Logf("این قالب افزونه‌ی وابسته ندارد")
		return nil, nil
	}
	cat := Catalog()
	for _, p := range t.Plugins {
		slug := p.Slug
		if !catalog.ValidSlug(slug) {
			return nil, fmt.Errorf("نامک افزونه‌ی %q نامعتبر است", slug)
		}
		created := false
		if ver, ok := rc.installedPlugin(slug); ok {
			rc.Logf("افزونه‌ی %s از قبل نصب است (نسخه‌ی %s)", slug, ver)
		} else {
			lib := cat.LibraryPath(slug)
			switch {
			case p.Source != "wporg" && lib != "":
				if err := rc.upload(ctx, lib, slug+".zip"); err != nil {
					return nil, err
				}
				rc.Logf("افزونه‌ی %s از کتابخانه‌ی پنل آپلود شد", slug)
			case p.Source == "library":
				return nil, fmt.Errorf("zip افزونه‌ی %s در کتابخانه‌ی پنل نیست", slug)
			default:
				if _, err := rc.helperCall(ctx, "plugin_fetch", url.Values{"slug": {slug}}); err != nil {
					rc.Warnf("دانلود %s روی هاست نشد (%v)؛ پنل خودش دانلود و آپلود می‌کند", slug, err)
					local, err := fetchToCache(ctx, wporgZipURL(slug), slug+".zip")
					if err != nil {
						return nil, err
					}
					if err := rc.upload(ctx, local, slug+".zip"); err != nil {
						return nil, err
					}
				}
				rc.Logf("افزونه‌ی %s از wordpress.org گرفته شد", slug)
			}
			res, err := rc.helperCall(ctx, "plugin_install", url.Values{"slug": {slug}, "f": {slug + ".zip"}})
			if err != nil {
				return nil, err
			}
			created, _ = res["created"].(bool)
			if created {
				s := slug
				rc.Undo("حذف افزونه‌ی "+s, func(ctx context.Context) error {
					_, err := rc.helperCall(ctx, "plugin_remove", url.Values{"slug": {s}, "delete": {"1"}})
					return err
				})
			}
			rc.Logf("افزونه‌ی %s نصب شد", slug)
		}
		res, err := rc.helperCall(ctx, "plugin_activate", url.Values{"slug": {slug}})
		if err != nil {
			return nil, fmt.Errorf("فعال‌سازی %s: %w", slug, err)
		}
		if already, _ := res["already"].(bool); already {
			rc.Logf("افزونه‌ی %s از قبل فعال است", slug)
			continue
		}
		if !created {
			s := slug
			rc.Undo("غیرفعال‌کردن افزونه‌ی "+s, func(ctx context.Context) error {
				_, err := rc.helperCall(ctx, "plugin_remove", url.Values{"slug": {s}})
				return err
			})
		}
		rc.Logf("✔ افزونه‌ی %s فعال شد", slug)
	}
	return map[string]string{"count": fmt.Sprint(len(t.Plugins))}, nil
}

func stepThemeInstall(ctx context.Context, rc *RunContext, a map[string]string) (map[string]string, error) {
	t, err := rc.catalogTheme(a["theme"])
	if err != nil {
		return nil, err
	}
	themes, _ := rc.wpInfo["themes"].(map[string]any)
	if v, ok := themes[t.Slug]; ok {
		rc.Warnf("قالب %s از قبل روی سایت هست (نسخه‌ی %v)؛ به آن دست نمی‌زنیم و همان استفاده می‌شود", t.Slug, v)
	} else {
		if err := rc.upload(ctx, t.Path(t.ThemeZip), t.Slug+"-theme.zip"); err != nil {
			return nil, err
		}
		res, err := rc.helperCall(ctx, "theme_install", url.Values{"slug": {t.Slug}, "f": {t.Slug + "-theme.zip"}})
		if err != nil {
			return nil, err
		}
		if created, _ := res["created"].(bool); created {
			slug := t.Slug
			rc.Undo("حذف قالب "+slug, func(ctx context.Context) error {
				_, err := rc.helperCall(ctx, "theme_remove", url.Values{"slug": {slug}})
				return err
			})
		}
		rc.Logf("قالب %s %s نصب شد", t.Name, t.Version)
	}
	if a["activate"] != "true" {
		rc.Logf("قالب نصب شد ولی فعال نشد (طبق انتخاب شما)")
		return map[string]string{"activated": "false"}, nil
	}
	res, err := rc.helperCall(ctx, "theme_activate", url.Values{"slug": {t.Slug}})
	if err != nil {
		return nil, err
	}
	prev := fmt.Sprint(res["previous"])
	if prev != t.Slug && catalog.ValidSlug(prev) {
		rc.Undo("برگرداندن قالب قبلی ("+prev+")", func(ctx context.Context) error {
			_, err := rc.helperCall(ctx, "theme_activate", url.Values{"slug": {prev}})
			return err
		})
	}
	rc.Logf("✔ قالب %s فعال شد (قالب قبلی: %s)", t.Name, prev)
	return map[string]string{"activated": "true", "previous": prev}, nil
}

// sdiLog writes Saayeh's log lines into the run log.
func (rc *RunContext) sdiLog(v any) {
	lines, _ := v.([]any)
	for _, l := range lines {
		m, _ := l.(map[string]any)
		msg := strings.TrimSpace(fmt.Sprint(m["message"]))
		if msg == "" || m["message"] == nil {
			continue
		}
		switch m["level"] {
		case "warning", "error":
			rc.Warnf("سایه: %s", msg)
		default:
			rc.Logf("سایه: %s", msg)
		}
	}
}

// demoPause is the wait between Saayeh retries (shortened in tests).
var demoPause = 3 * time.Second

func stepThemeDemo(ctx context.Context, rc *RunContext, a map[string]string) (map[string]string, error) {
	t, err := rc.catalogTheme(a["theme"])
	if err != nil {
		return nil, err
	}
	if a["import"] != "true" {
		rc.Logf("درون‌ریزی دمو انتخاب نشده بود")
		return nil, nil
	}
	if !t.HasDemo() {
		rc.Logf("این قالب بسته‌ی دمو ندارد")
		return nil, nil
	}
	if a["activate"] != "true" {
		return nil, errors.New("درون‌ریز سایه فقط برای قالب فعال کار می‌کند؛ گزینه‌ی فعال‌سازی قالب را روشن کنید")
	}
	name, local := t.Slug+"-demo.zip", t.Path(t.DemoZip)
	if id := strings.TrimSpace(a["draft"]); id != "" {
		d, err := Drafts().Get(id)
		if err != nil {
			return nil, err
		}
		if d.Theme != t.Slug {
			return nil, fmt.Errorf("پیش‌نویس «%s» برای قالب %s است، نه %s", d.Title, d.Theme, t.Slug)
		}
		demo, err := customize.LoadDemo(local)
		if err != nil {
			return nil, err
		}
		if local, err = customize.Build(demo, d); err != nil {
			return nil, fmt.Errorf("ساخت بسته‌ی شخصی‌سازی‌شده: %w", err)
		}
		logo := "بدون لوگو"
		if d.Logo != "" {
			logo = "با لوگو"
		}
		rc.Logf("بسته‌ی شخصی‌سازی‌شده‌ی «%s» ساخته شد: %d متن، %d تصویر، %s", d.Title, len(d.Values), len(d.Images), logo)
	}
	if err := rc.upload(ctx, local, name); err != nil {
		return nil, err
	}
	res, err := rc.helperCall(ctx, "demo_prepare", url.Values{"slug": {t.Slug}, "f": {name}})
	if err != nil {
		return nil, err
	}
	rc.Logf("بسته‌ی دمو (نسخه‌ی %v) روی سایت آماده شد", res["version"])
	res, err = rc.helperCall(ctx, "demo_start", url.Values{"slug": {t.Slug}, "groups": {strings.Join(t.DemoGroups, ",")}})
	if err != nil {
		return nil, err
	}
	slug := t.Slug
	rc.Undo("حذف محتوای دمو", func(ctx context.Context) error {
		_, _ = rc.helperCall(ctx, "demo_cancel", nil)
		for i := 0; i < 500; i++ {
			r, err := rc.helperCall(ctx, "demo_remove", url.Values{"slug": {slug}})
			if err != nil {
				return err
			}
			st, _ := r["state"].(map[string]any)
			if done, _ := st["done"].(bool); done {
				return nil
			}
		}
		return errors.New("حذف دمو تمام نشد")
	})
	st, _ := res["state"].(map[string]any)
	rc.sdiLog(st["log"])
	label, retries := "", 0
	for i := 0; i < 5000; i++ {
		if done, _ := st["done"].(bool); done {
			rc.Logf("✔ درون‌ریزی دمو کامل شد")
			return map[string]string{"imported": "true"}, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		res, err = rc.helperCall(ctx, "demo_step", nil)
		if err != nil {
			rc.sdiLog(res["log"])
			if retry, _ := res["retry"].(bool); (retry || res == nil) && retries < 3 {
				retries++
				rc.Warnf("مرحله‌ی دمو دوباره امتحان می‌شود (%d/3): %v", retries, err)
				time.Sleep(demoPause)
				continue
			}
			return nil, err
		}
		retries = 0
		st, _ = res["state"].(map[string]any)
		rc.sdiLog(st["log"])
		if l := fmt.Sprint(st["label"]); l != label && st["label"] != nil && l != "" {
			label = l
			rc.Logf("دمو: %s (%v٪)", l, st["percent"])
		}
	}
	return nil, errors.New("درون‌ریزی دمو بیش از حد طول کشید")
}

// fetchToCache downloads u into <config>/cache/name (refreshed daily) and returns the path.
func fetchToCache(ctx context.Context, u, name string) (string, error) {
	dir := filepath.Join(config.Dir(), "cache")
	_ = os.MkdirAll(dir, 0o700)
	p := filepath.Join(dir, filepath.Base(name))
	if st, err := os.Stat(p); err == nil && time.Since(st.ModTime()) < 24*time.Hour && st.Size() > 0 {
		return p, nil
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return "", fmt.Errorf("panel could not download %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("panel could not download %s: HTTP %d", u, resp.StatusCode)
	}
	f, err := os.Create(p + ".part")
	if err != nil {
		return "", err
	}
	if _, err := f.ReadFrom(resp.Body); err != nil {
		f.Close()
		return "", err
	}
	f.Close()
	return p, os.Rename(p+".part", p)
}
