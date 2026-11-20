package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/traptiterbeats/wpauto/internal/ai"
	"github.com/traptiterbeats/wpauto/internal/catalog"
	"github.com/traptiterbeats/wpauto/internal/customize"
	"github.com/traptiterbeats/wpauto/internal/engine"
)

// ---- AI providers ----

func (s *Server) handleAI(w http.ResponseWriter, r *http.Request) {
	s.render(w, "ai.html", s.page(r, map[string]any{
		"Providers": s.ai.List(), "Kinds": ai.Kinds,
		"Error": r.URL.Query().Get("err"), "Msg": r.URL.Query().Get("msg"),
	}))
}

func (s *Server) aiBack(w http.ResponseWriter, r *http.Request, key, msg string) {
	target := s.cfg.BasePath + "ai"
	if b := s.safeBack(r.FormValue("back")); b != "" {
		target = b
	}
	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	http.Redirect(w, r, target+sep+key+"="+url.QueryEscape(msg), http.StatusSeeOther)
}

// testAI checks a provider with a tiny request and records the result.
func (s *Server) testAI(ctx context.Context, p ai.Provider) error {
	c, err := s.ai.Client(p)
	if err == nil {
		tctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		err = c.Test(tctx)
		cancel()
	}
	s.ai.SetTest(p.ID, err)
	return err
}

func (s *Server) handleAIAdd(w http.ResponseWriter, r *http.Request) {
	p, err := s.ai.Add(ai.Provider{Name: r.FormValue("name"), Kind: r.FormValue("kind"), BaseURL: r.FormValue("base_url"), Model: r.FormValue("model")}, r.FormValue("key"))
	if err != nil {
		s.aiBack(w, r, "err", err.Error())
		return
	}
	if p.Model == "" {
		// no model typed: load the provider's list and let the user pick
		http.Redirect(w, r, s.cfg.BasePath+"ai/"+p.ID+"/models", http.StatusSeeOther)
		return
	}
	if err := s.testAI(r.Context(), p); err != nil {
		s.aiBack(w, r, "err", "«"+p.Name+"» اضافه شد ولی تستش ناموفق بود: "+err.Error()+" — با دکمه‌ی «انتخاب مدل» مدل دیگری بردارید.")
		return
	}
	s.aiBack(w, r, "msg", "✔ «"+p.Name+"» اضافه شد و تستش موفق بود")
}

// handleAIModels loads the provider's model list so the user picks the main model.
func (s *Server) handleAIModels(w http.ResponseWriter, r *http.Request) {
	p, ok := s.ai.Get(r.PathValue("id"))
	if !ok || r.PathValue("id") == "" {
		s.aiBack(w, r, "err", "سرویس پیدا نشد")
		return
	}
	data := map[string]any{"P": p, "Crumb": "انتخاب مدل · " + p.Name, "Error": r.URL.Query().Get("err")}
	c, err := s.ai.Client(p)
	if err == nil {
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		var models []ai.Model
		models, err = c.Models(ctx)
		cancel()
		data["Models"] = models
		chat := 0
		for _, m := range models {
			if m.Chat {
				chat++
			}
		}
		data["ChatCount"] = chat
	}
	if err != nil {
		data["LoadErr"] = err.Error()
	}
	s.render(w, "ai_models.html", s.page(r, data))
}

func (s *Server) handleAIModelSet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	model := r.FormValue("model")
	if m := strings.TrimSpace(r.FormValue("custom")); m != "" {
		model = m
	}
	p, err := s.ai.SetModel(id, model)
	if err != nil {
		http.Redirect(w, r, s.cfg.BasePath+"ai/"+id+"/models?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	if err := s.testAI(r.Context(), p); err != nil {
		http.Redirect(w, r, s.cfg.BasePath+"ai/"+id+"/models?err="+url.QueryEscape("مدل «"+p.Model+"» ذخیره شد ولی تستش ناموفق بود: "+err.Error()+" — مدل دیگری انتخاب کنید."), http.StatusSeeOther)
		return
	}
	s.aiBack(w, r, "msg", "✔ مدل اصلی «"+p.Name+"» شد "+p.Model+" و تستش موفق بود")
}

