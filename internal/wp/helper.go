// Package wp holds the WordPress-specific pieces of an install: the temporary PHP helper that runs
// on the host, the wp-config.php/.htaccess generators and an HTTP client that reaches a new site
// before its DNS or SSL certificate is ready.
package wp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"strings"
)

// RandHex returns n random bytes as hex.
func RandHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// RandString returns a random string from alphabet.
func RandString(n int, alphabet string) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		k, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		sb.WriteByte(alphabet[k.Int64()])
	}
	return sb.String()
}

// Alphabets.
const (
	Lower    = "abcdefghijklmnopqrstuvwxyz0123456789"
	Password = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	Salt     = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!#%()*+,-.:;<=>?@[]^_{|}~"
)

// Job is the parameter file the helper reads. It is written through cPanel (TLS), so secrets never
// travel in helper URLs.
type Job struct {
	ZipURL     string `json:"zip_url"`
	Title      string `json:"title"`
	AdminUser  string `json:"admin_user"`
	AdminEmail string `json:"admin_email"`
	AdminPass  string `json:"admin_pass"`
	Locale     string `json:"locale"`
	URL        string `json:"url"`
	Public     bool   `json:"public"`
}

// JSON encodes the job.
func (j Job) JSON() string {
	b, _ := json.Marshal(j)
	return string(b)
}

// ZipFile and TempDir are the WordPress download and the folder it is extracted into, both
// next to the helper (removed on cleanup).
const (
	ZipFile = ".wpauto-wp.zip"
	TempDir = ".wpauto-x"
)

// PkgDir is the folder next to the helper that receives uploaded zips (removed on cleanup).
func PkgDir(token string) string { return ".wpauto-pkg-" + token[:16] }

// Helper file names for a token.
func HelperName(token string) string { return "wpauto-" + token[:16] + ".php" }

// JobName is the job file next to the helper.
func JobName(token string) string { return ".wpauto-job-" + token[:16] + ".json" }

// ZipURL returns the official download for a locale.
func ZipURL(locale string) string {
	switch locale {
	case "", "en_US":
		return "https://wordpress.org/latest.zip"
	case "fa_IR":
		return "https://fa.wordpress.org/latest-fa_IR.zip"
	default:
		lang := strings.ToLower(strings.SplitN(locale, "_", 2)[0])
		return "https://" + lang + ".wordpress.org/latest-" + locale + ".zip"
	}
}

// HelperPHP returns the helper script for token. Every request must carry ?t=<token>; the script
// deletes itself on action=cleanup and the engine always calls cleanup at the end of a run.
func HelperPHP(token string) string {
	return strings.ReplaceAll(helperSrc, "__TOKEN__", token)
}

