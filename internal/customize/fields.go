// Package customize turns a theme's Saayeh demo into a client's site before it is imported:
// a brief, the client's logo and images, and AI-rewritten texts, saved as a draft and built
// into a new demo package that theme.demo uploads instead of the original.
package customize

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Field is one text in demo.json that can be changed.
type Field struct {
	Path     string   `json:"path"`
	Label    string   `json:"label"`
	Group    string   `json:"group"`
	Kind     string   `json:"kind"` // short | text | html | inline | email | url | color
	Hint     string   `json:"hint,omitempty"`
	Original string   `json:"original"`
	Role     string   `json:"role,omitempty"`  // contract role, e.g. hero.title, contact.email
	Allow    []string `json:"allow,omitempty"` // inline kind: allowed tags, "tag" or "tag.class"
	AI       bool     `json:"ai"`              // the AI writes it
	Data     bool     `json:"data"`            // filled from the brief (contact, socials, colours, names)
	Keep     bool     `json:"keep,omitempty"`  // keep the demo value when the brief has none
}

// ImageSlot is a demo image the client can replace. Contract v2 slots also have a standard
// ratio, an output size, a priority and a group (see images.go).
type ImageSlot struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	File     string `json:"file"`
	Ratio    string `json:"ratio,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Priority int    `json:"priority,omitempty"`
	Group    string `json:"group,omitempty"`
}

// Spec is the optional customize.json a theme author puts next to demo.json.
type Spec struct {
	Contract string   `json:"contract,omitempty"` // "wpauto-customize"
	Version  int      `json:"version"`
	Supports []string `json:"supports,omitempty"`
	Sections []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	} `json:"sections,omitempty"`
	// Auto also adds the automatically found fields that Fields does not list (default: only
	// when Fields is empty).
	Auto   *bool       `json:"auto,omitempty"`
	Fields []SpecField `json:"fields"`
	Logo   *struct {
		ThemeMod string `json:"theme_mod"`
		Label    string `json:"label"`
	} `json:"logo,omitempty"`
	Images []SpecImage `json:"images,omitempty"`
	// Products (v2) declares that the theme's demo has products.
	Products *SpecProducts `json:"products,omitempty"`
}

// SpecField describes one field in customize.json.
type SpecField struct {
	Path    string   `json:"path"`
	Label   string   `json:"label"`
	Section string   `json:"section,omitempty"`
	Group   string   `json:"group,omitempty"` // free-form group label (when no sections)
	Kind    string   `json:"kind,omitempty"`
	Hint    string   `json:"hint,omitempty"`
	Role    string   `json:"role,omitempty"`
	Allow   []string `json:"allow,omitempty"`
	AI      *bool    `json:"ai,omitempty"`
	Keep    bool     `json:"keep,omitempty"`
	From    string   `json:"from,omitempty"` // legacy: site_name, email, phone, address
}

var legacyFrom = map[string]string{"site_name": "site.name", "email": "contact.email", "phone": "contact.phone", "address": "contact.address"}

// newField sets the AI/Data flags from the role and kind.
func newField(f Field, ai *bool) Field {
	if f.Kind == "" {
		f.Kind = kindOf(f.Original)
	}
	switch {
	case f.Role == "contact.email":
		f.Kind = "email"
	case strings.HasPrefix(f.Role, "color."):
		f.Kind = "color"
	case strings.HasPrefix(f.Role, "social."):
		f.Kind = "url"
	}
	f.Data = dataRole(f.Role)
	f.AI = !f.Data && f.Kind != "email" && f.Kind != "url" && f.Kind != "color"
	if f.Role == "brand.latin" {
		f.AI = true // written by the AI when the brief has no Latin name
	}
	if ai != nil {
		f.AI = *ai && !(f.Data && f.Role != "brand.latin")
	}
	return f
}

// Demo is a parsed demo package.
type Demo struct {
	JSON     map[string]any
	Spec     *Spec
	Fields   []Field
	Images   []ImageSlot
	LogoMod  string // theme mod the logo goes to ("" = no logo slot)
	LogoText string
	// V2 is set for a customize.json of contract version 2 (sized image slots, products).
	V2 bool
	// Products is the v2 products declaration (nil = the theme declares none).
	Products *SpecProducts
	// Warnings are problems found in customize.json (shown on the draft page).
	Warnings []string
	prefix   string
	zipPath  string
}

// LoadDemo reads a Saayeh demo zip and lists what can be customised.
func LoadDemo(zipPath string) (*Demo, error) {
	z, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, err
	}
	defer z.Close()
	d := &Demo{zipPath: zipPath}
	var demoF, specF *zip.File
	for _, f := range z.File {
		switch path.Base(f.Name) {
		case "demo.json":
			if demoF == nil || len(f.Name) < len(demoF.Name) {
				demoF = f
			}
		case "customize.json":
			if specF == nil || len(f.Name) < len(specF.Name) {
				specF = f
			}
		}
	}
	if demoF == nil {
		return nil, errors.New("demo.json پیدا نشد")
	}
	d.prefix = strings.TrimSuffix(demoF.Name, "demo.json")
	if err := readJSON(demoF, &d.JSON); err != nil {
		return nil, fmt.Errorf("demo.json: %w", err)
	}
	if specF != nil && strings.TrimSuffix(specF.Name, "customize.json") == d.prefix {
		var s Spec
		if err := readJSON(specF, &s); err != nil {
			return nil, fmt.Errorf("customize.json: %w", err)
		}
		d.Spec = &s
	}
	d.scan()
	return d, nil
}

func readJSON(f *zip.File, v any) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, 64<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

var (
	reHTML  = regexp.MustCompile(`<[a-zA-Z/][^>]*>`)
	reColor = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)
	reEmail = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[a-z]{2,}$`)
	reToken = regexp.MustCompile(`^[a-z0-9_-]{1,12}$`)
	rePhone = regexp.MustCompile(`^[+0-9۰-۹ ()-]{6,}$`)
)

