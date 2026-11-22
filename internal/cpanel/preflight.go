package cpanel

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Check is one probe in a preflight report.
type Check struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Report is the capability profile of a host, built by Preflight.
type Report struct {
	At             time.Time         `json:"at"`
	LoginOK        bool              `json:"login_ok"`
	Checks         []Check           `json:"checks"`
	Domains        []string          `json:"domains,omitempty"`
	MainDomain     string            `json:"main_domain,omitempty"`
	PHPVersions    []string          `json:"php_versions,omitempty"`
	SitePHP        map[string]string `json:"site_php,omitempty"`
	DBPrefix       string            `json:"db_prefix,omitempty"`
	DBNameMax      int               `json:"db_name_max,omitempty"`
	DiskUsedMB     float64           `json:"disk_used_mb,omitempty"`
	DiskLimitMB    float64           `json:"disk_limit_mb,omitempty"`
	APITokens      bool              `json:"api_tokens"`
	Features       []string          `json:"features,omitempty"`
	PreferredPHP   string            `json:"preferred_php,omitempty"`
	ReadyForWP     bool              `json:"ready_for_wp"`
	Recommendation string            `json:"recommendation,omitempty"`
}

// Probe names a preflight check that a profile can enable.
const (
	ProbeUser     = "user"
	ProbeDomains  = "domains"
	ProbePHP      = "php"
	ProbeMySQL    = "mysql"
	ProbeQuota    = "quota"
	ProbeTokens   = "tokens"
	ProbeFeatures = "features"
)

// AllProbes is the default probe list.
var AllProbes = []string{ProbeUser, ProbeDomains, ProbePHP, ProbeMySQL, ProbeQuota, ProbeTokens, ProbeFeatures}

// Preflight runs read-only probes and returns the host's capability report. It never changes
// anything on the account. preferredPHP is the profile's ordered list (e.g. 8.3, 8.4).
func Preflight(ctx context.Context, c *Client, probes []string, preferredPHP []string) *Report {
	rep := &Report{At: time.Now()}
	if err := c.Login(ctx); err != nil {
		rep.Checks = append(rep.Checks, Check{ID: "login", Title: "ورود به cPanel", Detail: err.Error()})
		rep.Recommendation = "ورود ناموفق بود. اطلاعات را بررسی کنید؛ برای جلوگیری از بلاک شدن IP، تلاش خودکار دیگری انجام نمی‌شود."
		return rep
	}
	defer c.Logout(ctx)
	return Probe(ctx, c, probes, preferredPHP)
}

// Probe runs the read-only probes on a client that is already signed in.
func Probe(ctx context.Context, c *Client, probes []string, preferredPHP []string) *Report {
	rep := &Report{At: time.Now(), LoginOK: true}
	rep.Checks = append(rep.Checks, Check{ID: "login", Title: "ورود به cPanel", OK: true, Detail: c.BaseURL()})
	if len(probes) == 0 {
		probes = AllProbes
	}
	for _, p := range probes {
		switch p {
		case ProbeUser:
			rep.add(probeUser(ctx, c))
		case ProbeDomains:
			rep.add(probeDomains(ctx, c, rep))
		case ProbePHP:
			rep.add(probePHP(ctx, c, rep, preferredPHP))
		case ProbeMySQL:
			rep.add(probeMySQL(ctx, c, rep))
		case ProbeQuota:
			rep.add(probeQuota(ctx, c, rep))
		case ProbeTokens:
			_, err := c.Call(ctx, "Tokens", "list", nil)
			rep.APITokens = err == nil
			rep.add(Check{ID: ProbeTokens, Title: "API Token", OK: true, Detail: yesNo(err == nil, "فعال است", "غیرفعال؛ از ورود با نشست استفاده می‌شود")})
		case ProbeFeatures:
			rep.add(probeFeatures(ctx, c, rep))
		}
	}
	rep.ReadyForWP = rep.LoginOK && rep.checkOK(ProbeMySQL) && rep.MainDomain != ""
	if rep.ReadyForWP {
		rep.Recommendation = "هاست برای نصب وردپرس آماده است."
		if rep.PreferredPHP != "" {
			rep.Recommendation += " نسخه‌ی PHP پیشنهادی: " + rep.PreferredPHP
		}
	} else {
		rep.Recommendation = "بعضی بررسی‌ها ناموفق بود؛ جزئیات را ببینید."
	}
	return rep
}

