package web

import (
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/traptiterbeats/wpauto/internal/customize"
	"github.com/traptiterbeats/wpauto/internal/engine"
)

// readUpload reads one uploaded file (at most customize.MaxPhoto+1 bytes).
func readUpload(fh *multipart.FileHeader) ([]byte, error) {
	f, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, customize.MaxPhoto+1))
}

// formPhotos returns the non-empty files of a multi-file input.
func formPhotos(r *http.Request, field string) []*multipart.FileHeader {
	if r.MultipartForm == nil {
		return nil
	}
	var out []*multipart.FileHeader
	for _, fh := range r.MultipartForm.File[field] {
		if fh.Size > 0 {
			out = append(out, fh)
		}
	}
	return out
}

// draftImagesV2 saves the images step of a contract v2 draft: the logo, the products mode, the
// client's photos (any number), per-slot choices and direct uploads, then fills the empty slots
// automatically.
func (s *Server) draftImagesV2(w http.ResponseWriter, r *http.Request, id string, demo *customize.Demo) {
	added, placed := 0, 0
	var problems []string
	_, err := engine.Drafts().Update(id, func(d *customize.Draft) error {
		if fhs := formPhotos(r, "logo"); len(fhs) > 0 {
			b, err := readUpload(fhs[0])
			if err != nil {
				return err
			}
			name, err := d.SaveImage("logo", b)
			if err != nil {
				return fmt.Errorf("لوگو: %w", err)
			}
			d.Logo = name
		}
		if m := r.FormValue("products"); demo.Products != nil && (m == customize.ProductsKeep || m == customize.ProductsAI) {
			d.Products = m
		}
		mode := demo.ProductMode(d.Products)
		for i, sl := range demo.Images {
			if mode == customize.ProductsKeep && sl.Products() {
				continue
			}
			if fhs := formPhotos(r, fmt.Sprintf("img_%d", i)); len(fhs) > 0 {
				b, err := readUpload(fhs[0])
				if err == nil {
					var u customize.Upload
					if u, err = d.AddUpload(fhs[0].Filename, b); err == nil {
						added++
						err = d.SetSlot(sl, u.File, true)
					}
				}
				if err != nil {
					problems = append(problems, err.Error())
				}
				continue
			}
			cur := d.Slots[sl.ID]
			var err error
			switch pick := r.FormValue(fmt.Sprintf("pick_%d", i)); pick {
			case "", "keep":
			case "auto":
				if cur.Manual {
					err = d.SetSlot(sl, "", false)
				}
			case "demo":
				if !(cur.Manual && cur.Src == "") {
					err = d.SetSlot(sl, "", true)
				}
			default:
				if cur.Src != pick || !cur.Manual {
					err = d.SetSlot(sl, pick, true)
				}
			}
			if err != nil {
				problems = append(problems, err.Error())
			}
		}
		for _, fh := range formPhotos(r, "photos") {
			b, err := readUpload(fh)
			if err == nil {
				_, err = d.AddUpload(fh.Filename, b)
			}
			if err != nil {
				problems = append(problems, err.Error())
				continue
			}
			added++
		}
		var err error
		placed, err = d.PlaceImages(demo.ForProducts(mode))
		return err
	})
	if err != nil {
		s.draftBack(w, r, id, "err", err.Error(), "#images")
		return
	}
	msg := fmt.Sprintf("%d تصویر بارگذاری شد؛ %d جای تصویر خودکار پر شد", added, placed)
	if len(problems) > 0 {
		s.draftBack(w, r, id, "err", msg+" — این تصویرها پذیرفته نشد: "+strings.Join(problems, " · "), "#images")
		return
	}
	s.draftBack(w, r, id, "msg", msg, "#images")
}
