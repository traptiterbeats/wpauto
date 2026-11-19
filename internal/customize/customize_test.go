package customize

import (
	"archive/zip"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/traptiterbeats/wpauto/internal/ai"
	"github.com/traptiterbeats/wpauto/internal/catalog/catalogtest"
)

// fakeAI answers every key with "NEW <original>" and records the prompts.
type fakeAI struct{ prompts []string }

func (f *fakeAI) Chat(ctx context.Context, system, user string, jsonMode bool) (string, ai.Usage, error) {
	f.prompts = append(f.prompts, user)
	i := strings.Index(user, "Fields to rewrite (JSON):\n")
	var items []map[string]string
	_ = json.Unmarshal([]byte(user[i+len("Fields to rewrite (JSON):\n"):]), &items)
	out := map[string]string{}
	for _, it := range items {
		v := "NEW " + it["original"]
		if it["kind"] == "html" {
			v = `<p onclick="x()">NEW</p><script>alert(1)</script>`
		}
		out[it["key"]] = v
	}
	b, _ := json.Marshal(out)
	return "```json\n" + string(b) + "\n```", ai.Usage{Prompt: 10, Completion: 5}, nil
}

func paths(fs []Field) string {
	var p []string
	for _, f := range fs {
		p = append(p, f.Path)
	}
	return strings.Join(p, " ")
}

func TestAutoFields(t *testing.T) {
	d, err := LoadDemo(catalogtest.RichDemo(t, t.TempDir(), "acme", false))
	if err != nil {
		t.Fatal(err)
	}
	want := "options.blogname options.blogdescription theme_mods.brand_text theme_mods.contact_email pages[slug=home].title pages[slug=home].content " +
		"posts[slug=first].title posts[slug=first].excerpt posts[slug=first].content posts[slug=first].meta.chip menus.0.items.0.title"
	if got := paths(d.Fields); got != want {
		t.Fatalf("fields:\n got %s\nwant %s", got, want)
	}
	if len(d.Images) != 1 || d.Images[0].ID != "hero" || d.LogoMod != "custom_logo" {
		t.Fatalf("images/logo: %+v %s", d.Images, d.LogoMod)
	}
	for _, f := range d.Fields {
		if f.Path == "theme_mods.contact_email" && (f.AI || !f.Data || f.Role != "contact.email") {
			t.Fatal("email must come from the brief")
		}
	}
}

func TestSpecFields(t *testing.T) {
	d, err := LoadDemo(catalogtest.RichDemo(t, t.TempDir(), "acme", true))
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(d.Fields); got != "theme_mods.brand_text posts[slug=first].title theme_mods.contact_email" {
		t.Fatalf("spec fields: %s", got)
	}
	if d.LogoMod != "brand_logo" || d.LogoText != "Logo" {
		t.Fatal("logo spec")
	}
}

