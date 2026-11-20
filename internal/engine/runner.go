package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/traptiterbeats/wpauto/internal/config"
	"github.com/traptiterbeats/wpauto/internal/cpanel"
	"github.com/traptiterbeats/wpauto/internal/profile"
)

// Run states.
const (
	StatusPending     = "pending"
	StatusRunning     = "running"
	StatusSuccess     = "success"
	StatusFailed      = "failed"
	StatusSkipped     = "skipped"
	StatusCancelled   = "cancelled"
	StatusInterrupted = "interrupted"
)

// LogLine is one line of a run log.
type LogLine struct {
	N     int       `json:"n"`
	T     time.Time `json:"t"`
	Level string    `json:"level"`
	Step  string    `json:"step,omitempty"`
	Msg   string    `json:"msg"`
}

// StepState is the progress of one step.
type StepState struct {
	ID      string            `json:"id"`
	Title   string            `json:"title"`
	Status  string            `json:"status"`
	Error   string            `json:"error,omitempty"`
	Outputs map[string]string `json:"outputs,omitempty"`
}

// Run is one execution of a workflow on a host.
type Run struct {
	ID         string            `json:"id"`
	WorkflowID string            `json:"workflow_id"`
	Title      string            `json:"title"`
	HostID     string            `json:"host_id"`
	HostName   string            `json:"host_name"`
	ProjectID  string            `json:"project_id,omitempty"`
	Inputs     map[string]string `json:"inputs"` // passwords masked
	Status     string            `json:"status"`
	Error      string            `json:"error,omitempty"`
	StartedAt  time.Time         `json:"started_at"`
	EndedAt    time.Time         `json:"ended_at,omitempty"`
	Steps      []StepState       `json:"steps"`
	Log        []LogLine         `json:"log"`
}

// Done reports whether the run has finished.
func (r Run) Done() bool {
	return r.Status != StatusRunning && r.Status != StatusPending
}

// Target is everything a run needs to reach a host.
type Target struct {
	HostID   string
	HostName string
	Profile  *profile.Profile
	Options  cpanel.Options
	// OnLoginRejected is called once when cPanel rejects the credentials (to lock the host).
	OnLoginRejected func()
	// OnReport stores a fresh preflight report on the host.
	OnReport func(*cpanel.Report)
	// OnDomain is called after a successful run that created a (sub)domain, so forms list it.
	OnDomain func(string)
	// ProjectID ties the run to a client project; OnDone is called with the final status.
	ProjectID string
	OnDone    func(status string)
}

// RunContext is handed to each step.
type RunContext struct {
	newDomain string // a (sub)domain this run created
	Profile   *profile.Profile
	Inputs    map[string]string
	OnReport  func(*cpanel.Report)

	// Vars holds in-memory values shared between steps that must never be persisted (passwords).
	Vars map[string]string

	target  *Target
	run     *Run
	r       *Runner
	step    string
	client  *cpanel.Client
	undo    []action
	finally []action
	siteObj any
	wpInfo  map[string]any
}

type action struct {
	desc string
	fn   func(context.Context) error
}

// Undo registers a rollback for something this run created. Undo actions run in reverse order
// only when the run fails and the workflow has rollback: true.
func (rc *RunContext) Undo(desc string, fn func(context.Context) error) {
	rc.undo = append(rc.undo, action{desc, fn})
}

// Finally registers cleanup that always runs at the end (e.g. removing the helper script).
func (rc *RunContext) Finally(desc string, fn func(context.Context) error) {
	rc.finally = append(rc.finally, action{desc, fn})
}

// Warnf appends a warning line.
func (rc *RunContext) Warnf(format string, a ...any) {
	rc.r.log(rc.run, "warn", rc.step, fmt.Sprintf(format, a...))
}

// Logf appends an info line to the run log.
func (rc *RunContext) Logf(format string, a ...any) {
	rc.r.log(rc.run, "info", rc.step, fmt.Sprintf(format, a...))
}

// CPanel returns a signed-in client, logging in once per run.
func (rc *RunContext) CPanel(ctx context.Context) (*cpanel.Client, error) {
	if rc.client != nil {
		return rc.client, nil
	}
	c, err := cpanel.New(rc.target.Options)
	if err != nil {
		return nil, err
	}
	if err := c.Login(ctx); err != nil {
		if errors.Is(err, cpanel.ErrInvalidLogin) && rc.target.OnLoginRejected != nil {
			rc.target.OnLoginRejected()
		}
		return nil, err
	}
	rc.client = c
	return c, nil
}