const helperSrc = `<?php
// wpauto temporary helper. Removed automatically when the install finishes.
// Hosts often list functions in disable_functions; calling one is a fatal error in PHP 8,
// so every optional function is guarded.
function wpauto_fn($f) {
    if (!function_exists($f)) { return false; }
    $d = (string)ini_get('disable_functions');
    return $d === '' || !in_array($f, array_map('trim', explode(',', $d)), true);
}
if (wpauto_fn('set_time_limit')) { @set_time_limit(600); }
if (wpauto_fn('ignore_user_abort')) { @ignore_user_abort(true); }
if (wpauto_fn('ini_set')) { @ini_set('display_errors', '0'); }
header('Content-Type: application/json; charset=utf-8');
header('Cache-Control: no-store');
header('X-Robots-Tag: noindex');
$token = '__TOKEN__';
if (!isset($_GET['t']) || !hash_equals($token, (string)$_GET['t'])) { http_response_code(404); exit; }
$dir = __DIR__;
$jobFile = $dir . '/.wpauto-job-' . substr($token, 0, 16) . '.json';
$zip = $dir . '/.wpauto-wp.zip';
$tmp = $dir . '/.wpauto-x';
$job = is_file($jobFile) ? json_decode((string)file_get_contents($jobFile), true) : array();
if (!is_array($job)) { $job = array(); }

function wpauto_out($ok, $data = array()) {
    while (ob_get_level() > 0) { @ob_end_clean(); }
    echo json_encode(array_merge(array('ok' => $ok), $data));
    exit;
}
function wpauto_arg($k) {
    $v = isset($_GET[$k]) ? (string)$_GET[$k] : '';
    if (!preg_match('/^[a-z0-9][a-z0-9_.-]{0,80}$/', $v) || strpos($v, '..') !== false) { wpauto_out(false, array('error' => 'bad argument: ' . $k)); }
    return $v;
}
function wpauto_download($url, $dest) {
    @unlink($dest);
    if (wpauto_fn('curl_init') && wpauto_fn('curl_exec')) {
        $fp = fopen($dest, 'wb');
        $ch = curl_init($url);
        curl_setopt_array($ch, array(CURLOPT_FILE => $fp, CURLOPT_FOLLOWLOCATION => true, CURLOPT_CONNECTTIMEOUT => 20,
            CURLOPT_TIMEOUT => 300, CURLOPT_USERAGENT => 'wpauto', CURLOPT_FAILONERROR => true));
        $ok = curl_exec($ch);
        $code = curl_getinfo($ch, CURLINFO_HTTP_CODE);
        $err = curl_error($ch);
        curl_close($ch);
        fclose($fp);
        if (!$ok) { @unlink($dest); return 'download failed: HTTP ' . $code . ' ' . $err; }
        return '';
    }
    $ctx = stream_context_create(array('http' => array('timeout' => 300, 'user_agent' => 'wpauto')));
    if (!@copy($url, $dest, $ctx)) { @unlink($dest); return 'download failed (no curl)'; }
    return '';
}
// wpauto_unzip_folder extracts a zip whose single top folder is $slug into $parent/$slug.
// It never overwrites: an existing folder is reported and left alone.
function wpauto_unzip_folder($zipPath, $parent, $slug) {
    $target = $parent . '/' . $slug;
    if (file_exists($target)) { return 'exists'; }
    if (!class_exists('ZipArchive')) { return 'ZipArchive is not available in PHP'; }
    if (!is_file($zipPath)) { return 'zip file missing'; }
    $z = new ZipArchive();
    if ($z->open($zipPath) !== true) { return 'cannot open zip'; }
    for ($i = 0; $i < $z->numFiles; $i++) {
        $n = str_replace('\\', '/', (string)$z->getNameIndex($i));
        if (strpos($n, '__MACOSX/') === 0) { continue; }
        if (strpos($n, '..') !== false || substr($n, 0, 1) === '/' || strpos($n, $slug . '/') !== 0) {
            $z->close();
            return 'unexpected path in zip: ' . $n;
        }
    }
    $tmp = $parent . '/.wpauto-tmp-' . $slug;
    wpauto_rrmdir($tmp);
    if (!$z->extractTo($tmp)) { $z->close(); wpauto_rrmdir($tmp); return 'extract failed (disk full?)'; }
    $z->close();
    if (!@rename($tmp . '/' . $slug, $target)) { wpauto_rrmdir($tmp); return 'cannot move ' . $slug; }
    wpauto_rrmdir($tmp);
    return true;
}
function wpauto_plugin_file($slug) {
    $list = get_plugins('/' . $slug);
    if (!$list) { return ''; }
    $files = array_keys($list);
    // Prefer slug/slug.php when a folder holds several plugin files.
    foreach ($files as $f) { if ($f === $slug . '.php') { return $slug . '/' . $f; } }
    return $slug . '/' . $files[0];
}
function wpauto_sdi_state($r) {
    if (is_wp_error($r)) {
        $d = $r->get_error_data();
        wpauto_out(false, array('error' => $r->get_error_message(), 'retry' => is_array($d) && !empty($d['retry']),
            'log' => (is_array($d) && isset($d['log'])) ? $d['log'] : array()));
    }
    wpauto_out(true, array('state' => $r));
}
function wpauto_rrmdir($p) {
    if (is_link($p) || is_file($p)) { @unlink($p); return; }
    if (!is_dir($p)) { return; }
    foreach (scandir($p) as $f) { if ($f !== '.' && $f !== '..') { wpauto_rrmdir($p . '/' . $f); } }
    @rmdir($p);
}
register_shutdown_function(function () {
    $e = error_get_last();
    if ($e && in_array($e['type'], array(E_ERROR, E_PARSE, E_CORE_ERROR, E_COMPILE_ERROR), true)) {
        echo json_encode(array('ok' => false, 'error' => 'php fatal: ' . $e['message']));
    }
});

$a = isset($_GET['a']) ? $_GET['a'] : '';
$pkg = $dir . '/.wpauto-pkg-' . substr($token, 0, 16);
// Actions on an existing WordPress load it here, in global scope, as the first administrator.
if (in_array($a, array('wp_info', 'theme_install', 'theme_activate', 'theme_remove', 'plugin_fetch', 'plugin_install',
    'plugin_activate', 'plugin_remove', 'plugin_upgrade', 'plugin_restore', 'plugin_backup_remove', 'demo_prepare', 'demo_start', 'demo_step', 'demo_remove', 'demo_cancel'), true)) {
    if (!is_file($dir . '/wp-load.php') || !is_file($dir . '/wp-config.php')) { wpauto_out(false, array('error' => 'WordPress is not installed in this folder')); }
    ob_start();
    require_once $dir . '/wp-load.php';
    require_once ABSPATH . 'wp-admin/includes/admin.php';
    $wpauto_admins = get_users(array('role' => 'administrator', 'number' => 1, 'orderby' => 'ID', 'fields' => 'ID'));
    if ($wpauto_admins) { wp_set_current_user((int)$wpauto_admins[0]); }
    if (wpauto_fn('set_time_limit')) { @set_time_limit(300); }
}
switch ($a) {
case 'ping':
    wpauto_out(true, array(
        'php' => PHP_VERSION,
        'zip' => class_exists('ZipArchive'),
        'curl' => wpauto_fn('curl_init') && wpauto_fn('curl_exec'),
        'mysqli' => extension_loaded('mysqli'),
        'free_mb' => wpauto_fn('disk_free_space') ? (int)(@disk_free_space($dir) / 1048576) : -1,
        'writable' => is_writable($dir),
        'memory_limit' => ini_get('memory_limit'),
    ));

case 'download':
    if (empty($job['zip_url'])) { wpauto_out(false, array('error' => 'job has no zip_url')); }
    $err = wpauto_download($job['zip_url'], $zip);
    if ($err !== '') { wpauto_out(false, array('error' => $err)); }
    wpauto_out(true, array('bytes' => filesize($zip)));

case 'extract_chunk':
    // Extracts entries [from, from+n) of the WordPress zip into .wpauto-x, so no single request
    // runs long enough for the host to kill it. from=0 starts over with an empty folder.
    if (!class_exists('ZipArchive')) { wpauto_out(false, array('error' => 'ZipArchive is not available in PHP')); }
    if (!is_file($zip)) { wpauto_out(false, array('error' => 'zip file missing')); }
    $from = (isset($_GET['from']) && ctype_digit((string)$_GET['from'])) ? (int)$_GET['from'] : -1;
    if ($from < 0) { wpauto_out(false, array('error' => 'bad argument: from')); }
    $n = (isset($_GET['n']) && ctype_digit((string)$_GET['n'])) ? (int)$_GET['n'] : 300;
    if ($n < 1 || $n > 5000) { $n = 300; }
    $z = new ZipArchive();
    if ($z->open($zip) !== true) { wpauto_out(false, array('error' => 'cannot open zip (corrupt download?)')); }
    $total = $z->numFiles;
    if ($from === 0) { wpauto_rrmdir($tmp); }
    if (!is_dir($tmp) && !@mkdir($tmp, 0755)) { $z->close(); wpauto_out(false, array('error' => 'cannot create ' . basename($tmp))); }
    $end = min($total, $from + $n);
    $names = array();
    for ($i = $from; $i < $end; $i++) {
        $nm = (string)$z->getNameIndex($i);
        $c = str_replace('\\', '/', $nm);
        if (substr($c, 0, 1) === '/' || preg_match('#(^|/)\.\.(/|$)#', $c)) { $z->close(); wpauto_out(false, array('error' => 'unexpected path in zip: ' . $c)); }
        $names[] = $nm;
    }
    if ($names && !$z->extractTo($tmp, $names)) { $z->close(); wpauto_out(false, array('error' => 'extract failed (disk full?)')); }
    $z->close();
    wpauto_out(true, array('next' => $end, 'total' => $total, 'done' => $end >= $total));

case 'place':
    // Moves the extracted .wpauto-x/wordpress/* into this folder (renames only, so it is quick)
    // after checking that the extraction is complete and that nothing would be overwritten.
    $src = $tmp . '/wordpress';
    if (!is_dir($src)) { wpauto_out(false, array('error' => 'unexpected zip layout', 'redo' => true)); }
    if (is_file($zip) && class_exists('ZipArchive')) {
        $z = new ZipArchive();
        if ($z->open($zip) === true) {
            $missing = 0;
            $firstMissing = '';
            for ($i = 0; $i < $z->numFiles; $i++) {
                $st = $z->statIndex($i);
                $nm = str_replace('\\', '/', (string)$st['name']);
                if (substr($nm, -1) === '/') { continue; }
                $p = $tmp . '/' . $nm;
                if (!is_file($p) || filesize($p) !== (int)$st['size']) {
                    $missing++;
                    if ($firstMissing === '') { $firstMissing = $nm; }
                }
            }
            $z->close();
            if ($missing > 0) { wpauto_out(false, array('error' => 'incomplete extraction: ' . $missing . ' files missing or short (' . $firstMissing . ')', 'redo' => true)); }
        }
    }
    $conflicts = array();
    foreach (scandir($src) as $f) {
        if ($f === '.' || $f === '..') { continue; }
        if (file_exists($dir . '/' . $f)) { $conflicts[] = $f; }
    }
    if ($conflicts) { wpauto_rrmdir($tmp); wpauto_out(false, array('error' => 'files already exist: ' . implode(', ', $conflicts))); }
    $moved = 0;
    foreach (scandir($src) as $f) {
        if ($f === '.' || $f === '..') { continue; }
        if (!@rename($src . '/' . $f, $dir . '/' . $f)) { wpauto_out(false, array('error' => 'cannot move ' . $f)); }
        $moved++;
    }
    wpauto_rrmdir($tmp);
    @unlink($zip);
    wpauto_out(true, array('moved' => $moved));

case 'install':
    if (!is_file($dir . '/wp-load.php')) { wpauto_out(false, array('error' => 'WordPress files are missing')); }
    if (!is_file($dir . '/wp-config.php')) { wpauto_out(false, array('error' => 'wp-config.php is missing')); }
    define('WP_INSTALLING', true);
    $_SERVER['HTTPS'] = (isset($job['url']) && strpos($job['url'], 'https://') === 0) ? 'on' : (isset($_SERVER['HTTPS']) ? $_SERVER['HTTPS'] : '');
    require_once $dir . '/wp-load.php';
    require_once ABSPATH . 'wp-admin/includes/upgrade.php';
    if (is_blog_installed()) { wpauto_out(false, array('error' => 'WordPress is already installed in this database')); }
    $r = wp_install($job['title'], $job['admin_user'], $job['admin_email'], !empty($job['public']), '', wp_slash($job['admin_pass']), $job['locale']);
    if (is_wp_error($r)) { wpauto_out(false, array('error' => $r->get_error_message())); }
    if (!empty($job['url'])) {
        update_option('siteurl', untrailingslashit($job['url']));
        update_option('home', untrailingslashit($job['url']));
    }
    update_option('permalink_structure', '/%postname%/');
    wpauto_out(true, array('user_id' => isset($r['user_id']) ? $r['user_id'] : 0, 'version' => get_bloginfo('version')));

case 'wp_info':
    $plugins = array();
    foreach (get_plugins() as $file => $p) {
        $slug = strpos($file, '/') !== false ? dirname($file) : basename($file, '.php');
        $plugins[$slug] = array('file' => $file, 'version' => $p['Version'], 'active' => is_plugin_active($file));
    }
    $themes = array();
    foreach (wp_get_themes() as $st => $t) { $themes[$st] = $t->get('Version'); }
    wpauto_out(true, array('version' => get_bloginfo('version'), 'url' => home_url('/'), 'stylesheet' => get_stylesheet(),
        'template' => get_template(), 'themes' => $themes, 'plugins' => $plugins, 'php' => PHP_VERSION));

case 'plugin_fetch':
    $slug = wpauto_arg('slug');
    if (!is_dir($pkg) && !@mkdir($pkg, 0755)) { wpauto_out(false, array('error' => 'cannot create package folder')); }
    $err = wpauto_download('https://downloads.wordpress.org/plugin/' . $slug . '.latest-stable.zip', $pkg . '/' . $slug . '.zip');
    if ($err !== '') { wpauto_out(false, array('error' => $err)); }
    wpauto_out(true, array('bytes' => filesize($pkg . '/' . $slug . '.zip')));

case 'theme_install':
case 'plugin_install':
    $slug = wpauto_arg('slug');
    $f = wpauto_arg('f');
    $parent = $a === 'theme_install' ? get_theme_root() : WP_PLUGIN_DIR;
    $r = wpauto_unzip_folder($pkg . '/' . $f, $parent, $slug);
    @unlink($pkg . '/' . $f);
    if ($r === 'exists') { wpauto_out(true, array('created' => false)); }
    if ($r !== true) { wpauto_out(false, array('error' => $r)); }
    if ($a === 'theme_install') { wp_clean_themes_cache(); } else { wp_clean_plugins_cache(); }
    wpauto_out(true, array('created' => true));

case 'plugin_activate':
    $slug = wpauto_arg('slug');
    $file = wpauto_plugin_file($slug);
    if ($file === '') { wpauto_out(false, array('error' => 'plugin ' . $slug . ' is not installed')); }
    if (is_plugin_active($file)) { wpauto_out(true, array('file' => $file, 'already' => true)); }
    $r = activate_plugin($file);
    if (is_wp_error($r)) { wpauto_out(false, array('error' => $r->get_error_message())); }
    wpauto_out(true, array('file' => $file, 'already' => false));

case 'plugin_remove':
    $slug = wpauto_arg('slug');
    $file = wpauto_plugin_file($slug);
    if ($file !== '') { deactivate_plugins($file, true); }
    if (!empty($_GET['delete'])) { wpauto_rrmdir(WP_PLUGIN_DIR . '/' . $slug); }
    wpauto_out(true);

case 'plugin_upgrade':
    // Replaces an installed plugin with a newer zip. The old folder is moved (not copied) to
    // wp-content/upgrade/<b> so it can be put back exactly; a broken zip changes nothing.
    $slug = wpauto_arg('slug');
    $f = wpauto_arg('f');
    $b = wpauto_arg('b');
    if (!preg_match('/^wpauto-backup-[A-Za-z0-9._-]+$/', $b)) { wpauto_out(false, array('error' => 'bad backup name')); }
    $cur = WP_PLUGIN_DIR . '/' . $slug;
    if (!is_dir($cur)) { @unlink($pkg . '/' . $f); wpauto_out(false, array('error' => 'plugin ' . $slug . ' is not installed')); }
    $up = WP_CONTENT_DIR . '/upgrade';
    if (!is_dir($up) && !@mkdir($up, 0755)) { @unlink($pkg . '/' . $f); wpauto_out(false, array('error' => 'cannot create wp-content/upgrade')); }
    $bak = $up . '/' . $b;
    if (file_exists($bak)) { @unlink($pkg . '/' . $f); wpauto_out(false, array('error' => 'backup folder already exists')); }
    $stage = WP_PLUGIN_DIR . '/.wpauto-new-' . $slug;
    wpauto_rrmdir($stage);
    if (!@mkdir($stage, 0755)) { @unlink($pkg . '/' . $f); wpauto_out(false, array('error' => 'cannot create staging folder')); }
    $r = wpauto_unzip_folder($pkg . '/' . $f, $stage, $slug);
    @unlink($pkg . '/' . $f);
    if ($r !== true) { wpauto_rrmdir($stage); wpauto_out(false, array('error' => $r)); }
    $old = wpauto_plugin_file($slug);
    $active = $old !== '' && is_plugin_active($old);
    if (!@rename($cur, $bak)) { wpauto_rrmdir($stage); wpauto_out(false, array('error' => 'cannot back up ' . $slug . '; nothing changed')); }
    if (!@rename($stage . '/' . $slug, $cur)) {
        @rename($bak, $cur);
        wpauto_rrmdir($stage);
        wpauto_out(false, array('error' => 'cannot put the new version in place; old version kept'));
    }
    wpauto_rrmdir($stage);
    wp_clean_plugins_cache();
    $new = wpauto_plugin_file($slug);
    if ($active && $new !== '' && $new !== $old) {
        // the main file was renamed in the new version: keep the plugin active under its new name
        deactivate_plugins($old, true);
        $ar = activate_plugin($new, '', false, true);
        if (is_wp_error($ar)) {
            wpauto_rrmdir($cur); @rename($bak, $cur); wp_clean_plugins_cache(); activate_plugin($old, '', false, true);
            wpauto_out(false, array('error' => 'new version could not be activated: ' . $ar->get_error_message() . '; old version restored'));
        }
    }
    $pl = $new !== '' ? get_plugin_data(WP_PLUGIN_DIR . '/' . $new, false, false) : array();
    wpauto_out(true, array('backup' => 'wp-content/upgrade/' . $b, 'was_active' => $active, 'old_file' => $old, 'file' => $new,
        'version' => isset($pl['Version']) ? $pl['Version'] : ''));

case 'plugin_restore':
    // Puts a plugin_upgrade backup back exactly as it was, active again when it was active.
    $slug = wpauto_arg('slug');
    $b = wpauto_arg('b');
    if (!preg_match('/^wpauto-backup-[A-Za-z0-9._-]+$/', $b)) { wpauto_out(false, array('error' => 'bad backup name')); }
    $bak = WP_CONTENT_DIR . '/upgrade/' . $b;
    if (!is_dir($bak)) { wpauto_out(false, array('error' => 'backup wp-content/upgrade/' . $b . ' not found')); }
    $cur = WP_PLUGIN_DIR . '/' . $slug;
    $newFile = wpauto_plugin_file($slug);
    $trash = WP_PLUGIN_DIR . '/.wpauto-old-' . $slug;
    wpauto_rrmdir($trash);
    if (is_dir($cur) && !@rename($cur, $trash)) { wpauto_out(false, array('error' => 'cannot move the new version away')); }
    if (!@rename($bak, $cur)) {
        if (is_dir($trash)) { @rename($trash, $cur); }
        wpauto_out(false, array('error' => 'cannot move the backup back; it is still in wp-content/upgrade/' . $b));
    }
    wpauto_rrmdir($trash);
    wp_clean_plugins_cache();
    $file = wpauto_plugin_file($slug);
    if ($newFile !== '' && $newFile !== $file && is_plugin_active($newFile)) { deactivate_plugins($newFile, true); }
    if (!empty($_GET['active']) && $file !== '' && !is_plugin_active($file)) {
        $ar = activate_plugin($file, '', false, true);
        if (is_wp_error($ar)) { wpauto_out(false, array('error' => 'restored but not re-activated: ' . $ar->get_error_message())); }
    }
    $pl = $file !== '' ? get_plugin_data(WP_PLUGIN_DIR . '/' . $file, false, false) : array();
    wpauto_out(true, array('version' => isset($pl['Version']) ? $pl['Version'] : ''));

case 'plugin_backup_remove':
    $b = wpauto_arg('b');
    if (!preg_match('/^wpauto-backup-[A-Za-z0-9._-]+$/', $b)) { wpauto_out(false, array('error' => 'bad backup name')); }
    wpauto_rrmdir(WP_CONTENT_DIR . '/upgrade/' . $b);
    wpauto_out(true);

case 'theme_activate':
    $slug = wpauto_arg('slug');
    $th = wp_get_theme($slug);
    if (!$th->exists()) { wpauto_out(false, array('error' => 'theme ' . $slug . ' is not installed')); }
    if ($th->errors()) { wpauto_out(false, array('error' => 'theme is broken: ' . $th->errors()->get_error_message())); }
    $prev = get_stylesheet();
    if ($prev !== $slug) { switch_theme($slug); }
    wpauto_out(true, array('previous' => $prev, 'name' => $th->get('Name'), 'version' => $th->get('Version')));

case 'theme_remove':
    $slug = wpauto_arg('slug');
    if (get_stylesheet() === $slug || get_template() === $slug) { wpauto_out(false, array('error' => 'theme is active')); }
    wpauto_rrmdir(get_theme_root() . '/' . $slug);
    wpauto_out(true);

case 'demo_prepare':
    $slug = wpauto_arg('slug');
    $f = wpauto_arg('f');
    if (!class_exists('SDI_Sources')) { wpauto_out(false, array('error' => 'Saayeh Demo Importer is not active')); }
    $r = SDI_Sources::install_zip($slug, $pkg . '/' . $f, '', 'local');
    @unlink($pkg . '/' . $f);
    if (is_wp_error($r)) { wpauto_out(false, array('error' => $r->get_error_message())); }
    SDI_Themes::reset();
    wpauto_out(true, array('version' => isset($r['version']) ? $r['version'] : ''));

case 'demo_start':
    $slug = wpauto_arg('slug');
    if (!class_exists('SDI_Ajax')) { wpauto_out(false, array('error' => 'Saayeh Demo Importer is not active')); }
    $groups = array();
    foreach (explode(',', isset($_GET['groups']) ? (string)$_GET['groups'] : '') as $g) { $g = sanitize_key($g); if ($g !== '') { $groups[] = $g; } }
    if (!$groups) { $groups = array_keys(SDI_Importer::option_groups()); }
    wpauto_sdi_state(SDI_Ajax::start($slug, $groups, false));

case 'demo_step':
    if (!class_exists('SDI_Ajax')) { wpauto_out(false, array('error' => 'Saayeh Demo Importer is not active')); }
    wpauto_sdi_state(SDI_Ajax::step());

case 'demo_remove':
    if (!class_exists('SDI_Ajax')) { wpauto_out(false, array('error' => 'Saayeh Demo Importer is not active')); }
    wpauto_sdi_state(SDI_Ajax::remove_batch(wpauto_arg('slug')));

case 'demo_cancel':
    if (class_exists('SDI_State')) { SDI_State::clear_run(); SDI_State::unlock(); }
    wpauto_out(true);

case 'cleanup':
    wpauto_rrmdir($pkg);
    @unlink($jobFile);
    @unlink($zip);
    wpauto_rrmdir($tmp);
    @unlink(__FILE__);
    wpauto_out(true);
}
http_response_code(400);
wpauto_out(false, array('error' => 'unknown action'));
`
