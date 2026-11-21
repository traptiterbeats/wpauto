package customize

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"strings"
	"testing"

	"github.com/gen2brain/webp"
	"github.com/traptiterbeats/wpauto/internal/catalog/catalogtest"
)

// stripes is a w×h image: left third red, middle green, right third blue.
func stripes(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBA{0, 200, 0, 255}
			if x < w/3 {
				c = color.NRGBA{200, 0, 0, 255}
			} else if x >= w-w/3 {
				c = color.NRGBA{0, 0, 200, 255}
			}
			img.Set(x, y, c)
		}
	}
	return img
}

func pngBytes(t testing.TB, img image.Image) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// PhotoPNG is a w×h PNG photo for tests.
func photoPNG(t testing.TB, w, h int) []byte { return pngBytes(t, stripes(w, h)) }

func TestCropRect(t *testing.T) {
	cases := []struct {
		b      image.Rectangle
		w, h   int
		expect image.Rectangle
	}{
		{image.Rect(0, 0, 400, 100), 16, 9, image.Rect(111, 0, 289, 100)},
		{image.Rect(0, 0, 1000, 1000), 16, 9, image.Rect(0, 218, 1000, 781)},
		{image.Rect(0, 0, 300, 300), 3, 4, image.Rect(37, 0, 262, 300)},
		{image.Rect(0, 0, 160, 90), 160, 90, image.Rect(0, 0, 160, 90)},
	}
	for _, c := range cases {
		if got := CropRect(c.b, c.w, c.h); got != c.expect {
			t.Fatalf("CropRect(%v, %d:%d) = %v, want %v", c.b, c.w, c.h, got, c.expect)
		}
	}
	// the centre crop of a wide striped image to 1:1 shows only the middle (green) stripe
	out := Fit(stripes(900, 300), 50, 50)
	if out.Bounds().Dx() != 50 || out.Bounds().Dy() != 50 {
		t.Fatalf("size %v", out.Bounds())
	}
	for _, x := range []int{2, 25, 47} {
		if c := out.NRGBAAt(x, 25); c.G < 150 || c.R > 50 || c.B > 50 {
			t.Fatalf("pixel %d is %v, want the centre stripe", x, c)
		}
	}
}

func TestProcessImage(t *testing.T) {
	out, small, err := ProcessImage(photoPNG(t, 640, 480), 160, 90)
	if err != nil || small {
		t.Fatal(err, small)
	}
	if !bytes.HasPrefix(out, []byte("RIFF")) || string(out[8:12]) != "WEBP" {
		t.Fatal("output is not WebP")
	}
	img, err := webp.Decode(bytes.NewReader(out))
	if err != nil || img.Bounds().Dx() != 160 || img.Bounds().Dy() != 90 {
		t.Fatalf("decoded %v %v", err, img)
	}
	// a JPEG smaller than the slot is enlarged and flagged
	var jb bytes.Buffer
	_ = jpeg.Encode(&jb, stripes(100, 100), &jpeg.Options{Quality: 90})
	if _, small, err = ProcessImage(jb.Bytes(), 200, 150); err != nil || !small {
		t.Fatalf("small: %v %v", small, err)
	}
	if _, _, err := ProcessImage([]byte("<?php"), 10, 10); err == nil {
		t.Fatal("non-image accepted")
	}
	if _, _, err := ProcessImage(photoPNG(t, 10, 10), 0, 10); err == nil {
		t.Fatal("bad size accepted")
	}
}

// exifJPEG returns a w×h JPEG with an EXIF orientation tag.
func exifJPEG(t *testing.T, w, h int, orientation uint16) []byte {
	var jb bytes.Buffer
	_ = jpeg.Encode(&jb, stripes(w, h), nil)
	tiff := []byte("MM\x00\x2a\x00\x00\x00\x08\x00\x01")
	entry := make([]byte, 12)
	binary.BigEndian.PutUint16(entry[0:], 0x0112)
	binary.BigEndian.PutUint16(entry[2:], 3)
	binary.BigEndian.PutUint32(entry[4:], 1)
	binary.BigEndian.PutUint16(entry[8:], orientation)
	tiff = append(append(tiff, entry...), 0, 0, 0, 0)
	seg := append([]byte("Exif\x00\x00"), tiff...)
	app1 := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(app1[2:], uint16(len(seg)+2))
	b := jb.Bytes()
	return append(append(append([]byte{}, b[:2]...), append(app1, seg...)...), b[2:]...)
}

