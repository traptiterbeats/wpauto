package web

// AutoPilot: one form, one button, and the whole site-building path runs by itself:
// AI customisation (nothing on the host changes yet) → WordPress install → theme + customised
// demo → a final check of the live site. The host and WordPress checks are left out on purpose:
// whoever starts AutoPilot knows the host is ready.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/traptiterbeats/wpauto/internal/catalog"
	"github.com/traptiterbeats/wpauto/internal/config"
	"github.com/traptiterbeats/wpauto/internal/customize"
	"github.com/traptiterbeats/wpauto/internal/engine"
	"github.com/traptiterbeats/wpauto/internal/projects"
	"github.com/traptiterbeats/wpauto/internal/wp"
)

// Stage states.
const (
	apWaiting = "waiting"
	apRunning = "running"
	apSuccess = "success"
	apFailed  = "failed"
	apSkipped = "skipped"
)

// Timings, shortened in tests.
var (
	// apRetryWaits are the pauses before the 2nd and 3rd AI customisation attempts.
	apRetryWaits = []time.Duration{30 * time.Second, 60 * time.Second}
	// apPoll is how often a stage looks at the run it started.
	apPoll = time.Second
	// apVerifyWaits are the pauses before fetching the home page again after a 503 or an error.
	apVerifyWaits = []time.Duration{10 * time.Second, 20 * time.Second, 30 * time.Second}
)

// apStage is one stage of an AutoPilot pipeline.
type apStage struct {
	Key      string    `json:"key"`
	Title    string    `json:"title"`
	State    string    `json:"state"`
	Error    string    `json:"error,omitempty"`
	Note     string    `json:"note,omitempty"`
	Warnings []string  `json:"warnings,omitempty"`
	RunID    string    `json:"run_id,omitempty"`
	Attempts int       `json:"attempts,omitempty"`
	Started  time.Time `json:"started,omitempty"`
	Ended    time.Time `json:"ended,omitempty"`
	Seconds  int64     `json:"seconds,omitempty"` // duration once the stage ended
}

// Secs is the stage's duration so far.
func (st apStage) Secs() int64 {
	switch {
	case st.State == apRunning && !st.Started.IsZero():
		return int64(time.Since(st.Started).Seconds())
	case !st.Ended.IsZero():
		return st.Seconds
	}
	return 0
}

// DurText is the duration as mm:ss or h:mm:ss ("" when the stage has not run).
func (st apStage) DurText() string {
	if st.Started.IsZero() {
		return ""
	}
	return fmtDur(st.Secs())
}

// apInputs are the form values a pipeline keeps. The admin password is never stored.
type apInputs struct {
	HostID     string `json:"host_id"`
	HostName   string `json:"host_name"`
	Domain     string `json:"domain"`
	Mode       string `json:"mode"`
	Name       string `json:"name"`
	Title      string `json:"title"`
	Locale     string `json:"locale"`
	AdminUser  string `json:"admin_user"`
	AdminEmail string `json:"admin_email"`
	Theme      string `json:"theme"`
	Provider   string `json:"provider,omitempty"`
}

type apLogLine struct {
	T     time.Time `json:"t"`
	Level string    `json:"level"`
	Msg   string    `json:"msg"`
}

// autoPilot is one pipeline, saved as <config>/autopilot/<id>.json.
type autoPilot struct {
	ID        string      `json:"id"`
	Title     string      `json:"title"`
	ProjectID string      `json:"project_id"`
	DraftID   string      `json:"draft_id"`
	In        apInputs    `json:"inputs"`
	Status    string      `json:"status"` // running | success | failed | cancelled
	Error     string      `json:"error,omitempty"`
	Stages    []apStage   `json:"stages"`
	Log       []apLogLine `json:"log,omitempty"`
	SiteURL   string      `json:"site_url,omitempty"`
	Created   time.Time   `json:"created"`
	// Started is when the current run of the pipeline began (a resume starts a new one);
	// PrevSeconds holds the time of earlier runs and TotalSeconds the final total.
	Started      time.Time `json:"started"`
	Ended        time.Time `json:"ended,omitempty"`
	PrevSeconds  int64     `json:"prev_seconds,omitempty"`
	TotalSeconds int64     `json:"total_seconds"`
}

// Done reports whether the pipeline stopped (finished, failed or cancelled).
func (p autoPilot) Done() bool { return p.Status != apRunning }

// Elapsed is the pipeline's working time in seconds.
func (p autoPilot) Elapsed() int64 {
	if p.Done() {
		return p.TotalSeconds
	}
	return p.PrevSeconds + int64(time.Since(p.Started).Seconds())
}

// ElapsedText is Elapsed as mm:ss or h:mm:ss.
func (p autoPilot) ElapsedText() string { return fmtDur(p.Elapsed()) }

