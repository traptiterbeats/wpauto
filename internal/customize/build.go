package customize

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// LogoID is the image id the client's logo gets in the built package.
const LogoID = "wpauto-logo"

// Apply returns a copy of demo.json with the draft's texts, images and logo.
func Apply(demo *Demo, d *Draft) (map[string]any, map[string]string, error) {
	b, _ := json.Marshal(demo.JSON)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	known := map[string]Field{}
	for _, f := range demo.Fields {
		known[f.Path] = f
	}
	for p, v := range d.Values {
		f, ok := known[p]
		if !ok {
			continue // a field the demo no longer has
		}
		if err := Set(out, p, Clean(f.Kind, v)); err != nil {
			return nil, nil, err
		}
	}
	files := map[string]string{} // package path → draft file
	imgs, _ := out["images"].([]any)
	for _, it := range imgs {
		m, _ := it.(map[string]any)
		id := fmt.Sprint(m["id"])
		if f, ok := d.Images[id]; ok && m["id"] != nil {
			rel := "images/client-" + safeName(id) + path.Ext(f)
			m["file"] = rel
			files[rel] = f
			if d.Brief.SiteName != "" {
				m["alt"] = d.Brief.SiteName
			}
		}
	}
	if d.Logo != "" && demo.LogoMod != "" {
		rel := "images/" + LogoID + path.Ext(d.Logo)
		files[rel] = d.Logo
		out["images"] = append(imgs, map[string]any{"id": LogoID, "file": rel, "alt": d.Brief.SiteName, "title": d.Brief.SiteName})
		mods, _ := out["theme_mods"].(map[string]any)
		if mods == nil {
			mods = map[string]any{}
			out["theme_mods"] = mods
		}
		mods[demo.LogoMod] = "@image:" + LogoID
	}
	return out, files, nil
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, s)
}

// Build writes the customised demo package to the draft folder and returns its path.
func Build(demo *Demo, d *Draft) (string, error) {
	data, files, err := Apply(demo, d)
	if err != nil {
		return "", err
	}
	src, err := zip.OpenReader(demo.zipPath)
	if err != nil {
		return "", err
	}
	defer src.Close()
	// Saayeh imports only the files images[] lists, so a replaced demo image is left out.
	used := map[string]bool{}
	if imgs, ok := data["images"].([]any); ok {
		for _, it := range imgs {
			if m, ok := it.(map[string]any); ok {
				used[strings.TrimPrefix(fmt.Sprint(m["file"]), "./")] = true
			}
		}
	}
	outPath := d.File("demo-custom.zip")
	tmp := outPath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	zw := zip.NewWriter(f)
	fail := func(err error) (string, error) { zw.Close(); f.Close(); os.Remove(tmp); return "", err }
	for _, e := range src.File {
		rel, ok := strings.CutPrefix(e.Name, demo.prefix)
		if !ok || rel == "" || strings.HasSuffix(rel, "/") || rel == "demo.json" || rel == "customize.json" {
			continue
		}
		if _, replaced := files[rel]; replaced {
			continue
		}
		if ext := strings.ToLower(path.Ext(rel)); ext != ".json" && !used[rel] {
			continue
		}
		w, err := zw.Create(rel)
		if err != nil {
			return fail(err)
		}
		rc, err := e.Open()
		if err != nil {
			return fail(err)
		}
		_, err = io.Copy(w, rc)
		rc.Close()
		if err != nil {
			return fail(err)
		}
	}
	for rel, name := range files {
		b, err := os.ReadFile(d.File(name))
		if err != nil {
			return fail(fmt.Errorf("تصویر %s پیدا نشد: %w", name, err))
		}
		w, err := zw.Create(rel)
		if err != nil {
			return fail(err)
		}
		if _, err := w.Write(b); err != nil {
			return fail(err)
		}
	}
	js, _ := json.MarshalIndent(data, "", "  ")
	w, err := zw.Create("demo.json")
	if err != nil {
		return fail(err)
	}
	if _, err := w.Write(js); err != nil {
		return fail(err)
	}
	if err := zw.Close(); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return outPath, os.Rename(tmp, outPath)
}
