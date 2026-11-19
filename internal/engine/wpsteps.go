package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/traptiterbeats/wpauto/internal/config"
	"github.com/traptiterbeats/wpauto/internal/cpanel"
	"github.com/traptiterbeats/wpauto/internal/wp"
)

func init() {
	Register("wp.plan", "checks the target (subdomain or subfolder) and refuses to touch an existing site", stepPlan)
	Register("cpanel.subdomain", "creates a subdomain (rolled back on failure)", stepSubdomain)
	Register("cpanel.mkdir", "creates a folder (rolled back on failure)", stepMkdir)
	Register("cpanel.php", "sets the PHP version of a domain (args: vhost, version)", stepPHP)
	Register("cpanel.autossl", "asks cPanel to issue free SSL certificates now", stepAutoSSL)
	Register("mysql.create", "creates a database and user with a random password (rolled back on failure)", stepMySQL)
	Register("wp.helper", "uploads the temporary helper and checks PHP on the site", stepHelper)
	Register("wp.files", "downloads and extracts WordPress (host downloads; panel uploads as fallback)", stepFiles)
	Register("wp.config", "writes wp-config.php", stepConfig)
	Register("wp.setup", "runs the WordPress installer and writes .htaccess", stepSetup)
	Register("wp.health", "checks the home page and the login page (optional arg expect: text the home page must contain)", stepHealth)
}

// zipURLOverride is set by tests.
var zipURLOverride string

func zipURL(locale string) string {
	if zipURLOverride != "" {
		return zipURLOverride
	}
	return wp.ZipURL(locale)
}

var (
	labelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)
	emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	userRe  = regexp.MustCompile(`^[A-Za-z0-9._@-]{3,60}$`)
)

// dirState lists a directory: exists, entries (ignoring harmless defaults).
func dirState(ctx context.Context, c *cpanel.Client, dir string) (bool, []string) {
	data, err := c.Call(ctx, "Fileman", "list_files", url.Values{"dir": {dir}})
	if err != nil {
		return false, nil
	}
	var files []struct {
		File string `json:"file"`
	}
	_ = json.Unmarshal(data, &files)
	var names []string
	for _, f := range files {
		switch f.File {
		case ".", "..", "cgi-bin", ".well-known", "index.html", "default.html", ".htaccess":
			continue
		}
		names = append(names, f.File)
	}
	return true, names
}

