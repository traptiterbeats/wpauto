package wp

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/traptiterbeats/wpauto/internal/catalog/catalogtest"
)

// stubSaayeh implements the part of Saayeh Demo Importer's PHP API that wpauto calls.
const stubSaayeh = `
class SDI_Themes { public static function reset() {} }
class SDI_State { public static function clear_run() { delete_option('stub_run'); } public static function unlock() {} }
class SDI_Importer { public static function option_groups() { return array('pages' => 'Pages', 'posts' => 'Posts'); } }
class SDI_Sources {
	public static function install_zip($slug, $zip, $sha = '', $origin = 'local') {
		if (!is_readable($zip)) { return new WP_Error('x', 'zip missing'); }
		update_option('stub_pkg', $slug);
		return array('version' => '1.0.0');
	}
}
class SDI_Ajax {
	public static function start($slug, $groups, $resume = false) {
		if (get_stylesheet() !== $slug) { return new WP_Error('sdi_inactive', 'theme not active'); }
		update_option('stub_run', array('n' => 0, 'slug' => $slug));
		return array('done' => false, 'label' => 'start', 'percent' => 0, 'log' => array());
	}
	public static function step() {
		$r = get_option('stub_run');
		if (!$r) { return new WP_Error('sdi_no_run', 'no run'); }
		$r['n']++;
		if ($r['n'] === 1) {
			$id = wp_insert_post(array('post_title' => 'Demo page', 'post_type' => 'page', 'post_status' => 'publish'));
			update_post_meta($id, '_sdi_demo', $r['slug']);
		}
		update_option('stub_run', $r);
		$done = $r['n'] >= 2;
		if ($done) { delete_option('stub_run'); }
		return array('done' => $done, 'label' => 'pages', 'percent' => $r['n'] * 50, 'log' => array(array('level' => 'info', 'message' => 'step ' . $r['n'])));
	}
	public static function remove_batch($slug) {
		foreach (get_posts(array('post_type' => 'any', 'meta_key' => '_sdi_demo', 'numberposts' => -1, 'fields' => 'ids')) as $id) { wp_delete_post($id, true); }
		return array('done' => true);
	}
}
`