func TestEXIFOrientation(t *testing.T) {
	data := exifJPEG(t, 120, 60, 6)
	if o := jpegOrientation(data); o != 6 {
		t.Fatalf("orientation %d", o)
	}
	img, err := decodeImage(data)
	if err != nil || img.Bounds().Dx() != 60 || img.Bounds().Dy() != 120 {
		t.Fatalf("a portrait phone photo must stand: %v %v", err, img.Bounds())
	}
	if jpegOrientation(photoPNG(t, 4, 4)) != 1 {
		t.Fatal("png has no orientation")
	}
}

func TestNearestRatio(t *testing.T) {
	for in, want := range map[[2]int]string{{1920, 1080}: "16:9", {1600, 1200}: "4:3", {1000, 1000}: "1:1", {900, 1200}: "3:4",
		{3000, 1000}: "16:9", {1000, 3000}: "3:4", {1100, 1000}: "1:1"} {
		if got := nearestRatio(in[0], in[1]); got != want {
			t.Fatalf("%v: %s, want %s", in, got, want)
		}
	}
}

func slot(id, ratio string, w, h, prio int, group string) ImageSlot {
	return ImageSlot{ID: id, Label: id, Ratio: ratio, Width: w, Height: h, Priority: prio, Group: group}
}

func TestAutoAssignPriority(t *testing.T) {
	slots := []ImageSlot{
		slot("banner", "16:9", 160, 90, 5, GroupSite), // declared first but less important
		slot("hero", "16:9", 160, 90, 1, GroupSite),
		slot("square", "1:1", 80, 80, 3, GroupSite),
		slot("portrait", "3:4", 90, 120, 2, GroupSite),
	}
	d := &Draft{Images: map[string]string{}, Uploads: []Upload{
		{File: "a", W: 1600, H: 900},  // 16:9 → hero (priority 1), not banner
		{File: "b", W: 1000, H: 1050}, // ~1:1 → square
		{File: "c", W: 1920, H: 1080}, // 16:9 → banner (hero is taken)
		{File: "d", W: 1500, H: 1000}, // 3:2: no 4:3 slot, nearest free is banner/hero (taken) → … portrait is farther than nothing
	}}
	got := AutoAssign(slots, d)
	if got["hero"] != "a" || got["square"] != "b" || got["banner"] != "c" || got["portrait"] != "d" {
		t.Fatalf("assign %v", got)
	}
	// only two photos: the most important matching slots get them, the rest keep the demo image
	d.Uploads = d.Uploads[:2]
	got = AutoAssign(slots, d)
	if len(got) != 2 || got["hero"] != "a" || got["square"] != "b" {
		t.Fatalf("two photos: %v", got)
	}
	// a slot the user chose (even "demo image") and photos already used are left alone
	d.Slots = map[string]SlotPick{"hero": {Manual: true}, "square": {Src: "b", Manual: true}}
	got = AutoAssign(slots, d)
	if len(got) != 1 || got["banner"] != "a" {
		t.Fatalf("manual choices: %v", got)
	}
	// unsized (v1) slots take photos in order after every sized one
	d.Slots = nil
	got = AutoAssign([]ImageSlot{{ID: "v1a"}, {ID: "v1b"}}, d)
	if got["v1a"] != "a" || got["v1b"] != "b" {
		t.Fatalf("v1 order: %v", got)
	}
}

func loadV2(t *testing.T) *Demo {
	t.Helper()
	demo, err := LoadDemo(catalogtest.RichDemoV2(t, t.TempDir(), "shop"))
	if err != nil {
		t.Fatal(err)
	}
	return demo
}