func stepPlan(ctx context.Context, rc *RunContext, a map[string]string) (map[string]string, error) {
	mode, domain, name := a["mode"], strings.ToLower(strings.TrimSpace(a["domain"])), strings.ToLower(strings.TrimSpace(a["name"]))
	if mode != "subdomain" && mode != "subfolder" {
		return nil, fmt.Errorf("mode must be subdomain or subfolder")
	}
	if !labelRe.MatchString(name) {
		return nil, fmt.Errorf("نام «%s» مجاز نیست: فقط حروف کوچک انگلیسی، عدد و خط تیره", name)
	}
	if strings.HasPrefix(name, "wp-") || name == "cgi-bin" {
		return nil, fmt.Errorf("نام «%s» رزرو است", name)
	}
	if !userRe.MatchString(rc.Inputs["admin_user"]) {
		return nil, fmt.Errorf("نام کاربری مدیر باید ۳ تا ۶۰ کاراکتر انگلیسی باشد")
	}
	if !emailRe.MatchString(rc.Inputs["admin_email"]) {
		return nil, fmt.Errorf("ایمیل مدیر معتبر نیست")
	}
	if len(rc.Inputs["admin_pass"]) < 12 {
		return nil, fmt.Errorf("رمز مدیر باید حداقل ۱۲ کاراکتر باشد")
	}
	c, err := rc.CPanel(ctx)
	if err != nil {
		return nil, err
	}
	roots, err := DocRoots(ctx, c)
	if err != nil {
		return nil, err
	}
	var parent *DocRoot
	for i := range roots {
		if roots[i].Domain == domain {
			parent = &roots[i]
		}
	}
	if parent == nil {
		return nil, fmt.Errorf("دامنه‌ی %s روی این حساب نیست", domain)
	}
	home := path.Dir(roots[0].Root) // main docroot is <home>/public_html
	out := map[string]string{"rootdomain": domain, "sub": name}
	if mode == "subdomain" {
		host := name + "." + domain
		for _, r := range roots {
			if r.Domain == host {
				return nil, fmt.Errorf("ساب‌دامین %s از قبل وجود دارد؛ نام دیگری انتخاب کنید", host)
			}
		}
		tpl := rc.Profile.Quirks["subdomain_dir"]
		if tpl == "" {
			tpl = "{host}"
		}
		rel := strings.NewReplacer("{host}", host, "{sub}", name, "{domain}", domain).Replace(tpl)
		out["host"], out["docroot"], out["docroot_rel"], out["path"] = host, path.Join(home, rel), rel, "/"
	} else {
		out["host"], out["docroot"], out["path"] = domain, path.Join(parent.Root, name), "/"+name+"/"
		out["docroot_rel"] = strings.TrimPrefix(out["docroot"], home+"/")
	}
	if exists, names := dirState(ctx, c, out["docroot"]); exists && len(names) > 0 {
		return nil, fmt.Errorf("پوشه‌ی %s خالی نیست (%s)؛ برای جلوگیری از آسیب به سایت موجود متوقف شد", out["docroot"], strings.Join(first(names, 5), ", "))
	} else if exists {
		out["dir_exists"] = "true"
	}
	if ok, ver, _ := DetectWordPress(ctx, c, out["docroot"]); ok {
		return nil, fmt.Errorf("در %s وردپرس %s نصب است", out["docroot"], ver)
	}
	addr := siteAddr(ctx, rc, c, domain)
	out["site_addr"] = addr
	rc.Logf("مقصد: %s%s → %s (IP: %s)", out["host"], out["path"], out["docroot"], addr)
	return out, nil
}

// siteAddr is where to connect for a site before its DNS resolves: the profile's site_addr, else
// the domain's IP, else the cPanel server's IP.
func siteAddr(ctx context.Context, rc *RunContext, c *cpanel.Client, domain string) string {
	if addr := rc.Profile.Quirks["site_addr"]; addr != "" {
		return addr
	}
	if ips, err := net.DefaultResolver.LookupHost(ctx, domain); err == nil && len(ips) > 0 {
		return ips[0]
	}
	if u, err := url.Parse(c.BaseURL()); err == nil {
		if ips, err := net.DefaultResolver.LookupHost(ctx, u.Hostname()); err == nil && len(ips) > 0 {
			return ips[0]
		}
	}
	return ""
}

func first(s []string, n int) []string {
	if len(s) > n {
		return append(s[:n:n], "…")
	}
	return s
}

func stepSubdomain(ctx context.Context, rc *RunContext, a map[string]string) (map[string]string, error) {
	c, err := rc.CPanel(ctx)
	if err != nil {
		return nil, err
	}
	host := a["sub"] + "." + a["domain"]
	if _, err := c.Call(ctx, "SubDomain", "addsubdomain", url.Values{"domain": {a["sub"]}, "rootdomain": {a["domain"]}, "dir": {a["dir"]}, "disallowdot": {"1"}}); err != nil {
		return nil, err
	}
	rc.Logf("ساب‌دامین %s ساخته شد", host)
	docroot := a["docroot"]
	rc.Undo("حذف ساب‌دامین "+host, func(ctx context.Context) error {
		_, err := c.API2(ctx, "SubDomain", "delsubdomain", url.Values{"domain": {host}})
		if err == nil && docroot != "" && a["dir_exists"] != "true" {
			err = c.Remove(ctx, docroot)
		}
		return err
	})
	return map[string]string{"host": host}, nil
}

func stepMkdir(ctx context.Context, rc *RunContext, a map[string]string) (map[string]string, error) {
	c, err := rc.CPanel(ctx)
	if err != nil {
		return nil, err
	}
	dir := a["path"]
	if a["skip_if_exists"] == "true" {
		rc.Logf("پوشه‌ی %s از قبل هست (خالی)", dir)
		return nil, nil
	}
	if err := c.Mkdir(ctx, dir); err != nil {
		return nil, err
	}
	rc.Logf("پوشه‌ی %s ساخته شد", dir)
	rc.Undo("حذف پوشه‌ی "+dir, func(ctx context.Context) error { return c.Remove(ctx, dir) })
	return nil, nil
}

