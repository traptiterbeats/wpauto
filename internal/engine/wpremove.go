package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

func init() {
	Register("wp.remove", "permanently deletes test WordPress installs: files, database, database user and their own subdomain (arg: sites, one directory per line; or site = one address domain[/folder] with own_subs = subdomains wpauto created)", stepWPRemove)
}

var reDefine = regexp.MustCompile(`define\s*\(\s*['"](DB_NAME|DB_USER)['"]\s*,\s*['"]([^'"]*)['"]\s*\)`)

// wpConfigDB reads DB_NAME and DB_USER from a wp-config.php.
func wpConfigDB(content string) (name, user string) {
	for _, m := range reDefine.FindAllStringSubmatch(content, -1) {
		if m[1] == "DB_NAME" {
			name = m[2]
		} else {
			user = m[2]
		}
	}
	return name, user
}

func readFile(ctx context.Context, rc *RunContext, dir, file string) (string, error) {
	raw, err := rc.client.Call(ctx, "Fileman", "get_file_content", url.Values{"dir": {dir}, "file": {file}})
	if err != nil {
		return "", err
	}
	var fc struct {
		Content string `json:"content"`
	}
	_ = json.Unmarshal(raw, &fc)
	return fc.Content, nil
}

// wpOwnFiles are the names WordPress itself puts in its folder. When a folder also holds other
// sites, only these are deleted.
func wpOwnFile(name string) bool {
	switch name {
	case "index.php", "xmlrpc.php", "license.txt", "readme.html", ".htaccess", "error_log", ".user.ini", "php.ini":
		return true
	}
	return strings.HasPrefix(name, "wp-") || strings.HasPrefix(name, "wpauto-")
}

// removeOpts says how far a removal may go.
type removeOpts struct {
	// site is set when one project's site is removed by its address (project delete). Then a
	// subdomain is deleted only when it is in ownSubs (wpauto created it), and a plain folder is
	// deleted as a whole only when it holds nothing but WordPress' own files.
	site    bool
	ownSubs map[string]bool
}

// siteDir finds the WordPress folder of an address "domain" or "domain/folder" on the account.
func siteDir(roots []DocRoot, site string) (string, error) {
	site = strings.ToLower(strings.Trim(strings.TrimSpace(site), "/"))
	dom, folder, _ := strings.Cut(site, "/")
	if dom == "" || strings.Contains(folder, "/") || strings.HasPrefix(folder, ".") {
		return "", fmt.Errorf("آدرس سایت «%s» معتبر نیست", site)
	}
	for _, r := range roots {
		if strings.EqualFold(r.Domain, dom) {
			if folder == "" {
				return path.Clean(r.Root), nil
			}
			return path.Join(r.Root, folder), nil
		}
	}
	return "", fmt.Errorf("دامنه‌ی %s روی این حساب cPanel نیست؛ چیزی پاک نشد", dom)
}