func TestSpecV2(t *testing.T) {
	demo := loadV2(t)
	if !demo.V2 || demo.Products == nil || demo.Products.PostType != "product" || demo.Products.DefaultMode != ProductsKeep {
		t.Fatalf("v2 %v products %+v", demo.V2, demo.Products)
	}
	if len(demo.Images) != 6 || len(demo.Warnings) != 0 {
		t.Fatalf("slots %d warnings %v", len(demo.Images), demo.Warnings)
	}
	if s := demo.Images[4]; s.ID != "p-mug" || !s.Products() || s.Width != 80 || s.Ratio != "1:1" || s.Priority != 5 {
		t.Fatalf("product slot %+v", s)
	}
	if ok, missing := demo.Standard(); !ok {
		t.Fatalf("standard: %v", missing)
	}
	for _, f := range demo.Fields {
		switch f.Role {
		case "product.item.price", "product.item.sku":
			if f.AI {
				t.Fatalf("%s must stay as in the demo", f.Role)
			}
		case "product.item.title", "product.item.short", "product.item.description":
			if !f.AI {
				t.Fatalf("%s must be written by the AI", f.Role)
			}
		}
	}
	if demo.ProductMode("") != ProductsKeep || demo.ProductMode("ai") != ProductsAI || demo.ProductMode("x") != ProductsKeep {
		t.Fatal("product mode")
	}
	// bad entries become safe values with a warning
	d := &Demo{JSON: map[string]any{"images": []any{map[string]any{"id": "a", "file": "a.jpg"}, map[string]any{"id": "b", "file": "b.jpg"}}}}
	p := 0
	d.Spec = &Spec{Version: 2, Images: []SpecImage{{ID: "a", Ratio: "2:1", Width: 300, Height: 100}, {ID: "b", Ratio: "4:3", Width: 400, Group: "x", Priority: &p}}}
	d.scan()
	if d.Images[0].Ratio != "16:9" || d.Images[1].Height != 300 || d.Images[1].Group != GroupSite || d.Images[1].Priority != 0 || d.Images[0].Priority != defaultPriority || len(d.Warnings) != 3 {
		t.Fatalf("%+v %v", d.Images, d.Warnings)
	}
}

func TestV1Unchanged(t *testing.T) {
	demo, err := LoadDemo(catalogtest.RichDemo(t, t.TempDir(), "acme", true))
	if err != nil {
		t.Fatal(err)
	}
	if demo.V2 || demo.Products != nil || demo.ProductMode("ai") != "" || demo.ForProducts("") != demo || len(demo.Warnings) != 0 {
		t.Fatal("a v1 demo must not get v2 behaviour")
	}
	if len(demo.Images) != 1 || demo.Images[0].Sized() || demo.Images[0].Group != "" {
		t.Fatalf("v1 slot %+v", demo.Images[0])
	}
	// a photo placed in a v1 slot is used as uploaded
	d, _ := Open(t.TempDir()).New("acme", "x")
	u, err := d.AddUpload("p.png", photoPNG(t, 300, 200))
	if err != nil {
		t.Fatal(err)
	}
	if n, err := d.PlaceImages(demo); err != nil || n != 1 || d.Images["hero"] != u.File {
		t.Fatalf("v1 place %d %v %v", n, err, d.Images)
	}
}

