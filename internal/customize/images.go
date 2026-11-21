package customize

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif" // decoders for uploads
	_ "image/jpeg"
	_ "image/png"
	"math"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/gen2brain/webp" // pure-Go (transpiled libwebp) WebP encoder and decoder
	xdraw "golang.org/x/image/draw"
)

// Contract v2 image slots: every slot declares a standard ratio and an output size; the client's
// photos are cropped around the centre, resized to that size and stored as WebP.

// Ratios are the standard slot ratios (width:height).
var Ratios = []struct {
	ID   string
	W, H int
	// default output size when the theme gives none
	DefW, DefH int
}{
	{"16:9", 16, 9, 1920, 1080},
	{"4:3", 4, 3, 1600, 1200},
	{"1:1", 1, 1, 1200, 1200},
	{"3:4", 3, 4, 1200, 1600},
}

const (
	// GroupSite and GroupProducts are the image slot groups.
	GroupSite     = "site"
	GroupProducts = "products"
	// ProductsKeep and ProductsAI are the product modes of a run.
	ProductsKeep = "keep"
	ProductsAI   = "ai"
	// MaxPhoto is the largest photo a client may upload for the image slots.
	MaxPhoto = 40 << 20
	// maxPixels guards against decompression bombs (50 megapixels).
	maxPixels = 50_000_000
	// maxSide bounds a slot's output size.
	maxSide = 4000
	// defaultPriority is a slot's priority when customize.json gives none.
	defaultPriority = 100
	// webpQuality is the lossy WebP quality of processed images.
	webpQuality = 85
)

func ratioOf(id string) (float64, bool) {
	for _, r := range Ratios {
		if r.ID == id {
			return float64(r.W) / float64(r.H), true
		}
	}
	return 0, false
}

// nearestRatio returns the standard ratio closest to w/h.
func nearestRatio(w, h int) string {
	best, bd := "", math.MaxFloat64
	for _, r := range Ratios {
		if d := ratioDist(float64(w)/float64(h), float64(r.W)/float64(r.H)); d < bd {
			best, bd = r.ID, d
		}
	}
	return best
}

// ratioDist is the distance between two ratios (symmetric: 2:1 and 1:2 are as far from 1:1).
func ratioDist(a, b float64) float64 { return math.Abs(math.Log(a / b)) }

// SpecImage is one entry of "images" in customize.json.
type SpecImage struct {
	ID       string `json:"id"` // id of the demo.json images[] entry this slot replaces
	Label    string `json:"label"`
	Ratio    string `json:"ratio,omitempty"`    // v2: 16:9 | 4:3 | 1:1 | 3:4
	Width    int    `json:"width,omitempty"`    // v2: output width in px
	Height   int    `json:"height,omitempty"`   // v2: output height in px
	Priority *int   `json:"priority,omitempty"` // v2: lower is filled first
	Group    string `json:"group,omitempty"`    // v2: site (default) | products
}

// SpecProducts is the top-level "products" of customize.json (v2): the theme has products.
type SpecProducts struct {
	PostType    string `json:"post_type"`
	DefaultMode string `json:"default_mode"` // keep | ai
}

// Sized reports whether the slot has a v2 size (photos are cropped and converted).
func (s ImageSlot) Sized() bool { return s.Width > 0 && s.Height > 0 }

// Products reports whether the slot belongs to the products group.
func (s ImageSlot) Products() bool { return s.Group == GroupProducts }

// AspectW and AspectH are the slot's ratio for the page's crop preview (4:3 for v1 slots).
func (s ImageSlot) AspectW() int {
	if !s.Sized() {
		return 4
	}
	return s.Width
}

// AspectH is the height part of the preview ratio.
func (s ImageSlot) AspectH() int {
	if !s.Sized() {
		return 3
	}
	return s.Height
}