func (s *Server) handleAIOp(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, ok := s.ai.Get(id)
	if !ok || id == "" {
		s.aiBack(w, r, "err", "سرویس پیدا نشد")
		return
	}
	switch r.PathValue("op") {
	case "delete":
		_ = s.ai.Delete(id)
		s.aiBack(w, r, "msg", "حذف شد")
	case "default":
		_ = s.ai.SetDefault(id)
		s.aiBack(w, r, "msg", "«"+p.Name+"» پیش‌فرض شد")
	case "test":
		if err := s.testAI(r.Context(), p); err != nil {
			s.aiBack(w, r, "err", "«"+p.Name+"»: "+err.Error())
			return
		}
		s.aiBack(w, r, "msg", "✔ «"+p.Name+"» ("+p.Model+") درست جواب داد")
	default:
		http.NotFound(w, r)
	}
}

// ---- drafts ----

func resetStuckDrafts() {
	st := engine.Drafts()
	for _, d := range st.List() {
		if d.Status == "generating" {
			_, _ = st.Update(d.ID, func(d *customize.Draft) error {
				d.Status, d.Error, d.Progress = "error", "پنل وسط تولید متن‌ها دوباره راه‌اندازی شد؛ دوباره بزنید", ""
				return nil
			})
		}
	}
}

func (s *Server) handleDrafts(w http.ResponseWriter, r *http.Request) {
	var themes []*catalog.Theme
	for _, t := range engine.Catalog().List() {
		if t.HasDemo() {
			themes = append(themes, t)
		}
	}
	s.render(w, "drafts.html", s.page(r, map[string]any{
		"Drafts": engine.Drafts().List(), "Themes": themes, "Error": r.URL.Query().Get("err"),
	}))
}