func TestGenerateAndBuild(t *testing.T) {
	src := t.TempDir()
	demo, err := LoadDemo(catalogtest.RichDemo(t, src, "acme", false))
	if err != nil {
		t.Fatal(err)
	}
	st := Open(t.TempDir())
	d, err := st.New("acme", "Naranj Cafe")
	if err != nil {
		t.Fatal(err)
	}
	d.Brief = Brief{SiteName: "Naranj Cafe", Business: "cafe", Email: "hi@naranj.test"}
	fa := &fakeAI{}
	values, tokens, err := Generate(context.Background(), fa, demo, d.Brief, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tokens != 15 || len(fa.prompts) != 1 || !strings.Contains(fa.prompts[0], "Naranj Cafe") {
		t.Fatalf("tokens %d prompts %d", tokens, len(fa.prompts))
	}
	if values["theme_mods.brand_text"] != "NEW ACME STUDIO" || values["options.blogname"] != "Naranj Cafe" || values["theme_mods.contact_email"] != "hi@naranj.test" {
		t.Fatalf("values: %v", values)
	}
	if v := values["pages[slug=home].content"]; strings.Contains(v, "script") || strings.Contains(v, "onclick") || !strings.Contains(v, "<p>NEW</p>") {
		t.Fatalf("html not cleaned: %q", v)
	}
	d.Values = values
	d.Values["no.such.path"] = "ignored"
	if d.Logo, err = d.SaveImage("logo", []byte("\x89PNG\r\n\x1a\n0000000000000000")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SaveImage("x", []byte("<?php echo 1;")); err == nil {
		t.Fatal("non-image accepted")
	}
	img, _ := d.SaveImage("img-0", []byte("\xff\xd8\xff\xe0 jpeg data here"))
	d.Images["hero"] = img
	if err := st.Save(d); err != nil {
		t.Fatal(err)
	}
	out, err := Build(demo, d)
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	names := map[string]*zip.File{}
	for _, f := range z.File {
		names[f.Name] = f
	}
	for _, n := range []string{"manifest.json", "demo.json", "images/client-hero.jpg", "images/wpauto-logo.png"} {
		if names[n] == nil {
			t.Fatalf("%s missing from %v", n, names)
		}
	}
	if names["images/hero.png"] != nil {
		t.Fatal("replaced demo image still packed")
	}
	rc, _ := names["demo.json"].Open()
	b, _ := io.ReadAll(rc)
	rc.Close()
	var dj map[string]any
	if err := json.Unmarshal(b, &dj); err != nil {
		t.Fatal(err)
	}
	if v, _ := Get(dj, "posts[slug=first].title"); v != "NEW First project" {
		t.Fatalf("post title %q", v)
	}
	if v, _ := Get(dj, "options.blogname"); v != "Naranj Cafe" {
		t.Fatalf("blogname %q", v)
	}
	if v, _ := Get(dj, "theme_mods.custom_logo"); v != "@image:wpauto-logo" {
		t.Fatalf("logo mod %q", v)
	}
	if v, _ := Get(dj, "posts.0.meta.visual"); v != "v1" {
		t.Fatal("untouched value changed")
	}
	if dj["theme_mods"].(map[string]any)["count"] != float64(4) {
		t.Fatal("number changed")
	}
}

func TestSetGet(t *testing.T) {
	root := map[string]any{"posts": []any{map[string]any{"slug": "a", "title": "x"}}, "n": 1.0}
	if err := Set(root, "posts[slug=a].title", "y"); err != nil {
		t.Fatal(err)
	}
	if v, _ := Get(root, "posts.0.title"); v != "y" {
		t.Fatal(v)
	}
	if err := Set(root, "posts[slug=b].title", "y"); err == nil {
		t.Fatal("missing item must fail")
	}
	if err := Set(root, "n", "text"); err == nil {
		t.Fatal("overwriting a number must fail")
	}
	if err := Set(root, "options.blogname", "z"); err != nil {
		t.Fatal(err)
	}
}

func TestContract(t *testing.T) {
	d := &Demo{JSON: map[string]any{"theme_mods": map[string]any{"sp_social_instagram": "https://instagram.com/demo", "sp_social_x": "https://x.com/demo",
		"sp_email": "demo@demo.test", "sp_hero": "Old <span class=\"grad\">title</span>", "sp_accent": "#83B5FF"}}}
	d.Spec = &Spec{Fields: []SpecField{
		{Path: "theme_mods.sp_social_instagram", Role: "social.instagram"},
		{Path: "theme_mods.sp_social_x", Role: "social.x"},
		{Path: "theme_mods.sp_email", Role: "contact.email"},
		{Path: "theme_mods.sp_hero", Role: "hero.title", Kind: "inline", Allow: []string{"br", "span.grad"}},
		{Path: "theme_mods.sp_accent", Role: "color.primary"},
		{Path: "theme_mods.sp_brand", Role: "brand.latin"},
	}}
	d.scan()
	b := Brief{SiteName: "Naranj", Socials: map[string]string{"instagram": "@naranj.cafe"}}
	vals := map[string]string{}
	done := FromBrief(d.Fields, b, vals)
	if vals["theme_mods.sp_social_instagram"] != "https://instagram.com/naranj.cafe" {
		t.Fatalf("instagram: %v", vals)
	}
	if v, ok := vals["theme_mods.sp_social_x"]; !ok || v != "" {
		t.Fatal("unused demo social must be cleared")
	}
	if v, ok := vals["theme_mods.sp_email"]; !ok || v != "" {
		t.Fatal("demo email must be cleared")
	}
	if _, ok := vals["theme_mods.sp_accent"]; ok || done["theme_mods.sp_brand"] {
		t.Fatal("colour without brief colour and missing latin name must stay for the demo / AI")
	}
	var hero Field
	for _, f := range d.Fields {
		if f.Role == "hero.title" {
			hero = f
		}
		if f.Role == "brand.latin" && !f.AI {
			t.Fatal("latin brand falls back to AI")
		}
	}
	got := Clean(hero, `<h1 style="x">Fresh <span class="grad evil" onclick="x">coffee</span><br/><script>alert(1)</script><a href="javascript:x">y</a></h1>`)
	if got != `Fresh <span class="grad">coffee</span><br>y` {
		t.Fatalf("inline clean: %q", got)
	}
	if SocialURL("whatsapp", "۰۹۱۲ ۳۴۵ ۶۷۸۹") != "https://wa.me/989123456789" || SocialURL("telegram", "@abc") != "https://t.me/abc" {
		t.Fatal("social urls")
	}
	if Clean(Field{Kind: "color"}, "#abc") != "#ABC" || Clean(Field{Kind: "color"}, "red") != "" || Clean(Field{Kind: "url"}, "javascript:alert(1)") != "" {
		t.Fatal("clean kinds")
	}
	b.ColorPrimary = "#ff8800"
	vals = map[string]string{}
	FromBrief(d.Fields, b, vals)
	if vals["theme_mods.sp_accent"] != "#FF8800" {
		t.Fatal("brief colour")
	}
}