// Runner starts runs and keeps their state.
type Runner struct {
	mu      sync.Mutex
	dir     string
	runs    map[string]*Run
	cancels map[string]context.CancelFunc
	busy    map[string]string // hostID -> runID: one run per host at a time
}

// NewRunner loads past runs from dir (<config>/runs).
func NewRunner(dir string) (*Runner, error) {
	r := &Runner{dir: dir, runs: map[string]*Run{}, cancels: map[string]context.CancelFunc{}, busy: map[string]string{}}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var run Run
		if json.Unmarshal(b, &run) != nil {
			continue
		}
		if !run.Done() {
			run.Status = StatusInterrupted
			run.Error = "panel restarted while this run was in progress"
		}
		r.runs[run.ID] = &run
	}
	return r, nil
}

// Start validates inputs and begins the workflow in the background.
func (r *Runner) Start(wf *Workflow, t *Target, inputs map[string]string) (string, error) {
	for _, in := range wf.Inputs {
		v := strings.TrimSpace(inputs[in.Name])
		if v == "" && in.Default != "" {
			v = in.Default
		}
		if in.Required && v == "" {
			return "", fmt.Errorf("«%s» لازم است", in.Label)
		}
		if in.Type == "select" && v != "" && !contains(in.Options, v) {
			return "", fmt.Errorf("مقدار «%s» نامعتبر است", in.Label)
		}
		if in.Pattern != "" && v != "" {
			if re, err := regexp.Compile("^(?:" + in.Pattern + ")$"); err == nil && !re.MatchString(v) {
				return "", fmt.Errorf("«%s» قالب درستی ندارد", in.Label)
			}
		}
		inputs[in.Name] = v
	}
	r.mu.Lock()
	if id, ok := r.busy[t.HostID]; ok {
		r.mu.Unlock()
		return "", fmt.Errorf("یک کار دیگر (%s) روی این هاست در حال اجراست", id)
	}
	masked := map[string]string{}
	for _, in := range wf.Inputs {
		if in.Type == "password" && inputs[in.Name] != "" {
			masked[in.Name] = "••••••"
		} else {
			masked[in.Name] = inputs[in.Name]
		}
	}
	run := &Run{ID: time.Now().Format("20060102-150405") + "-" + config.RandString(4), WorkflowID: wf.ID, Title: wf.Title,
		HostID: t.HostID, HostName: t.HostName, ProjectID: t.ProjectID, Inputs: masked, Status: StatusRunning, StartedAt: time.Now()}
	for _, s := range wf.Steps {
		run.Steps = append(run.Steps, StepState{ID: s.ID, Title: s.Title, Status: StatusPending})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	r.runs[run.ID] = run
	r.cancels[run.ID] = cancel
	r.busy[t.HostID] = run.ID
	r.mu.Unlock()
	r.persist(run)
	go r.execute(ctx, wf, t, inputs, run)
	return run.ID, nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (r *Runner) execute(ctx context.Context, wf *Workflow, t *Target, inputs map[string]string, run *Run) {
	rc := &RunContext{Profile: t.Profile, Inputs: inputs, OnReport: t.OnReport, Vars: map[string]string{}, target: t, run: run, r: r}
	outputs := map[string]map[string]string{}
	data := map[string]any{"Inputs": inputs, "Steps": outputs, "Host": map[string]string{"Name": t.HostName, "ID": t.HostID}}
	final, finalErr := StatusSuccess, ""
	defer func() {
		if p := recover(); p != nil {
			final, finalErr = StatusFailed, fmt.Sprint("internal error: ", p)
		}
		// Rollback and cleanup use a fresh context so they still run after a cancel or timeout.
		// Rollback runs first: undoing changes inside WordPress still needs the helper.
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Minute)
		if final != StatusSuccess && wf.Rollback && len(rc.undo) > 0 {
			rc.step = "rollback"
			r.log(run, "warn", "rollback", "برگرداندن تغییرات این اجرا…")
			for i := len(rc.undo) - 1; i >= 0; i-- {
				if err := rc.undo[i].fn(cctx); err != nil {
					r.log(run, "error", "rollback", rc.undo[i].desc+": "+err.Error())
				} else {
					r.log(run, "info", "rollback", "✔ "+rc.undo[i].desc)
				}
			}
		}
		rc.step = "cleanup"
		for i := len(rc.finally) - 1; i >= 0; i-- {
			if err := rc.finally[i].fn(cctx); err != nil {
				r.log(run, "warn", "cleanup", rc.finally[i].desc+": "+err.Error())
			} else {
				r.log(run, "info", "cleanup", rc.finally[i].desc)
			}
		}
		if rc.client != nil {
			rc.client.Logout(cctx)
		}
		ccancel()
		if final == StatusSuccess && rc.newDomain != "" && t.OnDomain != nil {
			t.OnDomain(rc.newDomain)
		}
		r.mu.Lock()
		run.Status, run.Error, run.EndedAt = final, finalErr, time.Now()
		delete(r.busy, t.HostID)
		if c := r.cancels[run.ID]; c != nil {
			c()
		}
		delete(r.cancels, run.ID)
		r.mu.Unlock()
		r.log(run, "info", "", "پایان: "+final)
		r.persist(run)
		if t.OnDone != nil {
			t.OnDone(final)
		}
	}()
	for i, def := range wf.Steps {
		if ctx.Err() != nil {
			final, finalErr = StatusCancelled, "cancelled"
			r.setStep(run, i, StatusSkipped, "", nil)
			continue
		}
		if def.If != "" {
			v, err := render(def.If, data)
			v = strings.TrimSpace(v)
			if err == nil && (v == "" || v == "false" || v == "0") {
				r.setStep(run, i, StatusSkipped, "", nil)
				continue
			}
		}
		args := map[string]string{}
		var argErr error
		for k, v := range def.With {
			if args[k], argErr = render(v, data); argErr != nil {
				break
			}
		}
		rc.step = def.ID
		r.setStep(run, i, StatusRunning, "", nil)
		r.log(run, "info", def.ID, "▶ "+def.Title)
		var out map[string]string
		err := argErr
		if err == nil {
			out, err = registry[def.Uses].Run(ctx, rc, args)
		}
		if err != nil {
			if ctx.Err() == context.Canceled {
				err = errors.New("cancelled")
			}
			r.log(run, "error", def.ID, err.Error())
			r.setStep(run, i, StatusFailed, err.Error(), out)
			if !def.ContinueOnError {
				final, finalErr = StatusFailed, def.Title+": "+err.Error()
				if ctx.Err() != nil {
					final = StatusCancelled
				}
				for j := i + 1; j < len(wf.Steps); j++ {
					r.setStep(run, j, StatusSkipped, "", nil)
				}
				return
			}
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		outputs[def.ID] = out
		r.setStep(run, i, StatusSuccess, "", out)
	}
}

func (r *Runner) setStep(run *Run, i int, status, errMsg string, out map[string]string) {
	r.mu.Lock()
	run.Steps[i].Status, run.Steps[i].Error = status, errMsg
	if out != nil {
		run.Steps[i].Outputs = out
	}
	r.mu.Unlock()
	r.persist(run)
}

func (r *Runner) log(run *Run, level, step, msg string) {
	r.mu.Lock()
	run.Log = append(run.Log, LogLine{N: len(run.Log) + 1, T: time.Now(), Level: level, Step: step, Msg: msg})
	r.mu.Unlock()
}

func (r *Runner) persist(run *Run) {
	r.mu.Lock()
	b, err := json.MarshalIndent(run, "", "  ")
	r.mu.Unlock()
	if err != nil {
		return
	}
	p := filepath.Join(r.dir, run.ID+".json")
	_ = os.WriteFile(p+".tmp", b, 0o600)
	_ = os.Rename(p+".tmp", p)
}

// Cancel stops a running run.
func (r *Runner) Cancel(id string) {
	r.mu.Lock()
	c, run := r.cancels[id], r.runs[id]
	r.mu.Unlock()
	if c != nil && run != nil {
		r.log(run, "warn", "", "درخواست توقف ثبت شد")
		c()
	}
}

// Get returns a snapshot of a run with log lines after `after`.
func (r *Runner) Get(id string, after int) (Run, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[id]
	if !ok {
		return Run{}, false
	}
	cp := *run
	cp.Steps = append([]StepState(nil), run.Steps...)
	if after < 0 {
		after = 0
	}
	if after > len(run.Log) {
		after = len(run.Log)
	}
	cp.Log = append([]LogLine(nil), run.Log[after:]...)
	return cp, true
}

// List returns runs newest first, without logs.
func (r *Runner) List(limit int) []Run {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Run, 0, len(r.runs))
	for _, run := range r.runs {
		cp := *run
		cp.Log = nil
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
