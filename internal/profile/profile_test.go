package profile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuiltinsAndOverride(t *testing.T) {
	dir := t.TempDir()
	custom := "id: myhost\nname: My host\nport: 2087\npreferred_php: [\"8.2\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "myhost.yaml"), []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := LoadAll(dir)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].ID != "generic" {
		t.Fatalf("generic should be first, got %s", list[0].ID)
	}
	a, ok := Find(list, "autopilo")
	if !ok || a.Auth != "session" || a.PreferredPHP[0] != "8.3" {
		t.Fatalf("autopilo profile: %+v", a)
	}
	m, ok := Find(list, "myhost")
	if !ok || m.Port != 2087 || m.Auth != "session" || m.MaxLoginAttempts != 1 {
		t.Fatalf("custom profile defaults not applied: %+v", m)
	}
}

func TestInvalid(t *testing.T) {
	if _, err := Parse([]byte("id: x\nauth: magic\n"), "t"); err == nil {
		t.Fatal("expected error for bad auth")
	}
}

func TestForAddrMatch(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "sub.yaml"), []byte("id: sub\nname: sub\nmatch: [\"*.srv.autopilo.ir\"]\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "ip.yaml"), []byte("id: ip\nname: ip\nmatch: [\"127.0.0.1\", \" Other-Host.COM. \"]\n"), 0o600)
	list, err := LoadAll(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := Find(list, "autopilo"); len(a.Match) != 1 || a.Match[0] != "autopilo.ir" {
		t.Fatalf("builtin autopilo match: %v", a.Match)
	}
	for _, c := range []struct{ addr, want string }{
		{"https://autopilo.ir:2083", "autopilo"},
		{"https://AutoPilo.IR:2083/", "autopilo"},
		{"autopilo.ir", "autopilo"},
		{"cp1.autopilo.ir:2083", "autopilo"},
		{"https://x.srv.autopilo.ir:2083", "sub"},    // the longer entry wins
		{"https://srv.autopilo.ir:2083", "autopilo"}, // ".srv…" fits only names under it
		{"https://notautopilo.ir:2083", "generic"},
		{"https://autopilo.ir.evil.com:2083", "generic"},
		{"https://127.0.0.1:44321", "ip"},
		{"other-host.com", "ip"},
		{"https://example.com:2083", "generic"},
		{"", "generic"},
		{"::bad::", "generic"},
	} {
		if p := ForAddr(list, c.addr); p == nil || p.ID != c.want {
			t.Errorf("ForAddr(%q) = %v, want %s", c.addr, p, c.want)
		}
	}
	if ForAddr(nil, "autopilo.ir") != nil {
		t.Error("empty list")
	}
}
