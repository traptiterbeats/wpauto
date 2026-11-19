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
@set_time_limit(600);
@ignore_user_abort(true);
@ini_set('display_errors', '0');
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
    echo json_encode(array_merge(array('ok' => $ok), $data));
    exit;
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
switch ($a) {
case 'ping':
    wpauto_out(true, array(
        'php' => PHP_VERSION,
        'zip' => class_exists('ZipArchive'),
        'curl' => function_exists('curl_init'),
        'mysqli' => extension_loaded('mysqli'),
        'free_mb' => (int)(@disk_free_space($dir) / 1048576),
        'writable' => is_writable($dir),
        'memory_limit' => ini_get('memory_limit'),
    ));

case 'download':
    if (empty($job['zip_url'])) { wpauto_out(false, array('error' => 'job has no zip_url')); }
    @unlink($zip);
    if (function_exists('curl_init')) {
        $fp = fopen($zip, 'wb');
        $ch = curl_init($job['zip_url']);
        curl_setopt_array($ch, array(CURLOPT_FILE => $fp, CURLOPT_FOLLOWLOCATION => true, CURLOPT_CONNECTTIMEOUT => 20,
            CURLOPT_TIMEOUT => 300, CURLOPT_USERAGENT => 'wpauto', CURLOPT_FAILONERROR => true));
        $ok = curl_exec($ch);
        $code = curl_getinfo($ch, CURLINFO_HTTP_CODE);
        $err = curl_error($ch);
        curl_close($ch);
        fclose($fp);
        if (!$ok) { @unlink($zip); wpauto_out(false, array('error' => 'download failed: HTTP ' . $code . ' ' . $err)); }
    } else {
        $ctx = stream_context_create(array('http' => array('timeout' => 300, 'user_agent' => 'wpauto')));
        if (!@copy($job['zip_url'], $zip, $ctx)) { @unlink($zip); wpauto_out(false, array('error' => 'download failed (no curl)')); }
    }
    wpauto_out(true, array('bytes' => filesize($zip)));

case 'extract':
    if (!class_exists('ZipArchive')) { wpauto_out(false, array('error' => 'ZipArchive is not available in PHP')); }
    if (!is_file($zip)) { wpauto_out(false, array('error' => 'zip file missing')); }
    $z = new ZipArchive();
    if ($z->open($zip) !== true) { wpauto_out(false, array('error' => 'cannot open zip (corrupt download?)')); }
    wpauto_rrmdir($tmp);
    if (!$z->extractTo($tmp)) { $z->close(); wpauto_out(false, array('error' => 'extract failed (disk full?)')); }
    $z->close();
    $src = $tmp . '/wordpress';
    if (!is_dir($src)) { wpauto_rrmdir($tmp); wpauto_out(false, array('error' => 'unexpected zip layout')); }
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

case 'cleanup':
    @unlink($jobFile);
    @unlink($zip);
    wpauto_rrmdir($tmp);
    @unlink(__FILE__);
    wpauto_out(true);
}
http_response_code(400);
wpauto_out(false, array('error' => 'unknown action'));
`
