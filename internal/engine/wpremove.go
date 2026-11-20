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
	Register("wp.remove", "permanently deletes test WordPress installs: files, database, database user and their own subdomain (arg: sites, one directory per line)", stepWPRemove)
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
	if len(want) == 0 {
		return nil, errors.New("هیچ وردپرسی انتخاب نشده")
	}
	roots, err := DocRoots(ctx, c)
	if err != nil {
		return nil, err
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
		if err := removeOne(ctx, rc, home, dir, roots, keepDB); err != nil {
			rc.Logf("✘ %s: %v", label, err)
			failed = append(failed, label)
			continue
		}
		removed++
		rc.Logf("✔ %s کامل حذف شد", label)
		time.Sleep(200 * time.Millisecond)
	}
	out := map[string]string{"removed": fmt.Sprint(removed)}
	if len(failed) > 0 {
		return out, fmt.Errorf("حذف این‌ها کامل نشد: %s", strings.Join(failed, "، "))
	}
	return out, nil
}

func removeOne(ctx context.Context, rc *RunContext, home, dir string, roots []DocRoot, keepDB map[string]bool) error {
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

	// 1. Subdomain of its own.
	if root != nil && root.Kind == "sub" {
		if _, err := c.API2(ctx, "SubDomain", "delsubdomain", url.Values{"domain": {root.Domain}}); err != nil {
			return fmt.Errorf("حذف ساب‌دامین %s: %w", root.Domain, err)
		}
		rc.Logf("ساب‌دامین %s حذف شد", root.Domain)
	} else if root != nil {
		rc.Logf("دامنه‌ی %s (%s) خودش حذف نمی‌شود؛ فقط وردپرس داخلش", root.Domain, root.Kind)
	}

	// 2. Files.
	whole := !inner && (root == nil || root.Kind == "sub")
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
