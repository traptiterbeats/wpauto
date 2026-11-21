package web

import (
	"archive/zip"
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"strings"
	"testing"

	"github.com/traptiterbeats/wpauto/internal/catalog"
	"github.com/traptiterbeats/wpauto/internal/catalog/catalogtest"
	"github.com/traptiterbeats/wpauto/internal/customize"
	"github.com/traptiterbeats/wpauto/internal/engine"
)

func testPhoto(t *testing.T, w, h int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{uint8(x), uint8(y), 90, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// addV2Theme adds the "shop" theme whose demo follows contract v2 (image slots and products).
func (e *apEnv) addV2Theme() {
	src := e.t.TempDir()
	if _, err := engine.Catalog().Add(catalog.AddInput{ThemeZip: catalogtest.Theme(e.t, src, "shop"), DemoZip: catalogtest.RichDemoV2(e.t, src, "shop")}); err != nil {
		e.t.Fatal(err)
	}
}

// postMultipart posts fields and files (field name → contents) and returns the final page.
func (e *apEnv) postMultipart(p string, fields map[string]string, files map[string][][]byte) string {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("csrf", e.csrf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	for k, list := range files {
		for i, b := range list {
			fw, _ := mw.CreateFormFile(k, fmt.Sprintf("%s-%d.png", k, i))
			_, _ = fw.Write(b)
		}
	}
	_ = mw.Close()
	r, err := e.cl.Post(e.base+p, mw.FormDataContentType(), &buf)
	if err != nil {
		e.t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	return string(b)
}

func TestAutopilotWithImages(t *testing.T) {
	e := newAPEnv(t)
	e.addV2Theme()
	form := e.get("autopilot")
	for _, want := range []string{`name="photos" multiple`, `id="ap_products"`, `value="shop" data-products="keep" data-slots="6" data-v2="1"`,
		`value="acme" data-products="" data-slots="1"`, "هوش مصنوعی محصولات نمونه بسازد", "محصولات دمو بمانند"} {
		if !strings.Contains(form, want) {
			t.Fatalf("form lacks %q", want)
		}
	}
	themes := e.get("themes")
	if !strings.Contains(themes, "🖼 6 جای تصویر · v2") || !strings.Contains(themes, "🛒 محصولات") || !strings.Contains(themes, "🖼 1 جای تصویر") {
		t.Fatal("themes page lacks slot count or products")
	}

	id := e.startWith(map[string]string{"theme": "shop", "products": "ai"}, [][]byte{testPhoto(t, 320, 180), testPhoto(t, 200, 200)})
	p := e.wait(id)
	if p.Status != apSuccess {
		t.Fatalf("status %s: %s\n%s", p.Status, p.Error, apLogText(p))
	}
	if p.In.Products != "ai" || p.In.Photos != 2 {
		t.Fatalf("inputs %+v", p.In)
	}
	if w := strings.Join(p.Stages[0].Warnings, " "); !strings.Contains(w, "2 تصویر محصول هنوز تصویر دمو است") {
		t.Fatalf("product photo warning missing: %q", w)
	}
	d, err := engine.Drafts().Get(p.DraftID)
	if err != nil || d.Products != "ai" || len(d.Uploads) != 2 || d.Images["hero"] != "slot-hero.webp" || d.Images["team"] != "slot-team.webp" {
		t.Fatalf("draft %v %+v", err, d)
	}
	data, _ := e.fake.FindFile("/shop-demo.zip")
	zr, err := zip.NewReader(strings.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal("customised demo not uploaded")
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(b)
	}
	for _, n := range []string{"images/client-hero.webp", "images/client-team.webp", "images/p-mug.jpg", "images/about.jpg"} {
		if _, ok := files[n]; !ok {
			t.Fatalf("%s not in the package", n)
		}
	}
	if !strings.HasPrefix(files["images/client-hero.webp"], "RIFF") {
		t.Fatal("slot image is not WebP")
	}
	js := files["demo.json"]
	if !strings.Contains(js, `"file": "images/client-hero.webp"`) || !strings.Contains(js, `"regular_price": "19"`) || strings.Contains(js, `"name": "Code Mug"`) {
		t.Fatalf("demo.json: images or products wrong:\n%s", js)
	}
}

func TestDraftImagesV2(t *testing.T) {
	e := newAPEnv(t)
	e.addV2Theme()
	e.post("customize/new", map[string][]string{"theme": {"shop"}, "title": {"Naranj"}})
	d := engine.Drafts().List()[0]
	page := e.get("customize/" + d.ID + "?step=images")
	for _, want := range []string{`name="photos" multiple`, "جاهای تصویر قالب", `value="keep" checked`, "aspect-ratio: 160 / 90", "priority 1"} {
		if !strings.Contains(page, want) {
			t.Fatalf("images step lacks %q\n%s", want, page[strings.Index(page, "جاهای"):][:600])
		}
	}
	// two photos in keep mode: they go to the site slots, never to product slots
	e.postMultipart("customize/"+d.ID+"/images", map[string]string{"products": "keep"}, map[string][][]byte{"photos": {testPhoto(t, 200, 200), testPhoto(t, 320, 180)}})
	d, _ = engine.Drafts().Get(d.ID)
	if d.Products != "keep" || len(d.Uploads) != 2 || d.Images["hero"] == "" || d.Images["team"] == "" || d.Images["p-mug"] != "" {
		t.Fatalf("keep: %+v %v", d.Uploads, d.Images)
	}
	page = e.get("customize/" + d.ID + "?step=images")
	if !strings.Contains(page, "محصولات دمو می‌مانند؛ این تصویر عوض نمی‌شود") || !strings.Contains(page, d.Uploads[0].Thumb) {
		t.Fatal("skipped product slot or thumbnails not shown")
	}
	// the user overrides the hero with the square photo and asks for the demo image on "team"
	e.postMultipart("customize/"+d.ID+"/images", map[string]string{"pick_0": d.Uploads[0].File, "pick_2": "demo"}, nil)
	d, _ = engine.Drafts().Get(d.ID)
	if p := d.Slots["hero"]; !p.Manual || p.Src != d.Uploads[0].File || d.Images["team"] != "" || !d.Slots["team"].Manual {
		t.Fatalf("override: %+v %v", d.Slots, d.Images)
	}
	// ai mode: the unused wide photo is placed again and the empty product photos are warned about
	e.postMultipart("customize/"+d.ID+"/images", map[string]string{"products": "ai"}, nil)
	page = e.get("customize/" + d.ID + "?step=images")
	if !strings.Contains(page, "تصویر محصول هنوز تصویر دمو است") {
		t.Fatal("product photo warning missing")
	}
	e.post("customize/"+d.ID+"/photo-del", map[string][]string{"file": {d.Uploads[0].File}})
	d, _ = engine.Drafts().Get(d.ID)
	if len(d.Uploads) != 1 || d.Images["hero"] != "" {
		t.Fatalf("photo-del: %v %v", d.Uploads, d.Images)
	}
	e.post("customize/"+d.ID+"/autoplace", map[string][]string{})
	d, _ = engine.Drafts().Get(d.ID)
	if d.Images["hero"] == "" {
		t.Fatalf("autoplace: %v %+v", d.Images, d.Slots)
	}
	if strings.Count(page, `method="post"`) != strings.Count(page, `name="csrf"`) {
		t.Fatal("a POST form without csrf")
	}
	_ = customize.ProductsAI
}