func TestProductModes(t *testing.T) {
	demo := loadV2(t)
	st := Open(t.TempDir())
	run := func(mode string) (map[string]string, map[string]any, *Draft) {
		d, _ := st.New("shop", "Naranj")
		d.Brief = Brief{SiteName: "Naranj", Business: "cafe"}
		d.Products = mode
		values, _, err := Generate(context.Background(), &fakeAI{}, demo.ForProducts(demo.ProductMode(d.Products)), d.Brief, nil)
		if err != nil {
			t.Fatal(err)
		}
		d.Values = values
		// two photos: one wide, one square
		for _, p := range [][]byte{photoPNG(t, 320, 180), photoPNG(t, 200, 200)} {
			if _, err := d.AddUpload("p.png", p); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := d.PlaceImages(demo.ForProducts(demo.ProductMode(d.Products))); err != nil {
			t.Fatal(err)
		}
		out, _, err := Apply(demo, d)
		if err != nil {
			t.Fatal(err)
		}
		return values, out, d
	}

	// keep: product texts are neither written nor cleared, product slots are skipped
	values, out, d := run(ProductsKeep)
	for p := range values {
		if strings.HasPrefix(p, "products") {
			t.Fatalf("keep mode wrote %s", p)
		}
	}
	if values["theme_mods.hero_title"] != "NEW Software that ships" {
		t.Fatalf("site texts must still be written: %v", values)
	}
	d.Values["products[slug=mug].name"] = "stale AI text" // from an earlier ai run
	out, _, _ = Apply(demo, d)
	for p, want := range map[string]string{"products[slug=mug].name": "Code Mug", "products[slug=mug].short_description": "A mug for coders.",
		"products[slug=mug].regular_price": "19", "products[slug=tee].sku": "TEE-1"} {
		if v, _ := Get(out, p); v != want {
			t.Fatalf("keep: %s = %q", p, v)
		}
	}
	if d.Images["hero"] == "" || d.Images["team"] == "" || d.Images["p-mug"] != "" {
		t.Fatalf("keep placement %v", d.Images)
	}
	if len(demo.UnfilledProductSlots(d)) != 0 {
		t.Fatal("no product warning in keep mode")
	}

	// ai: product names and descriptions are written, prices and SKUs stay, product slots take photos
	values, out, d = run(ProductsAI)
	if values["products[slug=mug].name"] != "NEW Code Mug" || values["products[slug=mug].short_description"] != "NEW A mug for coders." || values["products[slug=tee].name"] != "NEW Dev Tee" {
		t.Fatalf("ai values: %v", values)
	}
	if _, ok := values["products[slug=mug].regular_price"]; ok {
		t.Fatal("price written by the AI")
	}
	if v, _ := Get(out, "products[slug=mug].name"); v != "NEW Code Mug" {
		t.Fatalf("ai apply: %q", v)
	}
	if v, _ := Get(out, "products[slug=tee].sku"); v != "TEE-1" {
		t.Fatal("sku changed")
	}
	// the square photo is nearest to 1:1; team (priority 3) comes before the product slots
	if d.Images["hero"] == "" || d.Images["team"] == "" || d.Images["p-mug"] != "" {
		t.Fatalf("ai placement %v", d.Images)
	}
	if n := len(demo.UnfilledProductSlots(d)); n != 2 {
		t.Fatalf("unfilled product slots %d", n)
	}
	// a manual choice puts a photo into a product slot, processed to its size
	if err := d.SetSlot(demo.Images[4], d.Uploads[1].File, true); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(d.File(d.Images["p-mug"]))
	if img, err := webp.Decode(bytes.NewReader(b)); err != nil || img.Bounds().Dx() != 80 || img.Bounds().Dy() != 80 {
		t.Fatalf("product slot image %v", err)
	}
	if n := len(demo.UnfilledProductSlots(d)); n != 1 {
		t.Fatalf("unfilled product slots %d", n)
	}
}

func TestBuildV2Images(t *testing.T) {
	demo := loadV2(t)
	st := Open(t.TempDir())
	d, _ := st.New("shop", "Naranj")
	d.Values["theme_mods.hero_title"] = "Fresh coffee"
	for _, p := range [][]byte{photoPNG(t, 1200, 700), photoPNG(t, 400, 300)} {
		if _, err := d.AddUpload("p.png", p); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := d.PlaceImages(demo.ForProducts(demo.ProductMode(d.Products))); err != nil || n != 2 {
		t.Fatalf("placed %d %v", n, err)
	}
	if d.Images["hero"] != "slot-hero.webp" || d.Images["about"] != "slot-about.webp" {
		t.Fatalf("images %v", d.Images)
	}
	out, err := Build(demo, d)
	if err != nil {
		t.Fatal(err)
	}
	z, err := zipFiles(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"images/client-hero.webp", "images/client-about.webp", "images/team.jpg", "images/p-mug.jpg"} {
		if !z[n] {
			t.Fatalf("%s missing: %v", n, z)
		}
	}
	if z["images/hero.jpg"] {
		t.Fatal("replaced demo image still packed")
	}
	// removing a photo empties its slot; re-placing fills it again from what is left
	d.RemoveUpload(demo, d.Slots["hero"].Src)
	if _, ok := d.Images["hero"]; ok || len(d.Uploads) != 1 {
		t.Fatalf("remove: %v", d.Images)
	}
}

func zipFiles(p string) (map[string]bool, error) {
	z, err := zip.OpenReader(p)
	if err != nil {
		return nil, err
	}
	defer z.Close()
	out := map[string]bool{}
	for _, f := range z.File {
		out[f.Name] = true
	}
	return out, nil
}