// sizeSlot fills a v2 slot's ratio, size, priority and group from its spec entry; problems are
// returned as warnings and replaced by safe values.
func sizeSlot(sl *ImageSlot, im SpecImage) []string {
	var warns []string
	w, h := im.Width, im.Height
	if w < 0 || h < 0 || w > maxSide || h > maxSide {
		warns = append(warns, fmt.Sprintf("تصویر %s: اندازه‌ی %dx%d پذیرفته نیست", im.ID, w, h))
		w, h = 0, 0
	}
	ratio := im.Ratio
	r, ok := ratioOf(ratio)
	if !ok {
		if ratio != "" {
			warns = append(warns, fmt.Sprintf("تصویر %s: نسبت «%s» استاندارد نیست (16:9، 4:3، 1:1، 3:4)", im.ID, ratio))
		}
		if w > 0 && h > 0 {
			ratio = nearestRatio(w, h)
		} else {
			ratio = "16:9"
			warns = append(warns, fmt.Sprintf("تصویر %s: نسبت و اندازه ندارد؛ 16:9 فرض شد", im.ID))
		}
		r, _ = ratioOf(ratio)
	}
	switch {
	case w > 0 && h > 0:
		if ratioDist(float64(w)/float64(h), r) > 0.02 {
			warns = append(warns, fmt.Sprintf("تصویر %s: اندازه‌ی %dx%d با نسبت %s نمی‌خواند؛ اندازه ملاک است", im.ID, w, h, ratio))
		}
	case w > 0:
		h = int(math.Round(float64(w) / r))
	case h > 0:
		w = int(math.Round(float64(h) * r))
	default:
		for _, x := range Ratios {
			if x.ID == ratio {
				w, h = x.DefW, x.DefH
			}
		}
	}
	sl.Ratio, sl.Width, sl.Height = ratio, w, h
	sl.Priority = defaultPriority
	if im.Priority != nil {
		sl.Priority = *im.Priority
	}
	switch im.Group {
	case "", GroupSite:
		sl.Group = GroupSite
	case GroupProducts:
		sl.Group = GroupProducts
	default:
		sl.Group = GroupSite
		warns = append(warns, fmt.Sprintf("تصویر %s: گروه «%s» شناخته نیست (site یا products)", im.ID, im.Group))
	}
	return warns
}

// ---- image processing ----

// decodeImage decodes a png, jpeg, gif or webp image (JPEG EXIF orientation applied).
func decodeImage(data []byte) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("فایل تصویر خوانده نشد (فقط png، jpg، webp یا gif)")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxPixels {
		return nil, fmt.Errorf("ابعاد تصویر (%dx%d) بیش از حد بزرگ است؛ حداکثر ۵۰ مگاپیکسل", cfg.Width, cfg.Height)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("فایل تصویر خراب است یا خوانده نشد")
	}
	return orient(img, jpegOrientation(data)), nil
}

// CropRect is the largest centred rectangle of b with the ratio w:h.
func CropRect(b image.Rectangle, w, h int) image.Rectangle {
	sw, sh := b.Dx(), b.Dy()
	cw, ch := sw, int(math.Round(float64(sw)*float64(h)/float64(w)))
	if ch > sh {
		ch = sh
		cw = int(math.Round(float64(sh) * float64(w) / float64(h)))
	}
	if cw < 1 {
		cw = 1
	}
	if ch < 1 {
		ch = 1
	}
	x := b.Min.X + (sw-cw)/2
	y := b.Min.Y + (sh-ch)/2
	return image.Rect(x, y, x+cw, y+ch)
}

// Fit crops img around its centre to w:h and resizes it to exactly w×h.
func Fit(img image.Image, w, h int) *image.NRGBA {
	src := CropRect(img.Bounds(), w, h)
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, src, xdraw.Src, nil)
	return dst
}

// encodeWebP writes img as lossy WebP.
func encodeWebP(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := webp.Encode(&buf, img, webp.Options{Quality: quality, Method: 4}); err != nil {
		return nil, fmt.Errorf("ساخت WebP: %w", err)
	}
	return buf.Bytes(), nil
}

// ProcessImage crops a photo around its centre to the slot's ratio, resizes it to w×h and
// returns it as WebP; small reports that the photo was smaller than the slot (it was enlarged).
func ProcessImage(data []byte, w, h int) (out []byte, small bool, err error) {
	if w <= 0 || h <= 0 || w > maxSide || h > maxSide {
		return nil, false, fmt.Errorf("اندازه‌ی %dx%d معتبر نیست", w, h)
	}
	img, err := decodeImage(data)
	if err != nil {
		return nil, false, err
	}
	c := CropRect(img.Bounds(), w, h)
	out, err = encodeWebP(Fit(img, w, h), webpQuality)
	return out, c.Dx() < w || c.Dy() < h, err
}