func (r *Report) add(c Check) { r.Checks = append(r.Checks, c) }

func (r *Report) checkOK(id string) bool {
	for _, c := range r.Checks {
		if c.ID == id {
			return c.OK
		}
	}
	return false
}

func yesNo(b bool, y, n string) string {
	if b {
		return y
	}
	return n
}

func fail(id, title string, err error) Check { return Check{ID: id, Title: title, Detail: err.Error()} }

func probeUser(ctx context.Context, c *Client) Check {
	data, err := c.Call(ctx, "Variables", "get_user_information", nil)
	if err != nil {
		return fail(ProbeUser, "اطلاعات حساب", err)
	}
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	parts := []string{}
	for _, k := range []string{"user", "plan", "theme", "home"} {
		if v, ok := m[k]; ok && v != nil {
			parts = append(parts, fmt.Sprintf("%s: %v", k, v))
		}
	}
	return Check{ID: ProbeUser, Title: "اطلاعات حساب", OK: true, Detail: strings.Join(parts, " · ")}
}

func probeDomains(ctx context.Context, c *Client, rep *Report) Check {
	data, err := c.Call(ctx, "DomainInfo", "list_domains", nil)
	if err != nil {
		// The account's own record also names the main domain: enough for the report.
		if ctx.Err() == nil {
			if info, uerr := c.Call(ctx, "Variables", "get_user_information", nil); uerr == nil {
				var u struct {
					Domain string `json:"domain"`
				}
				if json.Unmarshal(info, &u) == nil && u.Domain != "" {
					rep.MainDomain = u.Domain
					rep.Domains = []string{u.Domain}
					return Check{ID: ProbeDomains, Title: "دامنه‌ها", OK: true, Detail: "⚠ فهرست کامل دامنه‌ها خوانده نشد؛ دامنه‌ی اصلی از اطلاعات حساب: " + u.Domain}
				}
			}
		}
		return fail(ProbeDomains, "دامنه‌ها", err)
	}
	var d struct {
		Main  string   `json:"main_domain"`
		Addon []string `json:"addon_domains"`
		Sub   []string `json:"sub_domains"`
	}
	_ = json.Unmarshal(data, &d)
	rep.MainDomain = d.Main
	rep.Domains = append([]string{d.Main}, append(d.Addon, d.Sub...)...)
	return Check{ID: ProbeDomains, Title: "دامنه‌ها", OK: d.Main != "", Detail: fmt.Sprintf("اصلی: %s · افزودنی: %d · ساب‌دامین: %d", d.Main, len(d.Addon), len(d.Sub))}
}

func probePHP(ctx context.Context, c *Client, rep *Report, preferred []string) Check {
	data, err := c.Call(ctx, "LangPHP", "php_get_installed_versions", nil)
	if err != nil {
		return fail(ProbePHP, "نسخه‌های PHP", err)
	}
	var inst struct {
		Versions []string `json:"versions"`
	}
	_ = json.Unmarshal(data, &inst)
	rep.PHPVersions = inst.Versions
	rep.SitePHP = map[string]string{}
	if vh, err := c.Call(ctx, "LangPHP", "php_get_vhost_versions", nil); err == nil {
		var list []struct {
			Vhost   string `json:"vhost"`
			Version string `json:"version"`
		}
		_ = json.Unmarshal(vh, &list)
		for _, v := range list {
			rep.SitePHP[v.Vhost] = v.Version
		}
	}
	for _, want := range preferred {
		pkg := "ea-php" + strings.ReplaceAll(want, ".", "")
		for _, have := range inst.Versions {
			if have == pkg {
				rep.PreferredPHP = want
				break
			}
		}
		if rep.PreferredPHP != "" {
			break
		}
	}
	return Check{ID: ProbePHP, Title: "نسخه‌های PHP", OK: len(inst.Versions) > 0, Detail: strings.Join(inst.Versions, ", ")}
}