func (s *Server) handleDraftNew(w http.ResponseWriter, r *http.Request) {
	t, err := engine.Catalog().Get(r.FormValue("theme"))
	title := strings.TrimSpace(r.FormValue("title"))
	if err != nil || !t.HasDemo() {
		http.Redirect(w, r, s.cfg.BasePath+"customize?err="+url.QueryEscape("قالبی با دمو انتخاب کنید"), http.StatusSeeOther)
		return
	}
	if title == "" {
		http.Redirect(w, r, s.cfg.BasePath+"customize?err="+url.QueryEscape("نام سایت یا مشتری را بنویسید"), http.StatusSeeOther)
		return
	}
	d, err := engine.Drafts().New(t.Slug, title)
	if err != nil {
		http.Redirect(w, r, s.cfg.BasePath+"customize?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, s.cfg.BasePath+"customize/"+d.ID, http.StatusSeeOther)
}

type fieldView struct {
	customize.Field
	Value   string
	Changed bool
}

type groupView struct {
	Name   string
	Fields []fieldView
}

type slotView struct {
	customize.ImageSlot
	Upload string
}

func (s *Server) loadDraft(r *http.Request) (*customize.Draft, *catalog.Theme, *customize.Demo, error) {
	d, err := engine.Drafts().Get(r.PathValue("id"))
	if err != nil {
		return nil, nil, nil, err
	}
	t, err := engine.Catalog().Get(d.Theme)
	if err != nil || !t.HasDemo() {
		return d, nil, nil, fmt.Errorf("قالب %s دیگر در کاتالوگ نیست یا دمو ندارد", d.Theme)
	}
	demo, err := customize.LoadDemo(t.Path(t.DemoZip))
	return d, t, demo, err
}

func (s *Server) draftBack(w http.ResponseWriter, r *http.Request, id, key, msg, anchor string) {
	http.Redirect(w, r, s.cfg.BasePath+"customize/"+id+"?"+key+"="+url.QueryEscape(msg)+anchor, http.StatusSeeOther)
}

func (s *Server) handleDraft(w http.ResponseWriter, r *http.Request) {
	d, t, demo, err := s.loadDraft(r)
	if d == nil {
		http.NotFound(w, r)
		return
	}
	data := map[string]any{"D": d, "Theme": t, "Providers": s.ai.List(), "Socials": customize.Socials, "Crumb": d.Title,
		"Error": r.URL.Query().Get("err"), "Msg": r.URL.Query().Get("msg")}
	if err != nil {
		data["Error"] = err.Error()
	}
	if demo != nil {
		var groups []groupView
		idx := map[string]int{}
		changed := 0
		fromBrief := map[string]string{}
		customize.FromBrief(demo.Fields, d.Brief, fromBrief)
		for _, f := range demo.Fields {
			v, ok := d.Values[f.Path]
			if bv, isBrief := fromBrief[f.Path]; isBrief && f.Data {
				v, ok = bv, true
			}
			if !ok {
				v = f.Original
			}
			fv := fieldView{Field: f, Value: v, Changed: ok && v != f.Original}
			if fv.Changed {
				changed++
			}
			i, ok := idx[f.Group]
			if !ok {
				i = len(groups)
				idx[f.Group] = i
				groups = append(groups, groupView{Name: f.Group})
			}
			groups[i].Fields = append(groups[i].Fields, fv)
		}
		var slots []slotView
		for _, sl := range demo.Images {
			slots = append(slots, slotView{ImageSlot: sl, Upload: d.Images[sl.ID]})
		}
		data["Groups"], data["Slots"], data["Changed"], data["FieldCount"] = groups, slots, changed, len(demo.Fields)
		data["LogoMod"], data["LogoText"] = demo.LogoMod, demo.LogoText
		data["HasSpec"] = demo.Spec != nil
		if demo.Spec != nil {
			data["Supports"] = demo.Spec.Supports
		}
	}
	data["Steps"] = s.draftSteps(d)
	s.render(w, "draft.html", s.page(r, data))
}

func (s *Server) handleDraftFile(w http.ResponseWriter, r *http.Request) {
	d, err := engine.Drafts().Get(r.PathValue("id"))
	name := r.PathValue("name")
	if err != nil || name == "draft.json" || strings.HasPrefix(name, ".") {
		http.NotFound(w, r)
		return
	}
	if name == "demo-custom.zip" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+d.Theme+`-custom-demo.zip"`)
	}
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, d.File(name))
}

func (s *Server) handleDraftStatus(w http.ResponseWriter, r *http.Request) {
	d, err := engine.Drafts().Get(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": d.Status, "progress": d.Progress, "error": d.Error})
}

func (s *Server) handleDraftImages(w http.ResponseWriter, r *http.Request) {
	d, _, demo, err := s.loadDraft(r)
	if err != nil {
		if d == nil {
			http.NotFound(w, r)
			return
		}
		s.draftBack(w, r, d.ID, "err", err.Error(), "#images")
		return
	}
	read := func(field string) ([]byte, bool, error) {
		fhs := r.MultipartForm.File[field]
		if len(fhs) == 0 || fhs[0].Size == 0 {
			return nil, false, nil
		}
		f, err := fhs[0].Open()
		if err != nil {
			return nil, false, err
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, customize.MaxImage+1))
		return b, true, err
	}
	n := 0
	_, err = engine.Drafts().Update(d.ID, func(d *customize.Draft) error {
		if b, ok, err := read("logo"); err != nil {
			return err
		} else if ok {
			name, err := d.SaveImage("logo", b)
			if err != nil {
				return fmt.Errorf("لوگو: %w", err)
			}
			d.Logo = name
			n++
		}
		for i, sl := range demo.Images {
			b, ok, err := read(fmt.Sprintf("img_%d", i))
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			name, err := d.SaveImage(fmt.Sprintf("img-%d", i), b)
			if err != nil {
				return fmt.Errorf("%s: %w", sl.Label, err)
			}
			d.Images[sl.ID] = name
			n++
		}
		return nil
	})
	if err != nil {
		s.draftBack(w, r, d.ID, "err", err.Error(), "#images")
		return
	}
	s.draftBack(w, r, d.ID, "msg", fmt.Sprintf("%d تصویر ذخیره شد", n), "#images")
}

