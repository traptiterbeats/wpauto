package customize

import (
	"net/url"
	"regexp"
	"strings"
)

// The shared customisation contract ("wpauto-customize" v1). A theme's customize.json maps its
// own texts to these roles; wpauto fills data roles from the brief and tells the AI what each
// text role is for, so one brief works on every theme that follows the contract.

// Socials are the social networks the brief asks for.
var Socials = []struct{ ID, Label, Placeholder string }{
	{"instagram", "اینستاگرام", "@naranj.cafe"},
	{"telegram", "تلگرام", "@naranj_cafe"},
	{"whatsapp", "واتس‌اپ", "+98912…"},
	{"linkedin", "لینکدین", "https://linkedin.com/in/…"},
	{"x", "ایکس", "@naranj"},
	{"youtube", "یوتیوب", "https://youtube.com/@…"},
	{"aparat", "آپارات", "https://aparat.com/…"},
	{"github", "گیت‌هاب", "naranj"},
	{"website", "وب‌سایت دیگر", "https://…"},
}

// roleGuide tells the model what a text role is.
var roleGuide = map[string]string{
	"site.tagline":             "site tagline shown under the site name; one short sentence",
	"brand.latin":              "brand name written in Latin capital letters (transliterate the brand if needed)",
	"hero.title":               "main headline of the home page; at most 8 words, strong and specific",
	"hero.subtitle":            "one or two sentences under the main headline",
	"cta.primary":              "label of the main call-to-action button; 2-3 words",
	"cta.secondary":            "label of the secondary button; 2-3 words",
	"about.title":              "heading of the about section",
	"about.text":               "main paragraph introducing the business",
	"services.title":           "heading of the services section",
	"services.text":            "short intro of the services section",
	"services.item.title":      "name of one service; use the brief's services in order, one per item",
	"services.item.text":       "one-sentence description of that service",
	"portfolio.title":          "heading of the portfolio / work section",
	"portfolio.text":           "short intro of the portfolio section",
	"portfolio.item.title":     "title of a sample project; plausible for this business, clearly a sample",
	"portfolio.item.summary":   "one or two sentence summary of that sample project",
	"portfolio.item.content":   "body of that sample project",
	"blog.title":               "name of the blog page",
	"blog.post.title":          "title of a sample blog post relevant to the business",
	"blog.post.summary":        "one sentence summary of that post",
	"blog.post.content":        "short sample blog post (2-3 paragraphs)",
	"contact.title":            "heading of the contact section, inviting",
	"contact.text":             "short text inviting visitors to get in touch",
	"menu.item":                "menu label; keep the same destination/meaning as the original, 1-2 words",
	"footer.text":              "short footer line after the year",
	"testimonial.item.text":    "a testimonial; write it as an obvious placeholder the owner must replace",
	"faq.item.question":        "a frequently asked question for this business",
	"faq.item.answer":          "answer to that question using only brief facts",
	"product.item.title":       "name of a sample product this business could really sell; each item different",
	"product.item.description": "description of that sample product (features, use); no prices or invented facts",
	"product.item.short":       "one-sentence short description of that sample product",
}

// productTextRole reports whether a product role is a text the AI writes in the ai products
// mode (other product.* roles such as prices or SKUs stay as in the demo unless marked ai).
func productTextRole(role string) bool {
	switch role {
	case "product.item.title", "product.item.description", "product.item.short":
		return true
	}
	return false
}

// dataRole reports whether a role is filled from the brief instead of the AI.
func dataRole(role string) bool {
	switch {
	case role == "site.name", role == "brand.latin", strings.HasPrefix(role, "contact.") && role != "contact.title" && role != "contact.text",
		strings.HasPrefix(role, "social."), strings.HasPrefix(role, "color."):
		return true
	}
	return false
}

// briefValue returns the brief's value for a data role.
func briefValue(b Brief, role string) string {
	switch role {
	case "site.name":
		return b.SiteName
	case "brand.latin":
		return b.BrandLatin
	case "contact.email":
		return b.Email
	case "contact.phone":
		return b.Phone
	case "contact.address":
		return b.Address
	case "contact.hours":
		return b.Hours
	case "color.primary":
		return b.ColorPrimary
	case "color.secondary":
		return b.ColorSecondary
	}
	if s, ok := strings.CutPrefix(role, "social."); ok {
		return SocialURL(s, b.Socials[s])
	}
	return ""
}

var digits = regexp.MustCompile(`[^0-9]`)

// SocialURL turns a handle, number or link into a full URL.
func SocialURL(network, v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
		if u, err := url.Parse(v); err == nil && u.Host != "" {
			return v
		}
		return ""
	}
	h := strings.TrimPrefix(v, "@")
	switch network {
	case "instagram":
		return "https://instagram.com/" + h
	case "telegram":
		return "https://t.me/" + h
	case "x":
		return "https://x.com/" + h
	case "github":
		return "https://github.com/" + h
	case "youtube":
		return "https://youtube.com/@" + h
	case "aparat":
		return "https://aparat.com/" + h
	case "linkedin":
		return "https://linkedin.com/in/" + h
	case "whatsapp":
		d := digits.ReplaceAllString(toLatinDigits(v), "")
		if strings.HasPrefix(d, "00") {
			d = d[2:]
		} else if strings.HasPrefix(d, "0") {
			d = "98" + d[1:]
		}
		if d == "" {
			return ""
		}
		return "https://wa.me/" + d
	}
	return "https://" + h
}

func toLatinDigits(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= '۰' && r <= '۹':
			return '0' + (r - '۰')
		case r >= '٠' && r <= '٩':
			return '0' + (r - '٠')
		}
		return r
	}, s)
}

// autoRole guesses a role for a theme mod in automatic mode.
func autoRole(key, value string) string {
	k := strings.ToLower(key)
	switch {
	case reEmail.MatchString(strings.TrimSpace(value)):
		return "contact.email"
	case rePhone.MatchString(strings.TrimSpace(value)):
		return "contact.phone"
	}
	if strings.HasPrefix(value, "http") {
		for _, s := range Socials {
			if s.ID != "website" && s.ID != "x" && strings.Contains(k, s.ID) {
				return "social." + s.ID
			}
		}
		if strings.HasSuffix(k, "_x") || strings.Contains(k, "twitter") {
			return "social.x"
		}
	}
	return ""
}
