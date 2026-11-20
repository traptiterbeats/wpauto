package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/traptiterbeats/wpauto/internal/cpanel"
)

// StepFunc runs one step. It returns outputs other steps can reference as {{.Steps.<id>.<key>}}.
type StepFunc func(ctx context.Context, rc *RunContext, args map[string]string) (map[string]string, error)

// StepInfo documents a step type.
type StepInfo struct {
	Name        string
	Description string
	Run         StepFunc
}

var registry = map[string]StepInfo{}

// Register adds a step type. Later phases register wp.install, theme.install, ai.customize, ...
func Register(name, description string, fn StepFunc) {
	registry[name] = StepInfo{Name: name, Description: description, Run: fn}
}

// StepTypes lists registered step types (for docs and the UI).
func StepTypes() []StepInfo {
	out := make([]StepInfo, 0, len(registry))
	for _, s := range registry {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func init() {
	Register("log", "writes a message to the run log (arg: message)", stepLog)
	Register("cpanel.login", "signs in to the host's cPanel", stepLogin)
	Register("cpanel.preflight", "read-only capability report; saved on the host", stepPreflight)
	Register("cpanel.uapi", "calls a read-only UAPI function (args: module, function, plus any other args)", stepUAPI)
	Register("wp.detect", "finds existing WordPress installs in domain document roots (arg: domain, empty = all)", stepWPDetect)
}

func stepLog(_ context.Context, rc *RunContext, args map[string]string) (map[string]string, error) {
	rc.Logf("%s", args["message"])
	return nil, nil
}

func stepLogin(ctx context.Context, rc *RunContext, _ map[string]string) (map[string]string, error) {
	c, err := rc.CPanel(ctx)
	if err != nil {
		return nil, err
	}
	rc.Logf("وارد %s شدیم", c.BaseURL())
	return map[string]string{"url": c.BaseURL()}, nil
}

func stepPreflight(ctx context.Context, rc *RunContext, _ map[string]string) (map[string]string, error) {
	if _, err := rc.CPanel(ctx); err != nil {
		return nil, err
	}
	rep := cpanel.Probe(ctx, rc.client, rc.Profile.Probes, rc.Profile.PreferredPHP)
	for _, c := range rep.Checks {
		mark := "✔"
		if !c.OK {
			mark = "✘"
		}
		rc.Logf("%s %s: %s", mark, c.Title, c.Detail)
	}
	if rc.OnReport != nil {
		rc.OnReport(rep)
	}
	if !rep.ReadyForWP {
		return nil, fmt.Errorf("%s", rep.Recommendation)
	}
	rc.Logf("%s", rep.Recommendation)
	return map[string]string{
		"main_domain":   rep.MainDomain,
		"preferred_php": rep.PreferredPHP,
		"db_prefix":     rep.DBPrefix,
		"ready":         "true",
	}, nil
}

// readOnlyUAPI is the allow-list for the generic cpanel.uapi step. Writing calls get their own
// step types with explicit safety checks.
var readOnlyUAPI = regexp.MustCompile(`^(get_|list_|php_get_|domains_data$|single_domain_data$)`)

func stepUAPI(ctx context.Context, rc *RunContext, args map[string]string) (map[string]string, error) {
	mod, fn := args["module"], args["function"]
	if mod == "" || fn == "" {
		return nil, fmt.Errorf("module and function are required")
	}
	if !readOnlyUAPI.MatchString(fn) {
		return nil, fmt.Errorf("%s::%s is not a read-only call; use a dedicated step type", mod, fn)
	}
	c, err := rc.CPanel(ctx)
	if err != nil {
		return nil, err
	}
	v := url.Values{}
	for k, val := range args {
		if k != "module" && k != "function" {
			v.Set(k, val)
		}
	}
	data, err := c.Call(ctx, mod, fn, v)
	if err != nil {
		return nil, err
	}
	s := string(data)
	if len(s) > 2000 {
		s = s[:2000] + "…"
	}
	rc.Logf("%s::%s → %s", mod, fn, s)
	return map[string]string{"json": string(data)}, nil
}

// DocRoot is one domain and its document root.
type DocRoot struct {
	Domain string
	Root   string
	Kind   string // main | addon | sub | parked
}

// DocRoots lists every domain of the account with its document root.
func DocRoots(ctx context.Context, c *cpanel.Client) ([]DocRoot, error) {
	data, err := c.Call(ctx, "DomainInfo", "domains_data", url.Values{"format": {"hash"}})
	if err != nil {
		return nil, err
	}
	type dom struct {
		Domain       string `json:"domain"`
		DocumentRoot string `json:"documentroot"`
	}
	var d struct {
		Main  dom   `json:"main_domain"`
		Addon []dom `json:"addon_domains"`
		Sub   []dom `json:"sub_domains"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("unexpected domains_data response")
	}
	out := []DocRoot{{d.Main.Domain, d.Main.DocumentRoot, "main"}}
	for _, a := range d.Addon {
		out = append(out, DocRoot{a.Domain, a.DocumentRoot, "addon"})
	}
	for _, s := range d.Sub {
		out = append(out, DocRoot{s.Domain, s.DocumentRoot, "sub"})
	}
	return out, nil
}

// WPInstall describes a WordPress found on the host.
type WPInstall struct {
	Domain  string
	Path    string
	Version string
}

var wpVersionRe = regexp.MustCompile(`\$wp_version\s*=\s*'([^']+)'`)

type dirEntry struct {
	File string `json:"file"`
	Type string `json:"type"`
}

func listDir(ctx context.Context, c *cpanel.Client, dir string) ([]dirEntry, error) {
	data, err := c.Call(ctx, "Fileman", "list_files", url.Values{"dir": {dir}})
	if err != nil {
		return nil, err
	}
	var files []dirEntry
	_ = json.Unmarshal(data, &files)
	return files, nil
}

// skipSubdirs are folders inside a document root that are never a WordPress install of their own.
var skipSubdirs = map[string]bool{"cgi-bin": true, ".well-known": true, "wp-admin": true, "wp-content": true, "wp-includes": true,
	"assets": true, "css": true, "js": true, "images": true, "img": true, "fonts": true, "vendor": true, "node_modules": true, "tmp": true, "logs": true}

// DetectWordPress checks a directory for wp-config.php + wp-includes and reads the version.
func DetectWordPress(ctx context.Context, c *cpanel.Client, dir string) (bool, string, error) {
	files, err := listDir(ctx, c, dir)
	if err != nil {
		return false, "", err
	}
	hasConfig, hasIncludes := false, false
	for _, f := range files {
		switch {
		case f.File == "wp-config.php":
			hasConfig = true
		case f.File == "wp-includes" && f.Type == "dir":
			hasIncludes = true
		}
	}
	if !hasConfig || !hasIncludes {
		return false, "", nil
	}
	ver := "?"
	if raw, err := c.Call(ctx, "Fileman", "get_file_content", url.Values{"dir": {path.Join(dir, "wp-includes")}, "file": {"version.php"}}); err == nil {
		var fc struct {
			Content string `json:"content"`
		}
		_ = json.Unmarshal(raw, &fc)
		if m := wpVersionRe.FindStringSubmatch(fc.Content); m != nil {
			ver = m[1]
		}
	}
	return true, ver, nil
}

func stepWPDetect(ctx context.Context, rc *RunContext, args map[string]string) (map[string]string, error) {
	c, err := rc.CPanel(ctx)
	if err != nil {
		return nil, err
	}
	roots, err := DocRoots(ctx, c)
	if err != nil {
		return nil, err
	}
	want := strings.TrimSpace(args["domain"])
	wantDomain, wantPath, _ := strings.Cut(want, "/")
	wantPath = strings.Trim(wantPath, "/")
	isRoot := map[string]bool{}
	for _, r := range roots {
		isRoot[path.Clean(r.Root)] = true
	}
	var found []string
	checked := 0
	check := func(label, dir string) {
		ok, ver, err := DetectWordPress(ctx, c, dir)
		switch {
		case err != nil:
			rc.Logf("⚠ %s: %v", label, err)
		case ok:
			rc.Logf("وردپرس %s روی %s پیدا شد (%s)", ver, label, dir)
			found = append(found, label)
		case wantPath != "" || !strings.Contains(label, "/"):
			rc.Logf("%s: وردپرسی نیست (%s)", label, dir)
		}
		time.Sleep(200 * time.Millisecond) // be gentle with shared hosts
	}
	for _, r := range roots {
		// A domain also covers its subdomains (sub.domain.com), unless a folder was asked for.
		if want != "" && r.Domain != wantDomain && (wantPath != "" || !strings.HasSuffix(r.Domain, "."+wantDomain)) {
			continue
		}
		checked++
		if wantPath != "" {
			check(r.Domain+"/"+wantPath, path.Join(r.Root, wantPath))
			continue
		}
		check(r.Domain, r.Root)
		// installs in a folder of this domain (e.g. domain.com/blog), one level deep
		files, err := listDir(ctx, c, r.Root)
		if err != nil {
			continue
		}
		n := 0
		for _, f := range files {
			if f.Type != "dir" || skipSubdirs[f.File] || strings.HasPrefix(f.File, ".") {
				continue
			}
			sub := path.Join(r.Root, f.File)
			if isRoot[sub] { // another domain's document root (usual for subdomains)
				continue
			}
			if n++; n > 40 {
				rc.Logf("⚠ %s: پوشه‌ها زیادند؛ فقط ۴۰ پوشه‌ی اول بررسی شد", r.Domain)
				break
			}
			check(r.Domain+"/"+f.File, sub)
		}
	}
	if want != "" && checked == 0 {
		return nil, fmt.Errorf("domain %s is not on this account", want)
	}
	return map[string]string{"count": fmt.Sprint(len(found)), "domains": strings.Join(found, ",")}, nil
}