func (s *Server) handleDraftOp(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	st := engine.Drafts()
	switch r.PathValue("op") {
	case "delete":
		_ = st.Delete(id)
		http.Redirect(w, r, s.cfg.BasePath+"customize", http.StatusSeeOther)
	case "brief":
		_, err := st.Update(id, func(d *customize.Draft) error { s.briefFromForm(r, d); return nil })
		if err != nil {
			s.draftBack(w, r, id, "err", err.Error(), "#brief")
			return
		}
		s.draftBack(w, r, id, "msg", "بریف ذخیره شد", "#brief")
	case "unset":
		slot := r.FormValue("slot")
		_, _ = st.Update(id, func(d *customize.Draft) error {
			if slot == "logo" {
				_ = os.Remove(d.File(d.Logo))
				d.Logo = ""
			} else if f, ok := d.Images[slot]; ok {
				_ = os.Remove(d.File(f))
				delete(d.Images, slot)
			}
			return nil
		})
		s.draftBack(w, r, id, "msg", "تصویر دمو برگردانده شد", "#images")
	case "values":
		_, _, demo, err := s.loadDraft(r)
		if err != nil {
			s.draftBack(w, r, id, "err", err.Error(), "#texts")
			return
		}
		_, err = st.Update(id, func(d *customize.Draft) error {
			for _, f := range demo.Fields {
				v, ok := r.PostForm["v:"+f.Path]
				if !ok || (f.Data && f.Role != "brand.latin") {
					continue
				}
				val := strings.ReplaceAll(strings.TrimSpace(v[0]), "\r\n", "\n")
				if val == "" || val == f.Original {
					delete(d.Values, f.Path)
				} else {
					d.Values[f.Path] = customize.Clean(f, val)
				}
			}
			return nil
		})
		if err != nil {
			s.draftBack(w, r, id, "err", err.Error(), "#texts")
			return
		}
		s.draftBack(w, r, id, "msg", "متن‌ها ذخیره شد", "#texts")
	case "reset":
		_, _ = st.Update(id, func(d *customize.Draft) error { d.Values = map[string]string{}; d.Generated = 0; return nil })
		s.draftBack(w, r, id, "msg", "همه‌ی متن‌ها به دموی اصلی برگشت", "#texts")
	case "autofill":
		s.autofillBrief(w, r, id)
	case "generate":
		s.startGenerate(w, r, id)
	case "build":
		d, _, demo, err := s.loadDraft(r)
		if err == nil {
			_, err = customize.Build(demo, d)
		}
		if err != nil {
			s.draftBack(w, r, id, "err", err.Error(), "#install")
			return
		}
		http.Redirect(w, r, s.cfg.BasePath+"customize/"+id+"/file/demo-custom.zip", http.StatusSeeOther)
	default:
		http.NotFound(w, r)
	}
}

// briefFromForm reads the brief form into the draft.
func (s *Server) briefFromForm(r *http.Request, d *customize.Draft) {
	f := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	d.Brief = customize.Brief{Story: f("story"), SiteName: f("site_name"), BrandLatin: f("brand_latin"), Business: f("business"), About: f("about"),
		Services: f("services"), Audience: f("audience"), Tone: f("tone"), Email: f("email"), Phone: f("phone"),
		Address: f("address"), Hours: f("hours"), Notes: f("notes"), Language: f("language"),
		Socials: map[string]string{}}
	for _, sn := range customize.Socials {
		if v := f("social_" + sn.ID); v != "" {
			d.Brief.Socials[sn.ID] = v
		}
	}
	if f("use_colors") != "" {
		d.Brief.ColorPrimary, d.Brief.ColorSecondary = f("color_primary"), f("color_secondary")
	}
	if d.Brief.SiteName != "" {
		d.Title = d.Brief.SiteName
	}
}