func stepPHP(ctx context.Context, rc *RunContext, a map[string]string) (map[string]string, error) {
	v := strings.TrimSpace(a["version"])
	if v == "" {
		return nil, errors.New("no preferred PHP version available on this host")
	}
	c, err := rc.CPanel(ctx)
	if err != nil {
		return nil, err
	}
	pkg := "ea-php" + strings.ReplaceAll(v, ".", "")
	if _, err := c.Call(ctx, "LangPHP", "php_set_vhost_versions", url.Values{"version": {pkg}, "vhost": {a["vhost"]}}); err != nil {
		return nil, err
	}
	rc.Logf("PHP %s برای %s تنظیم شد", v, a["vhost"])
	return map[string]string{"version": v}, nil
}

func stepAutoSSL(ctx context.Context, rc *RunContext, _ map[string]string) (map[string]string, error) {
	c, err := rc.CPanel(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := c.Call(ctx, "SSL", "start_autossl_check", nil); err != nil {
		return nil, err
	}
	rc.Logf("درخواست صدور SSL رایگان ثبت شد (معمولاً چند دقیقه طول می‌کشد)")
	return nil, nil
}

func stepMySQL(ctx context.Context, rc *RunContext, a map[string]string) (map[string]string, error) {
	c, err := rc.CPanel(ctx)
	if err != nil {
		return nil, err
	}
	prefix := a["prefix"]
	n := 5
	if len(prefix)+2+n > 32 { // user names are limited to 32 characters on modern cPanel
		n = 32 - len(prefix) - 2
	}
	if n < 3 {
		return nil, fmt.Errorf("database prefix %q is too long", prefix)
	}
	name := prefix + "wp" + wp.RandString(n, "abcdefghijklmnopqrstuvwxyz")
	pass := wp.RandString(20, wp.Password) + "#9a"
	if _, err := c.Call(ctx, "Mysql", "create_database", url.Values{"name": {name}}); err != nil {
		return nil, err
	}
	rc.Undo("حذف دیتابیس "+name, func(ctx context.Context) error {
		_, err := c.Call(ctx, "Mysql", "delete_database", url.Values{"name": {name}})
		return err
	})
	if _, err := c.Call(ctx, "Mysql", "create_user", url.Values{"name": {name}, "password": {pass}}); err != nil {
		return nil, err
	}
	rc.Undo("حذف کاربر دیتابیس "+name, func(ctx context.Context) error {
		_, err := c.Call(ctx, "Mysql", "delete_user", url.Values{"name": {name}})
		return err
	})
	if _, err := c.Call(ctx, "Mysql", "set_privileges_on_database", url.Values{"user": {name}, "database": {name}, "privileges": {"ALL PRIVILEGES"}}); err != nil {
		return nil, err
	}
	rc.Vars["db_pass"] = pass
	rc.Logf("دیتابیس و کاربر %s ساخته شد", name)
	return map[string]string{"db_name": name, "db_user": name}, nil
}

func (rc *RunContext) site(a map[string]string) *wp.Site {
	if s, ok := rc.siteObj.(*wp.Site); ok && s != nil {
		return s
	}
	s := &wp.Site{Host: a["host"], Path: a["path"], Addr: a["addr"]}
	rc.siteObj = s
	return s
}

func stepHelper(ctx context.Context, rc *RunContext, a map[string]string) (map[string]string, error) {
	c, err := rc.CPanel(ctx)
	if err != nil {
		return nil, err
	}
	docroot := a["docroot"]
	token := wp.RandHex(24)
	rc.Vars["helper_token"] = token
	helper, job := wp.HelperName(token), wp.JobName(token)
	if err := c.SaveFile(ctx, docroot, job, "{}"); err != nil {
		return nil, fmt.Errorf("cannot write to %s: %w", docroot, err)
	}
	if err := c.SaveFile(ctx, docroot, helper, wp.HelperPHP(token)); err != nil {
		_ = c.Remove(ctx, path.Join(docroot, job))
		return nil, err
	}
	s := rc.site(a)
	rc.Finally("حذف فایل‌های موقت نصب", func(ctx context.Context) error {
		if _, err := s.Helper(ctx, helper, token, "cleanup"); err == nil {
			return nil
		}
		return c.Remove(ctx, path.Join(docroot, helper), path.Join(docroot, job), path.Join(docroot, ".wpauto-wp.zip"), path.Join(docroot, ".wpauto-x"))
	})
	ping, err := s.Ping(ctx, helper, token, 3*time.Minute, func(err error) {
		rc.Logf("سایت هنوز اسکریپت نصب را سرو نمی‌کند (%v)؛ برای ساب‌دامین تازه چند دقیقه صبر می‌کنیم…", err)
	})
	if err != nil {
		return nil, err
	}
	rc.Logf("PHP %v روی سایت · ZipArchive: %v · cURL: %v · فضای آزاد: %v MB · memory_limit: %v",
		ping["php"], ping["zip"], ping["curl"], ping["free_mb"], ping["memory_limit"])
	if z, _ := ping["zip"].(bool); !z {
		return nil, errors.New("افزونه‌ی ZipArchive در PHP این هاست فعال نیست")
	}
	if m, _ := ping["mysqli"].(bool); !m {
		return nil, errors.New("افزونه‌ی mysqli در PHP این هاست فعال نیست")
	}
	if f, _ := ping["free_mb"].(float64); f > 0 && f < 150 {
		return nil, fmt.Errorf("فضای آزاد کافی نیست (%.0f MB)", f)
	}
	scheme := "http"
	if s.HasValidTLS(ctx) {
		scheme = "https"
	} else {
		rc.Warnf("گواهی SSL معتبر هنوز روی %s نیست؛ سایت با http نصب می‌شود. بعد از صدور SSL می‌توانید آدرس را https کنید.", s.Host)
	}
	siteURL := scheme + "://" + s.Host + strings.TrimSuffix(s.Path, "/")
	j := wp.Job{ZipURL: wp.ZipURL(a["locale"]), Title: rc.Inputs["title"], AdminUser: rc.Inputs["admin_user"],
		AdminEmail: rc.Inputs["admin_email"], AdminPass: rc.Inputs["admin_pass"], Locale: a["locale"], URL: siteURL, Public: true}
	if err := c.SaveFile(ctx, docroot, job, j.JSON()); err != nil {
		return nil, err
	}
	return map[string]string{"url": siteURL, "php": fmt.Sprint(ping["php"])}, nil
}

func stepFiles(ctx context.Context, rc *RunContext, a map[string]string) (map[string]string, error) {
	c, err := rc.CPanel(ctx)
	if err != nil {
		return nil, err
	}
	s, token := rc.site(a), rc.Vars["helper_token"]
	helper := wp.HelperName(token)
	rc.Logf("دانلود وردپرس روی هاست از %s", wp.ZipURL(a["locale"]))
	res, err := s.Helper(ctx, helper, token, "download")
	if err != nil {
		rc.Warnf("دانلود مستقیم روی هاست نشد (%v)؛ پنل خودش دانلود و آپلود می‌کند", err)
		f, err := cachedZip(ctx, zipURL(a["locale"]))
		if err != nil {
			return nil, err
		}
		defer f.Close()
		if err := c.Upload(ctx, a["docroot"], ".wpauto-wp.zip", f); err != nil {
			return nil, fmt.Errorf("upload failed: %w", err)
		}
		rc.Logf("فایل وردپرس آپلود شد")
	} else {
		rc.Logf("دانلود شد (%.1f MB)", toF(res["bytes"])/1048576)
	}
	res, err = s.Helper(ctx, helper, token, "extract")
	if err != nil {
		return nil, err
	}
	rc.Logf("فایل‌ها باز شدند (%v مورد)", res["moved"])
	return nil, nil
}

func toF(v any) float64 {
	f, _ := v.(float64)
	return f
}

// cachedZip downloads a WordPress zip into <config>/cache once a day and opens it.
func cachedZip(ctx context.Context, u string) (*os.File, error) {
	dir := filepath.Join(config.Dir(), "cache")
	_ = os.MkdirAll(dir, 0o700)
	p := filepath.Join(dir, path.Base(u))
	if st, err := os.Stat(p); err == nil && time.Since(st.ModTime()) < 24*time.Hour && st.Size() > 1<<20 {
		return os.Open(p)
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("panel could not download WordPress: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("panel could not download WordPress: HTTP %d", resp.StatusCode)
	}
	tmp := p + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()
	if err := os.Rename(tmp, p); err != nil {
		return nil, err
	}
	return os.Open(p)
}

func stepConfig(ctx context.Context, rc *RunContext, a map[string]string) (map[string]string, error) {
	c, err := rc.CPanel(ctx)
	if err != nil {
		return nil, err
	}
	if rc.Vars["db_pass"] == "" {
		return nil, errors.New("database password is missing (mysql.create must run first)")
	}
	prefix := "wp" + wp.RandString(3, "abcdefghijklmnopqrstuvwxyz") + "_"
	host := rc.Profile.Quirks["db_host"]
	cfg := wp.Config(wp.DBConfig{Name: a["db_name"], User: a["db_user"], Pass: rc.Vars["db_pass"], Host: host, TablePrefix: prefix})
	if err := c.SaveFile(ctx, a["docroot"], "wp-config.php", cfg); err != nil {
		return nil, err
	}
	rc.Logf("wp-config.php نوشته شد (پیشوند جدول‌ها: %s)", prefix)
	return map[string]string{"table_prefix": prefix}, nil
}

func stepSetup(ctx context.Context, rc *RunContext, a map[string]string) (map[string]string, error) {
	c, err := rc.CPanel(ctx)
	if err != nil {
		return nil, err
	}
	s, token := rc.site(a), rc.Vars["helper_token"]
	res, err := s.Helper(ctx, wp.HelperName(token), token, "install")
	if err != nil {
		return nil, err
	}
	rc.Logf("وردپرس %v نصب شد؛ مدیر: %s", res["version"], rc.Inputs["admin_user"])
	if err := c.SaveFile(ctx, a["docroot"], ".htaccess", wp.Htaccess(a["path"])); err != nil {
		rc.Warnf(".htaccess نوشته نشد: %v (پیوندهای یکتا را از پیشخوان ذخیره کنید)", err)
	}
	return map[string]string{"version": fmt.Sprint(res["version"])}, nil
}

func stepHealth(ctx context.Context, rc *RunContext, a map[string]string) (map[string]string, error) {
	s := rc.site(a)
	code, body, err := s.Get(ctx, "/")
	if err != nil {
		return nil, err
	}
	if code != 200 || !strings.Contains(body, "wp-content") && !strings.Contains(body, "wp-includes") {
		return nil, fmt.Errorf("صفحه‌ی اصلی جواب درستی نداد (HTTP %d)", code)
	}
	rc.Logf("✔ صفحه‌ی اصلی: HTTP %d", code)
	code, _, err = s.Get(ctx, "/wp-login.php")
	if err != nil || code != 200 {
		return nil, fmt.Errorf("صفحه‌ی ورود جواب نداد (HTTP %d) %v", code, err)
	}
	rc.Logf("✔ صفحه‌ی ورود: HTTP %d", code)
	if _, _, err := s.Get(ctx, "/?p=1"); err == nil {
		rc.Logf("✔ نوشته‌ی نمونه در دسترس است")
	}
	if exp := a["expect"]; exp != "" {
		if strings.Contains(body, exp) {
			rc.Logf("✔ قالب جدید روی صفحه‌ی اصلی بارگذاری می‌شود")
		} else {
			rc.Warnf("نشانی %s در صفحه‌ی اصلی دیده نشد (شاید افزونه‌ی کش نسخه‌ی قدیمی را نشان می‌دهد)", exp)
		}
	}
	u := strings.TrimSuffix(a["url"], "/")
	rc.Logf("آدرس سایت: %s · پیشخوان: %s/wp-admin/", u, u)
	return map[string]string{"url": u, "admin_url": u + "/wp-admin/"}, nil
}