func stepWPRemove(ctx context.Context, rc *RunContext, a map[string]string) (map[string]string, error) {
	c, err := rc.CPanel(ctx)
	if err != nil {
		return nil, err
	}
	home := "/home/" + rc.target.Options.Username
	var want []string
	for _, l := range strings.FieldsFunc(a["sites"], func(r rune) bool { return r == '\n' || r == '\r' || r == ',' }) {
		if l = strings.TrimSpace(l); l != "" {
			want = append(want, path.Clean(l))
		}
	}
	opts := removeOpts{site: strings.TrimSpace(a["site"]) != "", ownSubs: map[string]bool{}}
	if opts.site && len(want) > 0 {
		return nil, errors.New("فقط یکی از site یا sites را بدهید")
	}
	if len(want) == 0 && !opts.site {
		return nil, errors.New("هیچ وردپرسی انتخاب نشده")
	}
	for _, s := range strings.Split(a["own_subs"], ",") {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			opts.ownSubs[s] = true
		}
	}
	roots, err := DocRoots(ctx, c)
	if err != nil {
		return nil, err
	}
	var before, limit float64
	diskKnown := false
	if opts.site {
		dir, err := siteDir(roots, a["site"])
		if err != nil {
			return nil, err
		}
		if ok, _, err := DetectWordPress(ctx, c, dir); err != nil || !ok {
			return nil, fmt.Errorf("روی %s (%s) وردپرسی پیدا نشد؛ شاید قبلاً حذف شده. چیزی پاک نشد — اگر سایت دیگر روی هاست نیست، پروژه را بدون تیک «سایت روی هاست» حذف کنید", a["site"], dir)
		}
		want = []string{dir}
		if u, l, err := c.DiskUsage(ctx); err == nil {
			before, limit, diskKnown = u, l, true
		}
	}
	rootOf := map[string]DocRoot{}
	for _, r := range roots {
		rootOf[path.Clean(r.Root)] = r
	}
	// Databases still used by installs that stay (so a shared database is never dropped).
	keepDB := map[string]bool{}
	removing := map[string]bool{}
	for _, d := range want {
		removing[d] = true
	}
	scanKeep := func(dir string) {
		if removing[dir] {
			return
		}
		if ok, _, _ := DetectWordPress(ctx, c, dir); ok {
			if cfg, err := readFile(ctx, rc, dir, "wp-config.php"); err == nil {
				if n, _ := wpConfigDB(cfg); n != "" {
					keepDB[n] = true
				}
			}
		}
	}
	for _, r := range roots {
		scanKeep(path.Clean(r.Root))
		if files, err := listDir(ctx, c, r.Root); err == nil {
			for _, f := range files {
				if f.Type == "dir" && !skipSubdirs[f.File] && !strings.HasPrefix(f.File, ".") {
					scanKeep(path.Join(r.Root, f.File))
				}
			}
		}
	}
	removed := 0
	var failed []string
	for _, dir := range want {
		label := dir
		if r, ok := rootOf[dir]; ok {
			label = r.Domain
		}
		rc.Logf("▶ %s (%s)", label, dir)
		if err := removeOne(ctx, rc, home, dir, roots, keepDB, opts); err != nil {
			rc.Logf("✘ %s: %v", label, err)
			failed = append(failed, label)
			continue
		}
		removed++
		rc.Logf("✔ %s کامل حذف شد", label)
		time.Sleep(200 * time.Millisecond)
	}
	out := map[string]string{"removed": fmt.Sprint(removed)}
	if diskKnown {
		out["disk_before"] = fmt.Sprintf("%.2f", before)
		out["disk_limit"] = fmt.Sprintf("%.2f", limit)
		if after, l, err := c.DiskUsage(ctx); err == nil {
			out["disk_after"], out["disk_limit"] = fmt.Sprintf("%.2f", after), fmt.Sprintf("%.2f", l)
			if before > after {
				rc.Logf("فضای آزادشده: حدود %.0f MB (اکنون %.0f MB مصرف)", before-after, after)
			}
		}
	}
	if len(failed) > 0 {
		return out, fmt.Errorf("حذف این‌ها کامل نشد: %s", strings.Join(failed, "، "))
	}
	return out, nil
}