// readyAI returns the chosen (or default) provider's client, testing it first when needed.
func (s *Server) readyAI(r *http.Request) (ai.Provider, *ai.Client, error) {
	p, ok := s.ai.Get(r.FormValue("provider"))
	if !ok {
		return p, nil, fmt.Errorf("هوش مصنوعی وصل نیست. اول در منوی «هوش مصنوعی» یک سرویس اضافه و تست کنید.")
	}
	if p.Model == "" {
		return p, nil, fmt.Errorf("برای «%s» هنوز مدلی انتخاب نشده. اول در منوی «هوش مصنوعی» مدل را انتخاب کنید.", p.Name)
	}
	if !p.Tested() || !p.TestOK {
		if err := s.testAI(r.Context(), p); err != nil {
			return p, nil, fmt.Errorf("هوش مصنوعی «%s» جواب نمی‌دهد: %v — اول در منوی «هوش مصنوعی» درستش کنید.", p.Name, err)
		}
	}
	c, err := s.ai.Client(p)
	return p, c, err
}

// autofillBrief saves the form, lets the AI fill the brief from the free description, and
// optionally goes straight on to writing the site texts.
func (s *Server) autofillBrief(w http.ResponseWriter, r *http.Request, id string) {
	st := engine.Drafts()
	d, err := st.Update(id, func(d *customize.Draft) error { s.briefFromForm(r, d); return nil })
	if err != nil {
		s.draftBack(w, r, id, "err", err.Error(), "#brief")
		return
	}
	if d.Status == "generating" {
		s.draftBack(w, r, id, "err", "تولید متن‌ها در جریان است", "#brief")
		return
	}
	if strings.TrimSpace(d.Brief.Story) == "" {
		s.draftBack(w, r, id, "err", "اول در کادر «درباره‌ی مشتری و خواسته‌هایش» توضیح بدهید", "#brief")
		return
	}
	p, client, err := s.readyAI(r)
	if err != nil {
		s.draftBack(w, r, id, "err", err.Error(), "#brief")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	b, _, err := customize.FillBrief(ctx, client, d.Brief, r.FormValue("overwrite") != "")
	cancel()
	if err != nil {
		s.ai.SetTest(p.ID, err)
		s.draftBack(w, r, id, "err", "پر کردن بریف نشد: "+err.Error(), "#brief")
		return
	}
	_, err = st.Update(id, func(d *customize.Draft) error {
		d.Brief = b
		if b.SiteName != "" {
			d.Title = b.SiteName
		}
		return nil
	})
	if err != nil {
		s.draftBack(w, r, id, "err", err.Error(), "#brief")
		return
	}
	if r.FormValue("then_texts") != "" {
		s.startGenerate(w, r, id)
		return
	}
	s.draftBack(w, r, id, "msg", "✔ خانه‌های بریف با «"+p.Name+"» پر شد. بررسی کنید، اگر لازم بود اصلاح کنید و ذخیره بزنید.", "#brief")
}

func (s *Server) startGenerate(w http.ResponseWriter, r *http.Request, id string) {
	d, _, demo, err := s.loadDraft(r)
	if err != nil {
		if d == nil {
			http.NotFound(w, r)
			return
		}
		s.draftBack(w, r, id, "err", err.Error(), "#texts")
		return
	}
	if d.Status == "generating" {
		s.draftBack(w, r, id, "err", "تولید متن‌ها در جریان است", "#texts")
		return
	}
	if strings.TrimSpace(d.Brief.Business+d.Brief.About+d.Brief.Services) == "" {
		s.draftBack(w, r, id, "err", "اول بریف را پر کنید (حداقل حوزه‌ی کار یا معرفی کسب‌وکار)", "#brief")
		return
	}
	p, client, err := s.readyAI(r)
	if err != nil {
		s.draftBack(w, r, id, "err", err.Error(), "#texts")
		return
	}
	st := engine.Drafts()
	_, _ = st.Update(id, func(d *customize.Draft) error {
		d.Status, d.Error, d.Progress, d.Provider = "generating", "", "در حال ارسال به "+p.Name, p.Name+" · "+p.Model
		return nil
	})
	brief := d.Brief
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		values, tokens, err := customize.Generate(ctx, client, demo, brief, func(done, total int) {
			_, _ = st.Update(id, func(d *customize.Draft) error {
				d.Progress = fmt.Sprintf("بخش %d از %d", done+1, total)
				if done >= total {
					d.Progress = "در حال ذخیره"
				}
				return nil
			})
		})
		_, _ = st.Update(id, func(d *customize.Draft) error {
			d.Tokens += tokens
			d.Progress = ""
			if len(values) > 0 {
				for k, v := range values {
					d.Values[k] = v
				}
				d.Generated = len(values)
			}
			if err == nil || len(values) > 0 {
				s.ai.SetTest(p.ID, nil)
			}
			if err != nil {
				d.Status, d.Error = "error", err.Error()
				if len(values) > 0 {
					d.Error += fmt.Sprintf(" (%d متن نوشته شد و ذخیره شد)", len(values))
				}
				return nil
			}
			d.Status = "ready"
			return nil
		})
	}()
	s.draftBack(w, r, id, "msg", "تولید متن‌ها شروع شد", "#texts")
}