// thumbnail scales a photo to fit within max×max, as WebP.
func thumbnail(img image.Image, max int) ([]byte, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > max || h > max {
		if w >= h {
			h, w = int(math.Max(1, math.Round(float64(h)*float64(max)/float64(w)))), max
		} else {
			w, h = int(math.Max(1, math.Round(float64(w)*float64(max)/float64(h)))), max
		}
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return encodeWebP(dst, 75)
}

// jpegOrientation reads the EXIF orientation (1-8) of a JPEG; 1 when absent.
func jpegOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 1
	}
	i := 2
	for i+4 <= len(b) {
		if b[i] != 0xFF {
			return 1
		}
		marker := b[i+1]
		if marker == 0xDA || marker == 0xD9 { // start of scan / end: no EXIF before the image
			return 1
		}
		n := int(binary.BigEndian.Uint16(b[i+2:]))
		if n < 2 || i+2+n > len(b) {
			return 1
		}
		seg := b[i+4 : i+2+n]
		if marker == 0xE1 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00" {
			return exifOrientation(seg[6:])
		}
		i += 2 + n
	}
	return 1
}

func exifOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	off := int(bo.Uint32(t[4:]))
	if off < 8 || off+2 > len(t) {
		return 1
	}
	n := int(bo.Uint16(t[off:]))
	for k := 0; k < n; k++ {
		e := off + 2 + k*12
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:]) == 0x0112 {
			if v := int(bo.Uint16(t[e+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// orient applies an EXIF orientation so the photo stands the way the camera saw it.
func orient(img image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var nx, ny int
			switch o {
			case 2:
				nx, ny = w-1-x, y
			case 3:
				nx, ny = w-1-x, h-1-y
			case 4:
				nx, ny = x, h-1-y
			case 5:
				nx, ny = y, x
			case 6:
				nx, ny = h-1-y, x
			case 7:
				nx, ny = h-1-y, w-1-x
			case 8:
				nx, ny = y, w-1-x
			}
			dst.Set(nx, ny, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

// ---- uploads, auto-assignment and slots ----

// Upload is a photo the client uploaded for the image slots (kept as uploaded).
type Upload struct {
	File  string `json:"file"`            // original, in the draft folder
	Thumb string `json:"thumb,omitempty"` // small WebP preview
	Name  string `json:"name,omitempty"`  // file name on the client's computer
	W     int    `json:"w"`
	H     int    `json:"h"`
}

// Ratio is the upload's nearest standard ratio.
func (u Upload) Ratio() string {
	if u.W <= 0 || u.H <= 0 {
		return ""
	}
	return nearestRatio(u.W, u.H)
}

// SlotPick is where a slot's image comes from.
type SlotPick struct {
	Src    string `json:"src,omitempty"`    // upload file; "" = the demo image
	Manual bool   `json:"manual,omitempty"` // chosen by the user; auto-assignment leaves it
	Small  bool   `json:"small,omitempty"`  // the photo was smaller than the slot
}

var uploadExt = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp", "image/gif": ".gif"}

// AddUpload stores an uploaded photo (and its thumbnail) in the draft. The caller saves the draft.
func (d *Draft) AddUpload(name string, data []byte) (Upload, error) {
	label := path.Base(strings.ReplaceAll(name, `\`, "/"))
	if len(data) == 0 {
		return Upload{}, fmt.Errorf("%s: فایل خالی است", label)
	}
	if len(data) > MaxPhoto {
		return Upload{}, fmt.Errorf("%s: حجم تصویر بیش از %d مگابایت است", label, MaxPhoto>>20)
	}
	ext, ok := uploadExt[http.DetectContentType(data)]
	if !ok && bytes.HasPrefix(data, []byte("RIFF")) && len(data) > 12 && string(data[8:12]) == "WEBP" {
		ext, ok = ".webp", true
	}
	if !ok {
		return Upload{}, fmt.Errorf("%s: فقط تصویر png، jpg، webp یا gif پذیرفته می‌شود", label)
	}
	img, err := decodeImage(data)
	if err != nil {
		return Upload{}, fmt.Errorf("%s: %w", label, err)
	}
	rb := make([]byte, 5)
	_, _ = rand.Read(rb)
	id := hex.EncodeToString(rb)
	u := Upload{File: "src-" + id + ext, Thumb: "thumb-" + id + ".webp", Name: label, W: img.Bounds().Dx(), H: img.Bounds().Dy()}
	th, err := thumbnail(img, 480)
	if err != nil {
		return Upload{}, err
	}
	if err := os.WriteFile(d.File(u.File), data, 0o600); err != nil {
		return Upload{}, err
	}
	if err := os.WriteFile(d.File(u.Thumb), th, 0o600); err != nil {
		return Upload{}, err
	}
	d.Uploads = append(d.Uploads, u)
	return u, nil
}

// UploadOf returns the upload stored as file.
func (d *Draft) UploadOf(file string) (Upload, bool) {
	for _, u := range d.Uploads {
		if u.File == file {
			return u, true
		}
	}
	return Upload{}, false
}

// ProductMode is the products option of a run for this demo: "" when the theme declares no
// products, else keep or ai (the draft's choice, or the theme's default).
func (demo *Demo) ProductMode(choice string) string {
	if demo.Products == nil {
		return ""
	}
	switch choice {
	case ProductsKeep, ProductsAI:
		return choice
	}
	if demo.Products.DefaultMode == ProductsAI {
		return ProductsAI
	}
	return ProductsKeep
}

// productRole reports whether a field belongs to the theme's products.
func productRole(role string) bool { return strings.HasPrefix(role, "product.") }

// ForProducts returns the demo as one run sees it: with the products mode keep, product fields
// and product image slots are left out, so they are neither written, cleared nor replaced.
func (demo *Demo) ForProducts(mode string) *Demo {
	if mode != ProductsKeep {
		return demo
	}
	cp := *demo
	cp.Fields, cp.Images = nil, nil
	for _, f := range demo.Fields {
		if !productRole(f.Role) {
			cp.Fields = append(cp.Fields, f)
		}
	}
	for _, s := range demo.Images {
		if !s.Products() {
			cp.Images = append(cp.Images, s)
		}
	}
	return &cp
}

// slotFile is the processed image of a slot.
func slotFile(id string) string { return "slot-" + safeName(id) + ".webp" }

// SetSlot puts upload src (an upload file; "" = the demo image) into a slot and processes it.
// manual marks a choice of the user, which auto-assignment never changes.
func (d *Draft) SetSlot(sl ImageSlot, src string, manual bool) error {
	if d.Slots == nil {
		d.Slots = map[string]SlotPick{}
	}
	if old, ok := d.Images[sl.ID]; ok {
		if _, isUpload := d.UploadOf(old); !isUpload && old != slotFile(sl.ID) {
			_ = os.Remove(d.File(old)) // an image saved by the per-slot form
		}
	}
	if src == "" {
		if f, ok := d.Images[sl.ID]; ok && f == slotFile(sl.ID) {
			_ = os.Remove(d.File(f))
		}
		delete(d.Images, sl.ID)
		if manual {
			d.Slots[sl.ID] = SlotPick{Manual: true}
		} else {
			delete(d.Slots, sl.ID)
		}
		return nil
	}
	u, ok := d.UploadOf(src)
	if !ok {
		return fmt.Errorf("%s: تصویر انتخاب‌شده پیدا نشد", sl.Label)
	}
	pick := SlotPick{Src: src, Manual: manual}
	if !sl.Sized() {
		// a v1 slot: the photo is used as uploaded
		d.Images[sl.ID] = u.File
		d.Slots[sl.ID] = pick
		return nil
	}
	data, err := os.ReadFile(d.File(u.File))
	if err != nil {
		return fmt.Errorf("%s: %w", sl.Label, err)
	}
	out, small, err := ProcessImage(data, sl.Width, sl.Height)
	if err != nil {
		return fmt.Errorf("%s: %w", sl.Label, err)
	}
	name := slotFile(sl.ID)
	if err := os.WriteFile(d.File(name), out, 0o600); err != nil {
		return err
	}
	pick.Small = small
	d.Images[sl.ID] = name
	d.Slots[sl.ID] = pick
	return nil
}

// AutoAssign decides which unused uploads go to which empty slots, without changing the draft:
// each photo (in upload order) goes to the empty slot whose ratio is closest to its own, the
// slot with the lowest priority number first. A slot is empty when it has no image and no
// choice of the user. Slots without a v2 size (v1 themes) take the remaining photos in order.
// It returns slot id → upload file.
func AutoAssign(slots []ImageSlot, d *Draft) map[string]string {
	out := map[string]string{}
	used := map[string]bool{}
	for _, p := range d.Slots {
		if p.Src != "" {
			used[p.Src] = true
		}
	}
	free := map[string]bool{}
	var order []ImageSlot
	for _, s := range slots {
		_, picked := d.Slots[s.ID]
		_, has := d.Images[s.ID]
		if !picked && !has {
			free[s.ID] = true
			order = append(order, s)
		}
	}
	// lowest priority number first, then declaration order
	sort.SliceStable(order, func(i, j int) bool { return order[i].Priority < order[j].Priority })
	for _, u := range d.Uploads {
		if used[u.File] || u.W <= 0 || u.H <= 0 {
			continue
		}
		r := float64(u.W) / float64(u.H)
		best, bd := "", math.MaxFloat64
		for _, s := range order {
			if !free[s.ID] {
				continue
			}
			dist := math.MaxFloat64 / 2 // unsized slots come after every sized one
			if s.Sized() {
				dist = ratioDist(r, float64(s.Width)/float64(s.Height))
			}
			if dist < bd-1e-9 {
				best, bd = s.ID, dist
			}
		}
		if best == "" {
			break
		}
		out[best] = u.File
		free[best] = false
		used[u.File] = true
	}
	return out
}

// PlaceImages auto-assigns the unused uploads to the empty slots of demo (as one run sees it,
// see ForProducts) and processes them. It returns how many slots it filled.
func (d *Draft) PlaceImages(demo *Demo) (int, error) {
	bySlot := map[string]ImageSlot{}
	for _, s := range demo.Images {
		bySlot[s.ID] = s
	}
	assign := AutoAssign(demo.Images, d)
	ids := make([]string, 0, len(assign))
	for id := range assign {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := d.SetSlot(bySlot[id], assign[id], false); err != nil {
			return 0, err
		}
	}
	return len(assign), nil
}

// ClearAuto removes every slot image auto-assignment chose (manual choices stay).
func (d *Draft) ClearAuto(demo *Demo) {
	for _, s := range demo.Images {
		if p, ok := d.Slots[s.ID]; ok && !p.Manual {
			_ = d.SetSlot(s, "", false)
		}
	}
}

// RemoveUpload deletes an upload and empties the slots that used it.
func (d *Draft) RemoveUpload(demo *Demo, file string) {
	u, ok := d.UploadOf(file)
	if !ok {
		return
	}
	for _, s := range demo.Images {
		if p, ok := d.Slots[s.ID]; ok && p.Src == file {
			_ = d.SetSlot(s, "", false)
		}
	}
	for id, f := range d.Images { // slots no longer in the demo
		if f == file {
			delete(d.Images, id)
		}
	}
	_ = os.Remove(d.File(u.File))
	_ = os.Remove(d.File(u.Thumb))
	out := d.Uploads[:0]
	for _, x := range d.Uploads {
		if x.File != file {
			out = append(out, x)
		}
	}
	d.Uploads = out
}

// UnfilledProductSlots lists the product slots that keep the demo photo in the ai products mode
// (the demo photos may not match the new product texts).
func (demo *Demo) UnfilledProductSlots(d *Draft) []ImageSlot {
	if demo.ProductMode(d.Products) != ProductsAI {
		return nil
	}
	var out []ImageSlot
	for _, s := range demo.Images {
		if _, ok := d.Images[s.ID]; s.Products() && !ok {
			out = append(out, s)
		}
	}
	return out
}