// AdminURL is the wp-admin address of the finished site.
func (p autoPilot) AdminURL() string {
	if p.SiteURL == "" {
		return ""
	}
	return p.SiteURL + "wp-admin/"
}

// Resumable reports whether «ادامه از همین مرحله» makes sense.
func (p autoPilot) Resumable() bool { return p.Done() && p.Status != apSuccess }

func fmtDur(sec int64) string {
	if sec < 0 {
		sec = 0
	}
	h, m, s := sec/3600, (sec%3600)/60, sec%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

func newStages() []apStage {
	return []apStage{
		{Key: "customize", Title: "شخصی‌سازی با هوش مصنوعی", State: apWaiting},
		{Key: "wpinstall", Title: "نصب وردپرس", State: apWaiting},
		{Key: "theme", Title: "نصب قالب و درون‌ریزی دموی شخصی‌سازی‌شده", State: apWaiting},
		{Key: "verify", Title: "بررسی سایت", State: apWaiting},
	}
}

// ---- store ----

type apStore struct {
	mu      sync.Mutex
	dir     string
	m       map[string]*autoPilot
	cancels map[string]context.CancelFunc
	pass    map[string]string // admin passwords, in memory only
}

var apIDRe = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[a-zA-Z0-9]{4}$`)

// openAP loads saved pipelines; one that was running when the panel stopped is marked failed.
func openAP(dir string) (*apStore, error) {
	st := &apStore{dir: dir, m: map[string]*autoPilot{}, cancels: map[string]context.CancelFunc{}, pass: map[string]string{}}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var p autoPilot
		if json.Unmarshal(b, &p) != nil || !apIDRe.MatchString(p.ID) {
			continue
		}
		if !p.Done() {
			now := time.Now()
			end := now
			if fi, err := os.Stat(f); err == nil {
				end = fi.ModTime() // the last moment the pipeline was seen working
			}
			for i := range p.Stages {
				if p.Stages[i].State == apRunning {
					p.Stages[i].State, p.Stages[i].Error = apFailed, "قطع شد (interrupted): پنل وسط این مرحله دوباره راه‌اندازی شد"
					p.Stages[i].Ended = end
					p.Stages[i].Seconds = int64(end.Sub(p.Stages[i].Started).Seconds())
				}
			}
			p.Status, p.Error = apFailed, "قطع شد (interrupted): پنل وسط کار دوباره راه‌اندازی شد. با «ادامه از همین مرحله» دوباره شروع کنید."
			p.Ended = end
			p.TotalSeconds = p.PrevSeconds + int64(end.Sub(p.Started).Seconds())
			st.m[p.ID] = &p
			st.save(&p)
			continue
		}
		st.m[p.ID] = &p
	}
	return st, nil
}

// save writes a pipeline; the caller holds mu or owns p.
func (st *apStore) save(p *autoPilot) {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return
	}
	f := filepath.Join(st.dir, p.ID+".json")
	_ = os.WriteFile(f+".tmp", b, 0o600)
	_ = os.Rename(f+".tmp", f)
}

func (st *apStore) add(p *autoPilot) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.m[p.ID] = p
	st.save(p)
}

func (st *apStore) update(id string, fn func(*autoPilot)) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if p, ok := st.m[id]; ok {
		fn(p)
		st.save(p)
	}
}

func copyAP(p *autoPilot) autoPilot {
	cp := *p
	cp.Stages = append([]apStage(nil), p.Stages...)
	for i := range cp.Stages {
		cp.Stages[i].Warnings = append([]string(nil), p.Stages[i].Warnings...)
	}
	cp.Log = append([]apLogLine(nil), p.Log...)
	return cp
}

func (st *apStore) get(id string) (autoPilot, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	p, ok := st.m[id]
	if !ok {
		return autoPilot{}, false
	}
	return copyAP(p), true
}

func (st *apStore) list() []autoPilot {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]autoPilot, 0, len(st.m))
	for _, p := range st.m {
		cp := copyAP(p)
		cp.Log = nil
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

func (st *apStore) password(id string) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.pass[id]
}

func (st *apStore) setPassword(id, pass string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if pass == "" {
		delete(st.pass, id)
	} else {
		st.pass[id] = pass
	}
}

func (st *apStore) logf(id, level, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	st.update(id, func(p *autoPilot) {
		p.Log = append(p.Log, apLogLine{T: time.Now(), Level: level, Msg: msg})
		if len(p.Log) > 300 {
			p.Log = p.Log[len(p.Log)-300:]
		}
	})
}

// ---- pipeline ----

// apRun runs the stages from index `from` on; it is the pipeline's goroutine. The final status,
// end time and total are set in one update at the very end, so a pipeline seen as done is
// completely saved.
func (s *Server) apRun(ctx context.Context, id string, from int) {
	st := s.ap
	final, finalErr := apSuccess, ""
	defer func() {
		if r := recover(); r != nil {
			final, finalErr = apFailed, fmt.Sprint("internal error: ", r)
		}
		st.mu.Lock()
		if c := st.cancels[id]; c != nil {
			c()
		}
		delete(st.cancels, id)
		if final == apSuccess {
			delete(st.pass, id) // nothing left that needs it
		}
		st.mu.Unlock()
		st.update(id, func(p *autoPilot) {
			p.Status, p.Error = final, finalErr
			p.Ended = time.Now()
			p.TotalSeconds = p.PrevSeconds + int64(p.Ended.Sub(p.Started).Seconds())
		})
	}()
	p, _ := st.get(id)
	for i := from; i < len(p.Stages); i++ {
		key := p.Stages[i].Key
		if ctx.Err() != nil {
			final, finalErr = s.apCancelled(id, i)
			return
		}
		st.update(id, func(p *autoPilot) {
			p.Stages[i].State, p.Stages[i].Error, p.Stages[i].Started, p.Stages[i].Ended = apRunning, "", time.Now(), time.Time{}
		})
		st.logf(id, "info", "▶ %s", p.Stages[i].Title)
		var err error
		var warnings []string
		switch key {
		case "customize":
			err = s.apCustomize(ctx, id, i)
		case "wpinstall":
			err = s.apInstall(ctx, id, i)
		case "theme":
			err = s.apTheme(ctx, id, i)
		case "verify":
			warnings, err = s.apVerify(ctx, id, i)
		}
		cancelled := err != nil && ctx.Err() != nil
		st.update(id, func(p *autoPilot) {
			sg := &p.Stages[i]
			sg.Ended = time.Now()
			sg.Seconds = int64(sg.Ended.Sub(sg.Started).Seconds())
			sg.Warnings = warnings
			switch {
			case cancelled:
				sg.State, sg.Error = apFailed, "متوقف شد"
			case err != nil:
				sg.State, sg.Error = apFailed, err.Error()
			default:
				sg.State = apSuccess
			}
		})
		if cancelled {
			final, finalErr = s.apCancelled(id, i+1)
			return
		}
		if err != nil {
			st.logf(id, "error", "✘ %s: %v", p.Stages[i].Title, err)
			final, finalErr = apFailed, p.Stages[i].Title+": "+err.Error()
			return
		}
		for _, w := range warnings {
			st.logf(id, "warn", "%s", w)
		}
		st.logf(id, "info", "✔ %s", p.Stages[i].Title)
	}
	st.logf(id, "info", "✔ همه‌ی مرحله‌ها انجام شد")
}

// apCancelled marks the stages from i on skipped and returns the pipeline's final state.
func (s *Server) apCancelled(id string, i int) (string, string) {
	s.ap.update(id, func(p *autoPilot) {
		for j := i; j < len(p.Stages); j++ {
			if p.Stages[j].State == apWaiting {
				p.Stages[j].State = apSkipped
			}
		}
	})
	s.ap.logf(id, "warn", "متوقف شد")
	return "cancelled", "با درخواست شما متوقف شد. با «ادامه از همین مرحله» از همان‌جا ادامه می‌دهید."
}

// apCustomize creates the client texts with the AI: up to len(apRetryWaits)+1 attempts, each
// asking only for the texts still missing. Nothing on the host changes in this stage.
func (s *Server) apCustomize(ctx context.Context, id string, idx int) error {
	p, _ := s.ap.get(id)
	d, err := engine.Drafts().Get(p.DraftID)
	if err != nil {
		return err
	}
	t, err := engine.Catalog().Get(d.Theme)
	if err != nil || !t.HasDemo() {
		return fmt.Errorf("قالب %s دیگر در کاتالوگ نیست یا دمو ندارد", d.Theme)
	}
	demo, err := customize.LoadDemo(t.Path(t.DemoZip))
	if err != nil {
		return err
	}
	if prov, ok := s.ai.Get(p.In.Provider); !ok || prov.Model == "" {
		// a missing provider or model does not fix itself: no retries
		return errors.New("هوش مصنوعی وصل نیست یا مدلش انتخاب نشده؛ در منوی «هوش مصنوعی» درستش کنید و بعد «ادامه از همین مرحله» را بزنید")
	}
	total := len(apRetryWaits) + 1
	var last error
	for attempt := 1; attempt <= total; attempt++ {
		if attempt > 1 {
			wait := apRetryWaits[attempt-2]
			s.ap.logf(id, "warn", "تلاش %d از %d برای شخصی‌سازی، %d ثانیه‌ی دیگر…", attempt, total, int(wait.Seconds()))
			s.ap.update(id, func(p *autoPilot) {
				p.Stages[idx].Note = fmt.Sprintf("تلاش %d ناموفق بود؛ صبر %d ثانیه و تلاش %d از %d", attempt-1, int(wait.Seconds()), attempt, total)
			})
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
		s.ap.update(id, func(p *autoPilot) {
			p.Stages[idx].Attempts = attempt
			p.Stages[idx].Note = fmt.Sprintf("تلاش %d از %d", attempt, total)
		})
		last = s.apGenerateOnce(ctx, id, demo)
		if last == nil {
			n := 0
			if d, err := engine.Drafts().Get(p.DraftID); err == nil {
				n = len(d.Values)
			}
			s.ap.update(id, func(p *autoPilot) {
				p.Stages[idx].Note = fmt.Sprintf("%d متن آماده شد (تلاش %d از %d)", n, attempt, total)
			})
			return nil
		}
		s.ap.logf(id, "warn", "تلاش %d از %d ناموفق: %v", attempt, total, last)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	s.ap.update(id, func(p *autoPilot) { p.Stages[idx].Note = "" })
	return fmt.Errorf("شخصی‌سازی با هوش مصنوعی پس از %d تلاش انجام نشد (%v). هیچ چیزی روی هاست تغییر نکرد. پیش‌نویس را باز کنید، متن‌ها را دستی بسازید یا اصلاح کنید و بعد «ادامه از همین مرحله» را بزنید", total, last)
}

// apGenerateOnce is one AI attempt: fill the brief from the free description when its main
// fields are empty, then write the texts that are still missing.
func (s *Server) apGenerateOnce(ctx context.Context, id string, demo *customize.Demo) error {
	p, _ := s.ap.get(id)
	prov, client, err := s.readyAIProvider(ctx, p.In.Provider)
	if err != nil {
		return err
	}
	st := engine.Drafts()
	d, err := st.Get(p.DraftID)
	if err != nil {
		return err
	}
	brief := d.Brief
	core := func(b customize.Brief) bool { return strings.TrimSpace(b.Business+b.About+b.Services) != "" }
	if !core(brief) && strings.TrimSpace(brief.Story) != "" {
		fctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		b, _, err := customize.FillBrief(fctx, client, brief, false)
		cancel()
		if err != nil {
			return fmt.Errorf("پر کردن بریف از توضیح مشتری: %w", err)
		}
		brief = b
		if _, err := st.Update(d.ID, func(d *customize.Draft) error {
			d.Brief = b
			if b.SiteName != "" {
				d.Title = b.SiteName
			}
			return nil
		}); err != nil {
			return err
		}
		s.ap.logf(id, "info", "بریف از روی توضیح مشتری پر شد")
	}
	if !core(brief) {
		return errors.New("بریف خالی است: حوزه‌ی کار، معرفی یا خدمات مشتری را بنویسید")
	}
	// ask only for the AI texts the draft does not have yet
	sub := *demo
	sub.Fields = nil
	missing := 0
	for _, f := range demo.Fields {
		if f.AI && strings.TrimSpace(d.Values[f.Path]) != "" {
			continue
		}
		if f.AI {
			missing++
		}
		sub.Fields = append(sub.Fields, f)
	}
	if missing > 0 {
		s.ap.logf(id, "info", "نوشتن %d متن با %s", missing, prov.Name)
	}
	markGenerating(d.ID, prov)
	gctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	err = s.generateDraft(gctx, d.ID, prov, client, &sub, brief, true)
	cancel()
	if err != nil {
		return err
	}
	d, err = st.Get(d.ID)
	if err != nil {
		return err
	}
	// the same guard theme install applies: never build a site from an empty customisation
	if len(d.Values) == 0 && len(d.Images) == 0 && d.Logo == "" {
		return errors.New("پیش‌نویس هیچ متن، تصویر یا لوگویی ندارد؛ سایت با متن‌های دمو ساخته می‌شد")
	}
	_, err = st.Update(d.ID, func(d *customize.Draft) error { d.Finished = true; return nil })
	return err
}

// apStartRun starts a workflow for the pipeline's project and waits for it to end.
func (s *Server) apStartRun(ctx context.Context, id string, idx int, wfID string, inputs map[string]string) error {
	p, _ := s.ap.get(id)
	wf, ok := engine.FindWorkflow(s.workflows(), wfID)
	if !ok {
		return fmt.Errorf("کار %s پیدا نشد", wfID)
	}
	ended := make(chan struct{})
	runID, err := s.startWorkflow(wf, p.In.HostID, p.ProjectID, inputs, func(string) { close(ended) })
	if err != nil {
		return err
	}
	s.ap.update(id, func(p *autoPilot) { p.Stages[idx].RunID = runID })
	s.ap.logf(id, "info", "اجرای %s شروع شد (%s)", wf.Title, runID)
	cancelled := false
	for done := false; !done; {
		select {
		case <-ended: // the run and its project update are saved
			done = true
		case <-ctx.Done():
			if !cancelled {
				s.runner.Cancel(runID) // the run rolls back what it created
				cancelled = true
			}
			select {
			case <-ended:
				done = true
			case <-time.After(apPoll):
			}
		}
	}
	run, ok := s.runner.Get(runID, 0)
	switch {
	case !ok:
		return errors.New("اجرا پیدا نشد")
	case run.Status == engine.StatusSuccess:
		return nil
	case cancelled:
		return ctx.Err()
	case run.Error != "":
		return errors.New(run.Error)
	}
	return fmt.Errorf("اجرا با وضعیت %s تمام شد", run.Status)
}

func (s *Server) apInstall(ctx context.Context, id string, idx int) error {
	p, _ := s.ap.get(id)
	pass := s.ap.password(id)
	if pass == "" {
		return errors.New("رمز مدیر وردپرس در حافظه‌ی پنل نیست (پنل دوباره راه‌اندازی شده). در فرم «ادامه از همین مرحله» دوباره واردش کنید")
	}
	in := p.In
	return s.apStartRun(ctx, id, idx, "wp-install", map[string]string{"mode": in.Mode, "domain": in.Domain, "name": in.Name,
		"title": in.Title, "locale": in.Locale, "admin_user": in.AdminUser, "admin_email": in.AdminEmail, "admin_pass": pass})
}

// apSite is where the pipeline's WordPress lives (domain, folder).
func apSite(in apInputs) (string, string) {
	return projectSite("wp-install", map[string]string{"mode": in.Mode, "domain": in.Domain, "name": in.Name})
}

func (s *Server) apTheme(ctx context.Context, id string, idx int) error {
	p, _ := s.ap.get(id)
	dom, folder := apSite(p.In)
	err := s.apStartRun(ctx, id, idx, "theme-install", map[string]string{"domain": dom, "folder": folder, "theme": p.In.Theme,
		"draft": p.DraftID, "activate": "true", "import_demo": "true"})
	if err == nil {
		s.ap.update(id, func(p *autoPilot) { p.SiteURL = projects.Project{Domain: strings.ToLower(dom), Folder: folder}.URL() })
	}
	return err
}

var (
	reTags   = regexp.MustCompile(`(?s)<script.*?</script>|<style.*?</style>|<[^>]*>`)
	reSpaces = regexp.MustCompile(`\s+`)
)

// plainText strips tags and entities and collapses white space.
func plainText(s string) string {
	s = html.UnescapeString(reTags.ReplaceAllString(s, " "))
	return strings.TrimSpace(reSpaces.ReplaceAllString(s, " "))
}

// apVerify fetches the live home page. Only a site that does not answer 200 fails the stage;
// the rest (another theme on the page, demo texts left) are warnings.
func (s *Server) apVerify(ctx context.Context, id string, idx int) ([]string, error) {
	p, _ := s.ap.get(id)
	dom, folder := apSite(p.In)
	site := &wp.Site{Host: strings.ToLower(dom), Path: "/"}
	if folder != "" {
		site.Path = "/" + folder + "/"
	}
	// connect the way the theme install did (its locate step knows the address before DNS does)
	if run, ok := s.runner.Get(p.Stages[idx-1].RunID, 0); ok {
		for _, stp := range run.Steps {
			if stp.ID == "locate" && stp.Outputs["host"] != "" {
				site.Host, site.Path, site.Addr = stp.Outputs["host"], stp.Outputs["path"], stp.Outputs["site_addr"]
			}
		}
	}
	if err := site.Detect(ctx); err != nil {
		return nil, fmt.Errorf("سایت در دسترس نیست: %v", err)
	}
	var code int
	var body string
	var err error
	for attempt := 0; ; attempt++ {
		code, body, err = site.Get(ctx, "/")
		if err == nil && code == http.StatusOK {
			break
		}
		retry := err != nil || code == http.StatusServiceUnavailable || code >= 500
		if !retry || attempt >= len(apVerifyWaits) {
			break
		}
		why := fmt.Sprintf("HTTP %d", code)
		if err != nil {
			why = err.Error()
		}
		s.ap.logf(id, "warn", "صفحه‌ی اصلی جواب نداد (%s)؛ %d ثانیه صبر و تلاش دوباره", why, int(apVerifyWaits[attempt].Seconds()))
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(apVerifyWaits[attempt]):
		}
	}
	if err != nil {
		return nil, fmt.Errorf("صفحه‌ی اصلی سایت باز نشد: %v", err)
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("صفحه‌ی اصلی سایت جواب درستی نداد (HTTP %d)", code)
	}
	var warns []string
	notes := []string{"HTTP 200"}
	if strings.Contains(body, "/themes/"+p.In.Theme+"/") {
		notes = append(notes, "قالب «"+p.In.Theme+"» فعال است")
	} else {
		warns = append(warns, "نشانی قالب «"+p.In.Theme+"» (/themes/"+p.In.Theme+"/) در صفحه‌ی اصلی دیده نشد؛ شاید قالب دیگری فعال است یا افزونه‌ی کش نسخه‌ی قدیمی را نشان می‌دهد")
	}
	if left, checked := demoLeftovers(p.DraftID, body); checked > 0 {
		if len(left) > 0 {
			ex := left
			if len(ex) > 3 {
				ex = ex[:3]
			}
			warns = append(warns, fmt.Sprintf("%d متن دموی اصلی هنوز روی صفحه‌ی اصلی دیده می‌شود، مثلاً: «%s»", len(left), strings.Join(ex, "»، «")))
		} else {
			notes = append(notes, fmt.Sprintf("هیچ‌کدام از %d متن جایگزین‌شده‌ی دمو دیده نشد", checked))
		}
	}
	s.ap.update(id, func(p *autoPilot) { p.Stages[idx].Note = strings.Join(notes, " · ") })
	return warns, nil
}

// demoLeftovers returns the demo texts the draft replaced that still appear on the page, and
// how many texts it could check (short texts are skipped: they match by chance).
func demoLeftovers(draftID, body string) ([]string, int) {
	d, err := engine.Drafts().Get(draftID)
	if err != nil {
		return nil, 0
	}
	t, err := engine.Catalog().Get(d.Theme)
	if err != nil || !t.HasDemo() {
		return nil, 0
	}
	demo, err := customize.LoadDemo(t.Path(t.DemoZip))
	if err != nil {
		return nil, 0
	}
	page := plainText(body)
	var left []string
	checked := 0
	for _, f := range demo.Fields {
		v, ok := d.Values[f.Path]
		if !ok || !f.AI {
			continue
		}
		orig := plainText(f.Original)
		if len([]rune(orig)) < 12 || plainText(v) == orig {
			continue
		}
		checked++
		if strings.Contains(page, orig) {
			r := []rune(orig)
			if len(r) > 60 {
				orig = string(r[:60]) + "…"
			}
			left = append(left, orig)
		}
	}
	return left, checked
}

// ---- handlers ----

// apForm is the AutoPilot form's values (re-shown after an error; never the password).
type apForm struct {
	Host, Domain, Mode, Name, Title, Locale, AdminUser, AdminEmail, Theme, Provider string
	Brief                                                                           customize.Brief
}

func (s *Server) handleAutopilot(w http.ResponseWriter, r *http.Request) {
	f := apForm{Mode: "subdomain", Locale: "fa_IR"}
	if pr, ok := s.projects.Active(); ok {
		f.Host, f.Domain, f.Brief.SiteName = pr.HostID, pr.Domain, pr.Name
		if pr.Folder != "" {
			f.Mode, f.Name = "subfolder", pr.Folder
		}
	}
	if p, ok := s.ai.Get(""); ok {
		f.Provider = p.ID
	}
	s.renderAutopilot(w, r, f, r.URL.Query().Get("err"))
}

func (s *Server) renderAutopilot(w http.ResponseWriter, r *http.Request, f apForm, errMsg string) {
	hosts := s.store.List()
	if f.Host == "" && len(hosts) > 0 {
		f.Host = hosts[0].ID
	}
	var domains []string
	for _, h := range hosts {
		if h.ID == f.Host && h.LastReport != nil {
			domains = h.LastReport.Domains
		}
	}
	if f.Domain == "" && len(domains) > 0 {
		f.Domain = domains[0]
	}
	var themes []*catalog.Theme
	for _, t := range engine.Catalog().List() {
		if t.HasDemo() {
			themes = append(themes, t)
		}
	}
	s.render(w, "autopilot.html", s.page(r, map[string]any{
		"F": f, "Hosts": hosts, "Domains": domains, "Themes": themes, "Providers": s.ai.List(),
		"Socials": customize.Socials, "Pipelines": s.ap.list(), "Error": errMsg,
	}))
}

func (s *Server) handleAutopilotStart(w http.ResponseWriter, r *http.Request) {
	v := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	f := apForm{Host: v("host"), Domain: strings.ToLower(v("domain")), Mode: v("mode"), Name: strings.ToLower(v("name")),
		Title: v("title"), Locale: v("locale"), AdminUser: v("admin_user"), AdminEmail: v("admin_email"),
		Theme: v("theme"), Provider: v("provider")}
	var tmp customize.Draft
	s.briefFromForm(r, &tmp)
	f.Brief = tmp.Brief
	pass := r.FormValue("admin_pass")
	fail := func(msg string) { s.renderAutopilot(w, r, f, msg) }

	h, ok := s.store.Get(f.Host)
	if !ok {
		fail("یک هاست انتخاب کنید")
		return
	}
	if f.Title == "" {
		f.Title = f.Brief.SiteName
	}
	inputs := map[string]string{"mode": f.Mode, "domain": f.Domain, "name": f.Name, "title": f.Title, "locale": f.Locale,
		"admin_user": f.AdminUser, "admin_email": f.AdminEmail, "admin_pass": pass}
	wf, ok := engine.FindWorkflow(s.workflows(), "wp-install")
	if !ok {
		fail("کار نصب وردپرس پیدا نشد")
		return
	}
	if err := engine.ValidateInputs(wf, inputs); err != nil {
		fail(err.Error())
		return
	}
	f.Mode, f.Locale = inputs["mode"], inputs["locale"]
	if len(pass) < 12 {
		fail("رمز مدیر باید حداقل ۱۲ کاراکتر باشد")
		return
	}
	t, err := engine.Catalog().Get(f.Theme)
	if err != nil || !t.HasDemo() {
		fail("قالبی با دمو انتخاب کنید (شخصی‌سازی روی دموی قالب انجام می‌شود)")
		return
	}
	prov, ok := s.ai.Get(f.Provider)
	if !ok {
		fail("هوش مصنوعی وصل نیست. اول در منوی «هوش مصنوعی» یک سرویس اضافه و تست کنید.")
		return
	}
	if prov.Model == "" {
		fail("برای «" + prov.Name + "» هنوز مدلی انتخاب نشده؛ اول در منوی «هوش مصنوعی» مدل را انتخاب کنید.")
		return
	}
	f.Provider = prov.ID
	if f.Brief.SiteName == "" {
		fail("نام سایت یا مشتری را بنویسید")
		return
	}
	b := f.Brief
	if strings.TrimSpace(b.Story+b.Business+b.About+b.Services) == "" {
		fail("درباره‌ی مشتری بنویسید (توضیح آزاد) یا حداقل حوزه‌ی کار و معرفی را پر کنید")
		return
	}
	var logo []byte
	if r.MultipartForm != nil {
		if fhs := r.MultipartForm.File["logo"]; len(fhs) > 0 && fhs[0].Size > 0 {
			fh, err := fhs[0].Open()
			if err == nil {
				logo, err = io.ReadAll(io.LimitReader(fh, customize.MaxImage+1))
				fh.Close()
			}
			if err != nil {
				fail("لوگو خوانده نشد: " + err.Error())
				return
			}
		}
	}

	// the draft the AI will fill
	d, err := engine.Drafts().New(t.Slug, b.SiteName)
	if err != nil {
		fail(err.Error())
		return
	}
	_, err = engine.Drafts().Update(d.ID, func(d *customize.Draft) error {
		s.briefFromForm(r, d)
		if d.Brief.Language == "" && f.Locale == "en_US" {
			d.Brief.Language = "English"
		}
		if logo != nil {
			name, err := d.SaveImage("logo", logo)
			if err != nil {
				return fmt.Errorf("لوگو: %w", err)
			}
			d.Logo = name
		}
		return nil
	})
	if err != nil {
		_ = engine.Drafts().Delete(d.ID)
		fail(err.Error())
		return
	}

	// the project: the active one when it is this site, else a new one
	dom, folder := projectSite("wp-install", inputs)
	dom = strings.ToLower(dom)
	proj, ok := s.projects.Active()
	if !ok || !((proj.Domain == dom && proj.Folder == folder) || (proj.Domain == "" && proj.Name == b.SiteName)) {
		proj, err = s.projects.Add(projects.Project{Name: b.SiteName, HostID: h.ID, Domain: dom, Folder: folder, DraftID: d.ID})
		if err != nil {
			fail(err.Error())
			return
		}
	} else {
		_ = s.projects.Update(proj.ID, func(x *projects.Project) {
			x.HostID, x.Domain, x.Folder, x.DraftID = h.ID, dom, folder, d.ID
		})
	}

	now := time.Now()
	p := &autoPilot{ID: now.Format("20060102-150405") + "-" + config.RandString(4), Title: b.SiteName,
		ProjectID: proj.ID, DraftID: d.ID, Status: apRunning, Stages: newStages(), Created: now, Started: now,
		In: apInputs{HostID: h.ID, HostName: h.Name, Domain: f.Domain, Mode: f.Mode, Name: inputs["name"], Title: inputs["title"],
			Locale: f.Locale, AdminUser: f.AdminUser, AdminEmail: f.AdminEmail, Theme: t.Slug, Provider: prov.ID}}
	s.ap.add(p)
	s.ap.setPassword(p.ID, pass)
	s.ap.logf(p.ID, "info", "AutoPilot برای %s شروع شد (هاست %s، قالب %s)", projects.Project{Domain: dom, Folder: folder}.Site(), h.Name, t.Slug)
	s.apGo(p.ID, 0)
	http.Redirect(w, r, s.cfg.BasePath+"autopilot/"+p.ID, http.StatusSeeOther)
}

// apGo starts the pipeline goroutine from stage `from`.
func (s *Server) apGo(id string, from int) {
	ctx, cancel := context.WithCancel(context.Background())
	s.ap.mu.Lock()
	s.ap.cancels[id] = cancel
	s.ap.mu.Unlock()
	go s.apRun(ctx, id, from)
}

func (s *Server) handleAutopilotView(w http.ResponseWriter, r *http.Request) {
	p, ok := s.ap.get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.render(w, "autopilot_run.html", s.page(r, map[string]any{
		"P": p, "Crumb": p.Title, "HasPass": s.ap.password(p.ID) != "", "NowUnix": time.Now().Unix(),
		"NeedPass": s.apNeedsPass(p),
	}))
}

// apNeedsPass reports whether resuming would install WordPress without a password in memory.
func (s *Server) apNeedsPass(p autoPilot) bool {
	if !p.Resumable() || s.ap.password(p.ID) != "" {
		return false
	}
	for _, st := range p.Stages {
		if st.Key == "wpinstall" {
			return st.State != apSuccess
		}
	}
	return false
}

func (s *Server) handleAutopilotJSON(w http.ResponseWriter, r *http.Request) {
	p, ok := s.ap.get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	type stageJSON struct {
		Key, Title, State, Error, Note, RunID string
		Warnings                              []string
		Attempts                              int
		Started                               int64 // unix, 0 when not started
		Seconds                               int64
	}
	var stages []stageJSON
	for _, st := range p.Stages {
		sj := stageJSON{Key: st.Key, Title: st.Title, State: st.State, Error: st.Error, Note: st.Note, RunID: st.RunID,
			Warnings: st.Warnings, Attempts: st.Attempts, Seconds: st.Secs()}
		if !st.Started.IsZero() {
			sj.Started = st.Started.Unix()
		}
		stages = append(stages, sj)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": p.Status, "error": p.Error, "done": p.Done(), "stages": stages, "site_url": p.SiteURL,
		"started": p.Started.Unix(), "prev_seconds": p.PrevSeconds, "total_seconds": p.TotalSeconds,
		"elapsed": p.Elapsed(), "now": time.Now().Unix(), "log": p.Log,
	})
}

func (s *Server) handleAutopilotOp(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, ok := s.ap.get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	back := s.cfg.BasePath + "autopilot/" + id
	switch r.PathValue("op") {
	case "cancel":
		s.ap.mu.Lock()
		c := s.ap.cancels[id]
		s.ap.mu.Unlock()
		if c != nil {
			s.ap.logf(id, "warn", "درخواست توقف ثبت شد")
			c()
		}
		http.Redirect(w, r, back, http.StatusSeeOther)
	case "resume":
		if !p.Resumable() {
			http.Redirect(w, r, back+"?err="+url.QueryEscape("این AutoPilot در حال اجرا یا تمام‌شده است"), http.StatusSeeOther)
			return
		}
		if pass := r.FormValue("admin_pass"); pass != "" {
			if len(pass) < 12 {
				http.Redirect(w, r, back+"?err="+url.QueryEscape("رمز مدیر باید حداقل ۱۲ کاراکتر باشد"), http.StatusSeeOther)
				return
			}
			s.ap.setPassword(id, pass)
		}
		if s.apNeedsPass(p) {
			http.Redirect(w, r, back+"?err="+url.QueryEscape("رمز مدیر وردپرس را وارد کنید"), http.StatusSeeOther)
			return
		}
		from := len(p.Stages)
		for i, st := range p.Stages {
			if st.State != apSuccess {
				from = i
				break
			}
		}
		if from == len(p.Stages) {
			http.Redirect(w, r, back, http.StatusSeeOther)
			return
		}
		s.ap.update(id, func(p *autoPilot) {
			for i := from; i < len(p.Stages); i++ {
				p.Stages[i] = apStage{Key: p.Stages[i].Key, Title: p.Stages[i].Title, State: apWaiting}
			}
			p.PrevSeconds = p.TotalSeconds
			p.TotalSeconds, p.Started, p.Ended = 0, time.Now(), time.Time{}
			p.Status, p.Error = apRunning, ""
		})
		s.ap.logf(id, "info", "ادامه از مرحله‌ی «%s»", p.Stages[from].Title)
		s.apGo(id, from)
		http.Redirect(w, r, back, http.StatusSeeOther)
	default:
		http.NotFound(w, r)
	}
}
