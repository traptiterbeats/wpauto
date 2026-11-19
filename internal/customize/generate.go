package customize

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/traptiterbeats/wpauto/internal/ai"
)

// Chatter is the part of ai.Client the generator uses.
type Chatter interface {
	Chat(ctx context.Context, system, user string, jsonMode bool) (string, ai.Usage, error)
}

const system = `You are a senior website copywriter. You rewrite the sample texts of a website theme's demo so the site belongs to a real client.
Rules:
- Use only facts from the client brief. Never invent prices, awards, client names, numbers, addresses or contact details; where a fact is missing write something general and true.
- Keep each text's role and roughly its length (a short label stays short; a heading stays a heading).
- For fields of kind "html", return HTML using only these tags: p, h2, h3, h4, ul, ol, li, strong, em, a, br, blockquote. Keep the same structure as the original where it makes sense.
- For fields of kind "inline" use only the tags in allowed_tags ("span.grad" means <span class="grad">); keep markup minimal.
- For "short" and "text" fields return plain text without HTML or Markdown.
- "role" explains what a text is for; "hint" is the theme author's instruction and must be followed. Items with the same role (e.g. services.item.title) form a list: fill them in order with different content.
- Write in the requested language with natural, fluent wording (for Persian: correct half-spaces and Persian digits).
- Reply with ONE JSON object whose keys are exactly the field keys you were given and whose values are the new texts. No other keys, no comments.`

// briefText renders the brief for the prompt.
func briefText(b Brief) string {
	var sb strings.Builder
	line := func(label, v string) {
		if v = strings.TrimSpace(v); v != "" {
			fmt.Fprintf(&sb, "%s: %s\n", label, v)
		}
	}
	line("Site / brand name", b.SiteName)
	line("Field of business", b.Business)
	line("About the business", b.About)
	line("Services or products", b.Services)
	line("Target audience", b.Audience)
	line("Tone of voice", b.Tone)
	line("Email", b.Email)
	line("Phone", b.Phone)
	line("Address", b.Address)
	line("Other notes", b.Notes)
	return sb.String()
}

