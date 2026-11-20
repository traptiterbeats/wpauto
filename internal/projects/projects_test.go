package projects

import "testing"

func TestStore(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Project{Name: " "}); err == nil {
		t.Fatal("empty name accepted")
	}
	p, err := s.Add(Project{Name: "سیس"})
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := s.Active(); !ok || a.ID != p.ID {
		t.Fatal("new project not active")
	}
	_ = s.Update(p.ID, func(x *Project) { x.Domain, x.Folder = "example.com", "shop" })
	s2, _ := Open(dir)
	if g, ok := s2.Get(p.ID); !ok || g.Site() != "example.com/shop" {
		t.Fatalf("reload: %+v", g)
	}
	if err := s2.Delete(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Active(); ok {
		t.Fatal("deleted project still active")
	}
}