func probeMySQL(ctx context.Context, c *Client, rep *Report) Check {
	data, err := c.Call(ctx, "Mysql", "get_restrictions", nil)
	if err != nil {
		return fail(ProbeMySQL, "دیتابیس MySQL", err)
	}
	var r struct {
		Prefix  string `json:"prefix"`
		NameMax int    `json:"max_database_name_length"`
	}
	_ = json.Unmarshal(data, &r)
	rep.DBPrefix, rep.DBNameMax = r.Prefix, r.NameMax
	count := "?"
	if dbs, err := c.Call(ctx, "Mysql", "list_databases", nil); err == nil {
		var l []any
		_ = json.Unmarshal(dbs, &l)
		count = fmt.Sprint(len(l))
	}
	return Check{ID: ProbeMySQL, Title: "دیتابیس MySQL", OK: true, Detail: fmt.Sprintf("پیشوند: %s · حداکثر طول نام: %d · دیتابیس‌های موجود: %s", r.Prefix, r.NameMax, count)}
}

func probeQuota(ctx context.Context, c *Client, rep *Report) Check {
	var err error
	rep.DiskUsedMB, rep.DiskLimitMB, err = c.DiskUsage(ctx)
	if err != nil {
		return fail(ProbeQuota, "فضای دیسک", err)
	}
	limit := "نامحدود"
	if rep.DiskLimitMB > 0 {
		limit = fmt.Sprintf("%.0f MB", rep.DiskLimitMB)
	}
	ok := rep.DiskLimitMB == 0 || rep.DiskLimitMB-rep.DiskUsedMB > 150 // WordPress + theme needs ~100-150MB
	return Check{ID: ProbeQuota, Title: "فضای دیسک", OK: ok, Detail: fmt.Sprintf("مصرف: %.1f MB از %s", rep.DiskUsedMB, limit)}
}

func probeFeatures(ctx context.Context, c *Client, rep *Report) Check {
	data, err := c.Call(ctx, "Features", "list_features", nil)
	if err != nil {
		return fail(ProbeFeatures, "امکانات فعال", err)
	}
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	for k, v := range m {
		if num(v) == 1 {
			rep.Features = append(rep.Features, k)
		}
	}
	sort.Strings(rep.Features)
	interesting := []string{}
	for _, f := range []string{"fileman", "mysql", "multiphp", "sslinstall", "autossl", "softaculous", "wp-toolkit", "ssh", "api_shell"} {
		for _, have := range rep.Features {
			if have == f {
				interesting = append(interesting, f)
			}
		}
	}
	return Check{ID: ProbeFeatures, Title: "امکانات فعال", OK: true, Detail: fmt.Sprintf("%d مورد · %s", len(rep.Features), strings.Join(interesting, ", "))}
}

func num(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		var f float64
		_, _ = fmt.Sscan(t, &f)
		return f
	case bool:
		if t {
			return 1
		}
	}
	return 0
}

// DiskUsage returns the account's used and allowed disk space in MB (limit 0 = unlimited).
func (c *Client) DiskUsage(ctx context.Context) (usedMB, limitMB float64, err error) {
	data, err := c.Call(ctx, "Quota", "get_quota_info", nil)
	if err != nil {
		return 0, 0, err
	}
	var q map[string]any
	_ = json.Unmarshal(data, &q)
	return num(q["megabytes_used"]), num(q["megabyte_limit"]), nil
}