// TestHelperEndToEnd runs the real PHP helper against a real WordPress download and MySQL.
// It runs in CI (see .github/workflows/ci.yml) when WPAUTO_E2E_DB is set ("user:pass@host/db").
func TestHelperEndToEnd(t *testing.T) {
	dsn := os.Getenv("WPAUTO_E2E_DB")
	if dsn == "" {
		t.Skip("WPAUTO_E2E_DB not set")
	}
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php not installed")
	}
	cred, rest, _ := strings.Cut(dsn, "@")
	user, pass, _ := strings.Cut(cred, ":")
	host, dbname, _ := strings.Cut(rest, "/")

	dir := t.TempDir()
	token := RandHex(24)
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(HelperName(token), HelperPHP(token))
	if out, err := exec.Command(php, "-l", filepath.Join(dir, HelperName(token))).CombinedOutput(); err != nil {
		t.Fatalf("php -l: %s", out)
	}

	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	// Mimic a locked-down shared host: functions commonly listed in disable_functions.
	srv := exec.Command(php, "-d", "disable_functions=disk_free_space,set_time_limit,ignore_user_abort,curl_exec,exec,shell_exec", "-S", addr, "-t", dir)
	srv.Stdout, srv.Stderr = os.Stdout, os.Stderr
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv.Process.Kill()
	time.Sleep(time.Second)

	siteURL := "http://" + addr
	write(JobName(token), Job{ZipURL: ZipURL("fa_IR"), Title: "تست", AdminUser: "boss", AdminEmail: "boss@example.com",
		AdminPass: "a-very-long-pass-123", Locale: "fa_IR", URL: siteURL, Public: true}.JSON())
	write("wp-config.php", Config(DBConfig{Name: dbname, User: user, Pass: pass, Host: host, TablePrefix: "wpt_"}))

	s := &Site{Host: "127.0.0.1", Path: "/", Addr: addr, Scheme: "http"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	for _, a := range []string{"ping", "download", "extract", "install"} {
		res, err := s.Helper(ctx, HelperName(token), token, a)
		if err != nil {
			t.Fatalf("%s: %v", a, err)
		}
		t.Logf("%s → %v", a, res)
	}
	if _, err := s.Helper(ctx, HelperName(token), "wrong-token-wrong-token-wrong-token-wrong-to", "ping"); err == nil {
		t.Fatal("wrong token accepted")
	}
	code, body, err := s.Get(ctx, "/")
	if err != nil || code != 200 || !strings.Contains(body, "wp-content") {
		t.Fatalf("home: %d %v", code, err)
	}
	code, _, _ = s.Get(ctx, "/wp-login.php")
	if code != 200 {
		t.Fatalf("login page: %d", code)
	}
	// Phase 5: theme, plugins and demo on the WordPress just installed.
	pkg := filepath.Join(dir, PkgDir(token))
	if err := os.Mkdir(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	catalogtest.Theme(t, pkg, "mytheme")
	os.Rename(filepath.Join(pkg, "mytheme.zip"), filepath.Join(pkg, "mytheme-theme.zip"))
	catalogtest.Plugin(t, pkg, "saayeh-demo-importer", stubSaayeh)
	catalogtest.Demo(t, pkg, "mytheme")
	call := func(action string, kv ...string) map[string]any {
		t.Helper()
		v := url.Values{}
		for i := 0; i+1 < len(kv); i += 2 {
			v.Set(kv[i], kv[i+1])
		}
		res, err := s.HelperArgs(ctx, HelperName(token), token, action, v)
		if err != nil {
			t.Fatalf("%s %v: %v", action, kv, err)
		}
		t.Logf("%s %v → %v", action, kv, res)
		return res
	}
	info := call("wp_info")
	prev := info["stylesheet"].(string)
	if res := call("plugin_install", "slug", "saayeh-demo-importer", "f", "saayeh-demo-importer.zip"); res["created"] != true {
		t.Fatal("plugin not created")
	}
	if res := call("plugin_install", "slug", "saayeh-demo-importer", "f", "saayeh-demo-importer.zip"); res["created"] != false {
		t.Fatal("existing plugin must be left alone")
	}
	call("plugin_activate", "slug", "saayeh-demo-importer")
	call("plugin_fetch", "slug", "hello-dolly")
	call("plugin_install", "slug", "hello-dolly", "f", "hello-dolly.zip")
	call("plugin_activate", "slug", "hello-dolly")
	call("theme_install", "slug", "mytheme", "f", "mytheme-theme.zip")
	if res := call("theme_activate", "slug", "mytheme"); res["previous"] != prev {
		t.Fatalf("previous theme %v, want %s", res["previous"], prev)
	}
	call("demo_prepare", "slug", "mytheme", "f", "mytheme-demo.zip")
	st := call("demo_start", "slug", "mytheme")["state"].(map[string]any)
	for i := 0; st["done"] != true; i++ {
		if i > 10 {
			t.Fatal("demo did not finish")
		}
		st = call("demo_step")["state"].(map[string]any)
	}
	info = call("wp_info")
	pl := info["plugins"].(map[string]any)
	if info["stylesheet"] != "mytheme" || pl["saayeh-demo-importer"].(map[string]any)["active"] != true || pl["hello-dolly"].(map[string]any)["active"] != true {
		t.Fatalf("unexpected state: %v", info)
	}
	if _, body, _ := s.Get(ctx, "/"); !strings.Contains(body, "/themes/mytheme/") {
		t.Fatal("new theme not served on the home page")
	}
	if _, err := s.HelperArgs(ctx, HelperName(token), token, "theme_remove", url.Values{"slug": {"mytheme"}}); err == nil {
		t.Fatal("active theme must not be removable")
	}
	if _, err := s.HelperArgs(ctx, HelperName(token), token, "theme_install", url.Values{"slug": {"../x"}, "f": {"a.zip"}}); err == nil {
		t.Fatal("bad slug accepted")
	}
	// Rollback path.
	call("demo_remove", "slug", "mytheme")
	call("theme_activate", "slug", prev)
	call("theme_remove", "slug", "mytheme")
	call("plugin_remove", "slug", "hello-dolly", "delete", "1")
	if _, err := os.Stat(filepath.Join(dir, "wp-content/themes/mytheme")); err == nil {
		t.Fatal("theme not removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "wp-content/plugins/hello-dolly")); err == nil {
		t.Fatal("plugin not removed")
	}

	if _, err := s.Helper(ctx, HelperName(token), token, "cleanup"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{HelperName(token), JobName(token), ".wpauto-wp.zip", ".wpauto-x", PkgDir(token)} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("%s left behind", f)
		}
	}
	fmt.Println("e2e ok")
}
