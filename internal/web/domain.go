package web

import (
	"errors"
	"regexp"
	"strings"

	"github.com/traptiterbeats/wpauto/internal/projects"
)

// Site kinds map one to one onto the wp-install modes.
const (
	kindRoot      = "root"      // example.com
	kindSubdomain = "subdomain" // shop.example.com
	kindSubfolder = "subfolder" // example.com/blog
)

// kindLabel is the Persian name of a site kind.
var kindLabel = map[string]string{kindRoot: "اصلی", kindSubdomain: "ساب‌دامین", kindSubfolder: "پوشه"}

// multiSuffixes are public suffixes with two labels, so example.co.ir is a main domain and
// shop.example.co.ir a subdomain. The same list is used by the page's script (home.html).
var multiSuffixes = []string{
	"co.ir", "ac.ir", "org.ir", "net.ir", "gov.ir", "id.ir", "sch.ir",
	"co.uk", "org.uk", "ac.uk", "com.au", "net.au", "org.au", "com.tr", "net.tr", "org.tr",
	"com.br", "co.jp", "co.in", "co.nz", "co.za", "com.cn", "com.mx", "com.de",
}

var labelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
var folderRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// siteAddr is a parsed project address.
type siteAddr struct {
	Kind   string // root | subdomain | subfolder ("" when empty input)
	Domain string // the site's host name: example.com or shop.example.com
	Folder string // blog for example.com/blog
	Parent string // the registrable domain the site lives on (example.com)
	Label  string // the subdomain label or the folder name (what wp-install calls "name")
}

// KindLabel is the Persian name of the kind.
func (a siteAddr) KindLabel() string { return kindLabel[a.Kind] }

// registrable returns the main domain of host (the last two labels, or three for a known
// two-label suffix).
func registrable(host string) string {
	labels := strings.Split(host, ".")
	n := 2
	for _, s := range multiSuffixes {
		if strings.HasSuffix(host, "."+s) {
			n = 3
			break
		}
	}
	if len(labels) <= n {
		return host
	}
	return strings.Join(labels[len(labels)-n:], ".")
}

// parseSite reads what the user typed in the domain field: example.com, shop.example.com or
// example.com/blog (with or without http(s):// and www.). An empty input is not an error.
func parseSite(in string) (siteAddr, error) {
	s := strings.ToLower(strings.TrimSpace(in))
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	s = strings.Trim(s, "/")
	if s == "" {
		return siteAddr{}, nil
	}
	host, folder, _ := strings.Cut(s, "/")
	folder = strings.Trim(folder, "/")
	host = strings.TrimPrefix(host, "www.")
	if i := strings.IndexAny(host, ":?#"); i >= 0 {
		host = host[:i]
	}
	bad := errors.New("آدرس معتبر نیست؛ مثل example.com یا shop.example.com یا example.com/blog بنویسید")
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return siteAddr{}, bad
	}
	for _, l := range labels {
		if !labelRe.MatchString(l) {
			return siteAddr{}, bad
		}
	}
	a := siteAddr{Domain: host, Parent: registrable(host)}
	switch {
	case folder != "":
		if strings.Contains(folder, "/") || !folderRe.MatchString(folder) {
			return siteAddr{}, errors.New("نام پوشه فقط حروف کوچک انگلیسی، عدد و خط تیره و یک سطح باشد (مثل example.com/blog)")
		}
		a.Kind, a.Folder, a.Label = kindSubfolder, folder, folder
	case host != a.Parent:
		a.Kind = kindSubdomain
		a.Label = strings.TrimSuffix(host, "."+a.Parent)
		if strings.Contains(a.Label, ".") || !folderRe.MatchString(a.Label) {
			return siteAddr{}, errors.New("ساب‌دامین فقط یک سطح و با حروف کوچک انگلیسی، عدد و خط تیره باشد (مثل shop.example.com)")
		}
	default:
		a.Kind = kindRoot
	}
	return a, nil
}

// projectAddr works out a saved project's address and kind (older projects have no Mode).
func projectAddr(p projects.Project) siteAddr {
	if p.Domain == "" {
		return siteAddr{}
	}
	a, err := parseSite(p.Site())
	if err != nil {
		return siteAddr{Domain: p.Domain, Folder: p.Folder, Parent: p.Domain}
	}
	if p.Mode == kindRoot && a.Kind == kindSubdomain {
		// the user said it is a main domain (e.g. an addon domain with an unusual suffix)
		a.Kind, a.Parent, a.Label = kindRoot, a.Domain, ""
	}
	return a
}

// installInputs is what wp-install and AutoPilot need for a project: mode, main domain and the
// subdomain/folder name. ok is false when the project has no address yet.
func installInputs(p projects.Project) (mode, domain, name string, ok bool) {
	a := projectAddr(p)
	if a.Kind == "" {
		return "", "", "", false
	}
	switch a.Kind {
	case kindSubfolder:
		return kindSubfolder, a.Domain, a.Folder, true
	case kindSubdomain:
		return kindSubdomain, a.Parent, a.Label, true
	}
	return kindRoot, a.Domain, "", true
}
