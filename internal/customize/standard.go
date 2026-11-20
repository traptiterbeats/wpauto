package customize

import "strings"

// CoreRoles are the texts every theme following the wpauto-customize contract must expose, so a
// client's site never keeps the demo's subject (a carpentry site must not talk about software).
var CoreRoles = []struct{ Role, Label string }{
	{"site.name", "نام سایت"},
	{"hero.title", "سرتیتر اصلی"},
	{"hero.subtitle", "متن زیر سرتیتر"},
	{"about.text", "متن درباره"},
	{"services.item.title", "عنوان خدمات"},
	{"contact.title", "عنوان بخش تماس"},
}

// Standard reports how well a demo follows the contract: ok when it has customize.json with the
// contract name and every core role; missing lists the labels of the core roles it lacks.
func (d *Demo) Standard() (ok bool, missing []string) {
	have := map[string]bool{}
	for _, f := range d.Fields {
		if f.Role != "" {
			have[f.Role] = true
			// "about.text" is also satisfied by about.lead etc.
			if i := strings.LastIndex(f.Role, "."); i > 0 && !strings.Contains(f.Role, ".item.") {
				have[f.Role[:i]+".*"] = true
			}
		}
	}
	for _, c := range CoreRoles {
		if have[c.Role] {
			continue
		}
		if i := strings.LastIndex(c.Role, "."); i > 0 && !strings.Contains(c.Role, ".item.") && c.Role != "site.name" && have[c.Role[:i]+".*"] {
			continue
		}
		missing = append(missing, c.Label)
	}
	return d.Spec != nil && d.Spec.Contract == "wpauto-customize" && len(missing) == 0, missing
}