func removeOne(ctx context.Context, rc *RunContext, home, dir string, roots []DocRoot, keepDB map[string]bool, opts removeOpts) error {
	c := rc.client
	if !strings.HasPrefix(dir, home+"/") {
		return fmt.Errorf("مسیر بیرون از حساب است")
	}
	ok, _, err := DetectWordPress(ctx, c, dir)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("اینجا وردپرسی نیست (شاید قبلاً حذف شده)")
	}
	cfg, err := readFile(ctx, rc, dir, "wp-config.php")
	if err != nil {
		return fmt.Errorf("خواندن wp-config.php: %w", err)
	}
	dbName, dbUser := wpConfigDB(cfg)
	prefix := rc.target.Options.Username + "_"

	// What else lives inside this folder: other domains' roots and other WordPress installs.
	var root *DocRoot
	inner := false
	for i, r := range roots {
		rr := path.Clean(r.Root)
		if rr == dir {
			root = &roots[i]
		} else if strings.HasPrefix(rr, dir+"/") {
			inner = true
		}
	}
	files, err := listDir(ctx, c, dir)
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.Type == "dir" && !wpOwnFile(f.File) && !skipSubdirs[f.File] {
			if ok, _, _ := DetectWordPress(ctx, c, path.Join(dir, f.File)); ok {
				inner = true
			}
		}
	}

	// 1. Subdomain of its own (for a project's site: only one wpauto created).
	ownSub := root != nil && root.Kind == "sub" && (!opts.site || opts.ownSubs[strings.ToLower(root.Domain)])
	if root != nil && root.Kind == "sub" && !ownSub {
		rc.Logf("ساب‌دامین %s را wpauto نساخته؛ خودش و پوشه‌اش می‌ماند و فقط وردپرس داخلش پاک می‌شود", root.Domain)
	} else if ownSub {
		if _, err := c.API2(ctx, "SubDomain", "delsubdomain", url.Values{"domain": {root.Domain}}); err != nil {
			return fmt.Errorf("حذف ساب‌دامین %s: %w", root.Domain, err)
		}
		rc.Logf("ساب‌دامین %s حذف شد", root.Domain)
	} else if root != nil {
		rc.Logf("دامنه‌ی %s (%s) خودش حذف نمی‌شود؛ فقط وردپرس داخلش", root.Domain, root.Kind)
	}

	// 2. Files.
	whole := !inner && (root == nil || ownSub)
	if whole && opts.site && root == nil {
		// a project's folder is deleted whole only when nothing but WordPress lives in it
		var other []string
		for _, f := range files {
			if !wpOwnFile(f.File) && f.File != "cgi-bin" && f.File != ".well-known" {
				other = append(other, f.File)
			}
		}
		if len(other) > 0 {
			whole = false
			rc.Logf("پوشه‌ی %s فایل‌های دیگری هم دارد (%s)؛ فقط فایل‌های وردپرس پاک می‌شود", dir, strings.Join(first(other, 5), ", "))
		}
	}
	if whole {
		if err := c.Remove(ctx, dir); err != nil {
			return fmt.Errorf("حذف پوشه: %w", err)
		}
		rc.Logf("پوشه‌ی %s حذف شد", dir)
	} else {
		var del []string
		for _, f := range files {
			if wpOwnFile(f.File) {
				del = append(del, path.Join(dir, f.File))
			}
		}
		if err := c.Remove(ctx, del...); err != nil {
			return fmt.Errorf("حذف فایل‌ها: %w", err)
		}
		rc.Logf("فایل‌های وردپرس از %s حذف شد (%d مورد)؛ پوشه‌های سایت‌های دیگر دست نخورد", dir, len(del))
	}

	// 3. Database and its user.
	switch {
	case dbName == "":
		rc.Logf("⚠ نام دیتابیس در wp-config.php پیدا نشد")
	case !strings.HasPrefix(dbName, prefix):
		rc.Logf("⚠ دیتابیس %s مال این حساب نیست؛ دست نخورد", dbName)
	case keepDB[dbName]:
		rc.Logf("⚠ دیتابیس %s را وردپرس دیگری هم استفاده می‌کند؛ دست نخورد", dbName)
	default:
		if _, err := c.Call(ctx, "Mysql", "delete_database", url.Values{"name": {dbName}}); err != nil {
			return fmt.Errorf("حذف دیتابیس %s: %w", dbName, err)
		}
		rc.Logf("دیتابیس %s حذف شد", dbName)
		if dbUser != "" && strings.HasPrefix(dbUser, prefix) && !userHasDatabases(ctx, rc, dbUser) {
			if _, err := c.Call(ctx, "Mysql", "delete_user", url.Values{"name": {dbUser}}); err != nil {
				return fmt.Errorf("حذف کاربر دیتابیس %s: %w", dbUser, err)
			}
			rc.Logf("کاربر دیتابیس %s حذف شد", dbUser)
		} else if dbUser != "" {
			rc.Logf("کاربر دیتابیس %s به دیتابیس دیگری هم دسترسی دارد؛ نگه داشته شد", dbUser)
		}
	}
	return nil
}

// userHasDatabases reports whether a MySQL user still has privileges on any database. When the
// list cannot be read it answers true, so the user is kept.
func userHasDatabases(ctx context.Context, rc *RunContext, user string) bool {
	raw, err := rc.client.Call(ctx, "Mysql", "list_databases", nil)
	if err != nil {
		return true
	}
	var dbs []struct {
		Users []string `json:"users"`
	}
	if json.Unmarshal(raw, &dbs) != nil {
		return true
	}
	for _, d := range dbs {
		for _, u := range d.Users {
			if u == user {
				return true
			}
		}
	}
	return false
}