// draftSteps is the side panel checklist of one customisation draft.
func (s *Server) draftSteps(d *customize.Draft) []sideStep {
	state := func(done bool) string {
		if done {
			return "done"
		}
		return "todo"
	}
	b := d.Brief
	steps := []sideStep{
		{ID: "brief", Title: "۱. بریف مشتری", State: state(b.SiteName != "" && strings.TrimSpace(b.Business+b.About+b.Services) != ""), Link: "#brief"},
		{ID: "images", Title: "۲. لوگو و تصاویر", State: "optional", Note: "اختیاری", Link: "#images"},
		{ID: "texts", Title: "۳. متن‌ها با هوش مصنوعی", State: state(d.Generated > 0), Link: "#texts"},
		{ID: "install", Title: "۴. نصب روی سایت", State: "todo", Link: "#install"},
	}
	if d.Logo != "" || len(d.Images) > 0 {
		steps[1].State, steps[1].Note = "done", ""
	}
	if d.Status == "generating" {
		steps[2].State, steps[2].Note = "maybe", "در حال نوشتن…"
	} else if d.Generated == 0 {
		p, ok := s.ai.Get("")
		switch {
		case !ok:
			steps[2].State, steps[2].Note, steps[2].Link = "blocked", "اول هوش مصنوعی را وصل کنید", s.cfg.BasePath+"ai"
		case p.Model == "":
			steps[2].State, steps[2].Note, steps[2].Link = "blocked", "اول مدل هوش مصنوعی را انتخاب کنید", s.cfg.BasePath+"ai/"+p.ID+"/models"
		case p.Tested() && !p.TestOK:
			steps[2].State, steps[2].Note, steps[2].Link = "blocked", "تست هوش مصنوعی ناموفق است", s.cfg.BasePath+"ai"
		}
	}
	for _, run := range s.runner.List(200) {
		if run.WorkflowID == "theme-install" && run.Inputs["draft"] == d.ID && run.Status == engine.StatusSuccess {
			steps[3].State, steps[3].Note = "done", run.Inputs["domain"]
			break
		}
	}
	return steps
}