// batches splits AI fields so each request stays small.
func batches(fields []Field, maxChars int) [][]Field {
	var out [][]Field
	var cur []Field
	n := 0
	for _, f := range fields {
		l := len(f.Original) + 80
		if len(cur) > 0 && (n+l > maxChars || len(cur) >= 40) {
			out = append(out, cur)
			cur, n = nil, 0
		}
		cur = append(cur, f)
		n += l
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

var (
	reScript   = regexp.MustCompile(`(?is)<(script|style|iframe|object|embed|form)[^>]*>.*?</(script|style|iframe|object|embed|form)>|<(script|style|iframe|object|embed|form)[^>]*/?>`)
	reOnAttr   = regexp.MustCompile(`(?i)\s(on[a-z]+|style)\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
	reJSURL    = regexp.MustCompile(`(?i)(href|src)\s*=\s*("|')?\s*javascript:`)
	reAnyTag   = regexp.MustCompile(`<[^>]*>`)
	reMarkdown = regexp.MustCompile(`(\*\*|__)(.+?)(\*\*|__)`)
)

var reTag = regexp.MustCompile(`<(/?)([a-zA-Z0-9]+)([^>]*)>`)
var reClass = regexp.MustCompile(`class\s*=\s*("([^"]*)"|'([^']*)')`)

// inline keeps only the allowed tags ("tag" or "tag.class") and drops every attribute but an
// allowed class.
func inline(v string, allow []string) string {
	tags := map[string]map[string]bool{}
	for _, a := range allow {
		t, c, _ := strings.Cut(strings.ToLower(a), ".")
		if tags[t] == nil {
			tags[t] = map[string]bool{}
		}
		if c != "" {
			tags[t][c] = true
		}
	}
	return reTag.ReplaceAllStringFunc(v, func(m string) string {
		p := reTag.FindStringSubmatch(m)
		name := strings.ToLower(p[2])
		classes, ok := tags[name]
		if !ok {
			return ""
		}
		if p[1] == "/" {
			return "</" + name + ">"
		}
		if name == "br" {
			return "<br>"
		}
		if cm := reClass.FindStringSubmatch(p[3]); cm != nil {
			var keep []string
			for _, c := range strings.Fields(cm[2] + cm[3]) {
				if classes[strings.ToLower(c)] {
					keep = append(keep, c)
				}
			}
			if len(keep) > 0 {
				return "<" + name + ` class="` + strings.Join(keep, " ") + `">`
			}
		}
		return "<" + name + ">"
	})
}

// Clean makes a value safe for its field: HTML is filtered, plain kinds lose markup, and
// e-mail, URL and colour values that do not parse become empty.
func Clean(f Field, v string) string {
	v = strings.TrimSpace(v)
	switch f.Kind {
	case "html":
		v = reScript.ReplaceAllString(v, "")
		v = reOnAttr.ReplaceAllString(v, "")
		return reJSURL.ReplaceAllString(v, `$1="#`)
	case "inline":
		allow := f.Allow
		if len(allow) == 0 {
			allow = []string{"br", "strong", "em"}
		}
		return strings.Join(strings.Fields(inline(reScript.ReplaceAllString(v, ""), allow)), " ")
	case "email":
		if reEmail.MatchString(v) {
			return v
		}
		return ""
	case "url":
		if u, err := url.Parse(v); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
			return v
		}
		return ""
	case "color":
		if reColor.MatchString(v) {
			return strings.ToUpper(v)
		}
		return ""
	}
	v = reAnyTag.ReplaceAllString(v, "")
	v = reMarkdown.ReplaceAllString(v, "$2")
	if f.Kind == "short" {
		v = strings.Join(strings.Fields(v), " ")
	}
	return v
}

// limit is the longest text accepted for a field.
func limit(f Field) int {
	n := len([]rune(f.Original)) * 3
	if n < 300 {
		n = 300
	}
	if f.Kind == "short" && n > 600 {
		n = 600
	}
	return n
}

// FromBrief sets the data fields (names, contact, socials, colours) from the brief. A data field
// the brief leaves empty is cleared, so the demo's sample e-mail or social links never reach a
// client's site; colours, Keep fields and a missing Latin brand name (the AI writes it) stay.
// It returns the paths it decided.
func FromBrief(fields []Field, b Brief, values map[string]string) map[string]bool {
	done := map[string]bool{}
	for _, f := range fields {
		if !f.Data {
			continue
		}
		if v := Clean(f, briefValue(b, f.Role)); v != "" {
			values[f.Path], done[f.Path] = v, true
			continue
		}
		if f.Keep || f.Role == "brand.latin" || f.Role == "site.name" || strings.HasPrefix(f.Role, "color.") {
			continue
		}
		values[f.Path], done[f.Path] = "", true
	}
	return done
}

// Progress reports generation progress (batch i of n).
type Progress func(done, total int)

// Generate asks the model for new texts for every AI field and returns them by path.
func Generate(ctx context.Context, c Chatter, demo *Demo, b Brief, progress Progress) (map[string]string, int, error) {
	values := map[string]string{}
	done := FromBrief(demo.Fields, b, values)
	var fields []Field
	for _, f := range demo.Fields {
		if f.AI && !done[f.Path] {
			fields = append(fields, f)
		}
	}
	lang := strings.TrimSpace(b.Language)
	if lang == "" {
		lang = "Persian (Farsi)"
	}
	groups := batches(fields, 9000)
	tokens := 0
	for i, batch := range groups {
		if progress != nil {
			progress(i, len(groups))
		}
		list := make([]map[string]string, 0, len(batch))
		keys := map[string]Field{}
		for j, f := range batch {
			k := fmt.Sprintf("f%d", j+1)
			keys[k] = f
			m := map[string]string{"key": k, "where": f.Group, "label": f.Label, "kind": f.Kind, "original": f.Original}
			if f.Hint != "" {
				m["hint"] = f.Hint
			}
			if g := roleGuide[f.Role]; g != "" {
				m["role"] = g
			}
			if f.Kind == "inline" {
				m["allowed_tags"] = strings.Join(f.Allow, ", ")
			}
			list = append(list, m)
		}
		js, _ := json.MarshalIndent(list, "", " ")
		user := "Language: " + lang + "\n\nClient brief:\n" + briefText(b) + "\nFields to rewrite (JSON):\n" + string(js)
		var got map[string]any
		var lastErr error
		for attempt := 0; attempt < 2; attempt++ {
			out, u, err := c.Chat(ctx, system, user, true)
			tokens += u.Prompt + u.Completion
			if err == nil {
				got, err = ai.JSONObject(out)
			}
			if err == nil {
				break
			}
			lastErr = err
			if ctx.Err() != nil {
				return values, tokens, ctx.Err()
			}
		}
		if got == nil {
			return values, tokens, fmt.Errorf("بخش %d از %d: %w", i+1, len(groups), lastErr)
		}
		for k, f := range keys {
			s, ok := got[k].(string)
			if !ok {
				continue
			}
			s = Clean(f, s)
			if s == "" || len([]rune(s)) > limit(f) {
				continue
			}
			values[f.Path] = s
		}
	}
	if progress != nil {
		progress(len(groups), len(groups))
	}
	return values, tokens, nil
}