func kindOf(v string) string {
	switch {
	case reHTML.MatchString(v):
		return "html"
	case len([]rune(v)) > 120 || strings.Contains(v, "\n"):
		return "text"
	}
	return "short"
}

// textual reports whether an auto-found value is prose worth rewriting.
func textual(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || strings.HasPrefix(v, "@image:") || strings.HasPrefix(v, "#") || strings.HasPrefix(v, "http") || strings.HasPrefix(v, "/") {
		return false
	}
	if _, err := strconv.ParseFloat(v, 64); err == nil {
		return false
	}
	return !reToken.MatchString(v)
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// sel addresses an item of a list by slug when it has one, else by index.
func sel(list string, i int, item map[string]any) string {
	if s := str(item, "slug"); s != "" && !strings.ContainsAny(s, "[].=") {
		return fmt.Sprintf("%s[slug=%s]", list, s)
	}
	return fmt.Sprintf("%s.%d", list, i)
}

func (d *Demo) auto() []Field {
	var out []Field
	add := func(p, label, group, v string) {
		out = append(out, newField(Field{Path: p, Label: label, Group: group, Original: v, Role: autoRole(label, v)}, nil))
	}
	opts, _ := d.JSON["options"].(map[string]any)
	out = append(out, newField(Field{Path: "options.blogname", Label: "نام سایت", Group: "عمومی", Kind: "short", Original: str(opts, "blogname"), Role: "site.name"}, nil))
	if v := str(opts, "blogdescription"); v != "" {
		add("options.blogdescription", "معرفی کوتاه سایت", "عمومی", v)
	}
	if mods, ok := d.JSON["theme_mods"].(map[string]any); ok {
		for _, k := range sortedKeys(mods) {
			if v, ok := mods[k].(string); ok && (textual(v) || autoRole(k, v) != "") {
				f := newField(Field{Path: "theme_mods." + k, Label: k, Group: "تنظیمات قالب", Original: v, Role: autoRole(k, v)}, nil)
				if strings.HasPrefix(f.Role, "social.") {
					f.Kind, f.AI = "url", false
				}
				out = append(out, f)
			}
		}
	}
	list := func(name, group string, keys ...string) {
		items, _ := d.JSON[name].([]any)
		for i, it := range items {
			m, ok := it.(map[string]any)
			if !ok {
				continue
			}
			base := sel(name, i, m)
			title := str(m, "title")
			if title == "" {
				title = str(m, "name")
			}
			g := group + ": " + title
			for _, k := range keys {
				if v := str(m, k); textual(v) {
					add(base+"."+k, k, g, v)
				}
			}
			if meta, ok := m["meta"].(map[string]any); ok {
				for _, k := range sortedKeys(meta) {
					if v, ok := meta[k].(string); ok && !strings.HasPrefix(k, "_") && textual(v) && (len([]rune(v)) > 3 || strings.Contains(v, " ")) {
						add(base+".meta."+k, k, g, v)
					}
				}
			}
		}
	}
	list("pages", "برگه", "title", "excerpt", "content")
	list("posts", "نوشته", "title", "excerpt", "content")
	list("product_categories", "دسته‌ی محصول", "name", "description")
	list("products", "محصول", "name", "short_description", "description")
	if menus, ok := d.JSON["menus"].([]any); ok {
		for i, mn := range menus {
			m, _ := mn.(map[string]any)
			items, _ := m["items"].([]any)
			for j, it := range items {
				im, _ := it.(map[string]any)
				if v := str(im, "title"); textual(v) {
					add(fmt.Sprintf("menus.%d.items.%d.title", i, j), "گزینه‌ی منو", "منو: "+str(m, "name"), v)
				}
			}
		}
	}
	return out
}

func (d *Demo) scan() {
	d.V2 = d.Spec != nil && d.Spec.Version >= 2
	if d.V2 && d.Spec.Products != nil {
		p := *d.Spec.Products
		if p.PostType == "" {
			p.PostType = "product"
		}
		if p.DefaultMode != ProductsAI {
			if p.DefaultMode != "" && p.DefaultMode != ProductsKeep {
				d.Warnings = append(d.Warnings, "products.default_mode باید keep یا ai باشد؛ keep فرض شد")
			}
			p.DefaultMode = ProductsKeep
		}
		d.Products = &p
	}
	labels := map[string]string{"title": "عنوان", "excerpt": "خلاصه", "content": "متن", "name": "نام", "description": "توضیحات", "short_description": "توضیح کوتاه"}
	auto := d.auto()
	for i := range auto {
		if l, ok := labels[auto[i].Label]; ok {
			auto[i].Label = l
		}
	}
	if d.Spec == nil || len(d.Spec.Fields) == 0 {
		d.Fields = auto
	} else {
		seen := map[string]bool{}
		sections := map[string]string{}
		for _, sec := range d.Spec.Sections {
			sections[sec.ID] = sec.Label
		}
		for _, sf := range d.Spec.Fields {
			v, err := Get(d.JSON, sf.Path)
			if err != nil && !strings.HasPrefix(sf.Path, "options.") && !strings.HasPrefix(sf.Path, "theme_mods.") {
				continue // a path the demo does not have
			}
			role := sf.Role
			if role == "" {
				role = legacyFrom[sf.From]
			}
			group := sf.Group
			if l, ok := sections[sf.Section]; ok {
				group = l
			} else if group == "" {
				group = sf.Section
			}
			if group == "" {
				group = "متن‌ها"
			}
			ai := sf.AI
			if d.V2 && productRole(role) && !productTextRole(role) && ai == nil {
				no := false
				ai = &no // prices, SKUs and other product data stay as in the demo unless marked ai
			}
			f := newField(Field{Path: sf.Path, Label: sf.Label, Group: group, Kind: sf.Kind, Hint: sf.Hint, Original: v,
				Role: role, Allow: sf.Allow, Keep: sf.Keep}, ai)
			seen[f.Path] = true
			d.Fields = append(d.Fields, f)
		}
		if d.Spec.Auto != nil && *d.Spec.Auto {
			for _, f := range auto {
				if !seen[f.Path] {
					d.Fields = append(d.Fields, f)
				}
			}
		}
	}
	// Images: every demo image is a slot unless customize.json names them.
	labelOf := map[string]string{}
	specOf := map[string]SpecImage{}
	named := false
	if d.Spec != nil {
		for _, im := range d.Spec.Images {
			labelOf[im.ID], named = im.Label, true
			specOf[im.ID] = im
		}
	}
	imgs, _ := d.JSON["images"].([]any)
	for _, it := range imgs {
		m, _ := it.(map[string]any)
		id := fmt.Sprint(m["id"])
		if m["id"] == nil || str(m, "file") == "" {
			continue
		}
		l, ok := labelOf[id]
		if named && !ok {
			continue
		}
		if l == "" {
			l = str(m, "title")
		}
		if l == "" {
			l = id
		}
		sl := ImageSlot{ID: id, Label: l, File: str(m, "file")}
		if d.V2 {
			im, ok := specOf[id]
			if !ok {
				im = SpecImage{ID: id}
			}
			d.Warnings = append(d.Warnings, sizeSlot(&sl, im)...)
		}
		d.Images = append(d.Images, sl)
	}
	if d.V2 {
		have := map[string]bool{}
		for _, sl := range d.Images {
			have[sl.ID] = true
		}
		for _, im := range d.Spec.Images {
			if !have[im.ID] {
				d.Warnings = append(d.Warnings, fmt.Sprintf("تصویر %s در demo.json نیست (images[].id)", im.ID))
			}
		}
	}
	d.LogoMod, d.LogoText = "custom_logo", "لوگو"
	if d.Spec != nil && d.Spec.Logo != nil {
		d.LogoMod = d.Spec.Logo.ThemeMod
		if d.Spec.Logo.Label != "" {
			d.LogoText = d.Spec.Logo.Label
		}
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// segment of a path: a key, an index, or a [key=value] selector on a list.
type segment struct {
	key        string
	index      int
	selK, selV string
}

var reSel = regexp.MustCompile(`^([a-z0-9_]+)\[([a-z0-9_]+)=([^\]]+)\]$`)

func parsePath(p string) ([]segment, error) {
	var out []segment
	for _, part := range strings.Split(p, ".") {
		if part == "" {
			return nil, fmt.Errorf("bad path %q", p)
		}
		if m := reSel.FindStringSubmatch(part); m != nil {
			out = append(out, segment{key: m[1], index: -1}, segment{index: -1, selK: m[2], selV: m[3]})
			continue
		}
		if n, err := strconv.Atoi(part); err == nil {
			out = append(out, segment{index: n})
			continue
		}
		out = append(out, segment{key: part, index: -1})
	}
	return out, nil
}

// walk returns the container and final segment for p, creating maps when create is set.
func walk(root map[string]any, p string, create bool) (any, segment, error) {
	segs, err := parsePath(p)
	if err != nil {
		return nil, segment{}, err
	}
	var cur any = root
	for i, s := range segs[:len(segs)-1] {
		var next any
		switch c := cur.(type) {
		case map[string]any:
			if s.key == "" {
				return nil, s, fmt.Errorf("%s: expected a key", p)
			}
			next = c[s.key]
			if next == nil && create && segs[i+1].key != "" {
				next = map[string]any{}
				c[s.key] = next
			}
		case []any:
			if s.selK != "" {
				for _, it := range c {
					if m, ok := it.(map[string]any); ok && fmt.Sprint(m[s.selK]) == s.selV {
						next = it
						break
					}
				}
			} else if s.index >= 0 && s.index < len(c) {
				next = c[s.index]
			}
		}
		if next == nil {
			return nil, s, fmt.Errorf("%s: not found", p)
		}
		cur = next
	}
	return cur, segs[len(segs)-1], nil
}

// Get reads a string at p.
func Get(root map[string]any, p string) (string, error) {
	c, last, err := walk(root, p, false)
	if err != nil {
		return "", err
	}
	m, ok := c.(map[string]any)
	if !ok || last.key == "" {
		return "", fmt.Errorf("%s: not a text field", p)
	}
	v, ok := m[last.key]
	if !ok {
		return "", fmt.Errorf("%s: not found", p)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s: not text", p)
	}
	return s, nil
}

// Set writes a string at p (parents must exist, except maps under the root).
func Set(root map[string]any, p, v string) error {
	c, last, err := walk(root, p, true)
	if err != nil {
		return err
	}
	m, ok := c.(map[string]any)
	if !ok || last.key == "" {
		return fmt.Errorf("%s: not a text field", p)
	}
	if old, exists := m[last.key]; exists {
		if _, isStr := old.(string); !isStr {
			return fmt.Errorf("%s: not text", p)
		}
	}
	m[last.key] = v
	return nil
}
