// Package engine runs workflows: ordered lists of steps declared in YAML. Steps are built-in Go
// step types (cpanel.login, cpanel.preflight, wp.detect, ...) so new tasks are added by writing a
// YAML file, and host differences stay in host profiles.
package engine

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

//go:embed workflows/*.yaml
var builtinFS embed.FS

// Input is one form field a workflow asks for before it runs.
type Input struct {
	Name     string   `yaml:"name"`
	Label    string   `yaml:"label"`
	Type     string   `yaml:"type"` // text | password | select | checkbox | domain | theme (catalogue)
	Required bool     `yaml:"required"`
	Default  string   `yaml:"default"`
	Options  []string `yaml:"options"`
	// Labels shows a friendly label for each option value.
	Labels map[string]string `yaml:"labels"`
	// Pattern is an optional regular expression the value must match.
	Pattern string `yaml:"pattern"`
	Help    string `yaml:"help"`
}

// StepDef is one step in a workflow.
type StepDef struct {
	ID              string            `yaml:"id"`
	Title           string            `yaml:"title"`
	Uses            string            `yaml:"uses"`
	With            map[string]string `yaml:"with"`
	If              string            `yaml:"if"` // template; step is skipped when it renders to "", "false" or "0"
	ContinueOnError bool              `yaml:"continue_on_error"`
}

// Workflow is a runnable task.
type Workflow struct {
	ID          string    `yaml:"id"`
	Title       string    `yaml:"title"`
	Description string    `yaml:"description"`
	Order       int       `yaml:"order"`
	Inputs      []Input   `yaml:"inputs"`
	Steps       []StepDef `yaml:"steps"`
	// Rollback undoes what the run created (database, subdomain, files) when a step fails.
	Rollback bool   `yaml:"rollback"`
	Source   string `yaml:"-"`
}

// Validate checks the workflow against the step registry.
func (w *Workflow) Validate() error {
	if w.ID == "" || strings.ContainsAny(w.ID, " /\\") {
		return fmt.Errorf("workflow id missing or invalid")
	}
	if len(w.Steps) == 0 {
		return fmt.Errorf("workflow %s has no steps", w.ID)
	}
	seen := map[string]bool{}
	for i, s := range w.Steps {
		if s.ID == "" {
			return fmt.Errorf("workflow %s: step %d has no id", w.ID, i+1)
		}
		if seen[s.ID] {
			return fmt.Errorf("workflow %s: duplicate step id %s", w.ID, s.ID)
		}
		seen[s.ID] = true
		if _, ok := registry[s.Uses]; !ok {
			return fmt.Errorf("workflow %s: step %s uses unknown step type %q", w.ID, s.ID, s.Uses)
		}
	}
	for _, in := range w.Inputs {
		switch in.Type {
		case "", "text", "password", "select", "checkbox", "domain", "theme", "draft":
		default:
			return fmt.Errorf("workflow %s: input %s has unknown type %q", w.ID, in.Name, in.Type)
		}
	}
	return nil
}

// ParseWorkflow reads one workflow from YAML.
func ParseWorkflow(b []byte, source string) (*Workflow, error) {
	var w Workflow
	if err := yaml.Unmarshal(b, &w); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	for i := range w.Inputs {
		if w.Inputs[i].Type == "" {
			w.Inputs[i].Type = "text"
		}
	}
	w.Source = source
	if err := w.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	return &w, nil
}

// LoadWorkflows returns built-in workflows overlaid with *.yaml files from dir.
func LoadWorkflows(dir string) ([]*Workflow, error) {
	byID := map[string]*Workflow{}
	entries, _ := builtinFS.ReadDir("workflows")
	for _, e := range entries {
		b, _ := builtinFS.ReadFile("workflows/" + e.Name())
		w, err := ParseWorkflow(b, "builtin/"+e.Name())
		if err != nil {
			return nil, err
		}
		byID[w.ID] = w
	}
	if dir != "" {
		files, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				return nil, err
			}
			w, err := ParseWorkflow(b, f)
			if err != nil {
				return nil, err
			}
			byID[w.ID] = w
		}
	}
	out := make([]*Workflow, 0, len(byID))
	for _, w := range byID {
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// FindWorkflow returns the workflow with id.
func FindWorkflow(list []*Workflow, id string) (*Workflow, bool) {
	for _, w := range list {
		if w.ID == id {
			return w, true
		}
	}
	return nil, false
}

// render expands {{.Inputs.x}}, {{.Steps.id.key}} and {{.Host.x}} in a step argument.
func render(s string, data map[string]any) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	t, err := template.New("").Option("missingkey=zero").Parse(s)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return strings.ReplaceAll(b.String(), "<no value>", ""), nil
}

// Step plan values for StepPlan.
const (
	PlanYes   = "yes"   // the step runs with these inputs
	PlanNo    = "no"    // the step is not needed with these inputs
	PlanMaybe = "maybe" // decided by an earlier step's result
)

// StepPlan says for each step whether it will run with the given inputs,
// before anything has run. Inputs missing from the map take their defaults.
func StepPlan(wf *Workflow, inputs map[string]string) []string {
	in := map[string]string{}
	for _, i := range wf.Inputs {
		in[i.Name] = i.Default
	}
	for k, v := range inputs {
		in[k] = v
	}
	data := map[string]any{"Inputs": in, "Steps": map[string]map[string]string{}, "Host": map[string]string{}}
	out := make([]string, len(wf.Steps))
	for i, def := range wf.Steps {
		out[i] = PlanYes
		if def.If == "" {
			continue
		}
		if strings.Contains(def.If, ".Steps") || strings.Contains(def.If, ".Host") {
			out[i] = PlanMaybe
			continue
		}
		v, err := render(def.If, data)
		v = strings.TrimSpace(v)
		if err == nil && (v == "" || v == "false" || v == "0") {
			out[i] = PlanNo
		}
	}
	return out
}
