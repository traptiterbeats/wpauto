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
