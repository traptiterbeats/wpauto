package wp

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	srv := exec.Command(php, "-S", addr, "-t", dir)
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
	if _, err := s.Helper(ctx, HelperName(token), token, "cleanup"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{HelperName(token), JobName(token), ".wpauto-wp.zip", ".wpauto-x"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("%s left behind", f)
		}
	}
	fmt.Println("e2e ok")
}
