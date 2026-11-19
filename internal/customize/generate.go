package customize

import (
	"context"
	"encoding/json"
	"fmt"
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
- For "short" and "text" fields return plain text without HTML or Markdown.
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

// Clean makes a generated value safe for its kind.
func Clean(kind, v string) string {
	v = strings.TrimSpace(v)
	if kind == "html" {
		v = reScript.ReplaceAllString(v, "")
		v = reOnAttr.ReplaceAllString(v, "")
		v = reJSURL.ReplaceAllString(v, `$1="#`)
		return v
	}
	v = reAnyTag.ReplaceAllString(v, "")
	v = reMarkdown.ReplaceAllString(v, "$2")
	if kind == "short" {
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

// FromBrief fills the fields that come straight from the brief. It returns how many it set.
func FromBrief(fields []Field, b Brief, values map[string]string) int {
	n := 0
	for _, f := range fields {
		var v string
		switch f.From {
		case "site_name":
			v = b.SiteName
		case "email":
			v = b.Email
		case "phone":
			v = b.Phone
		case "address":
			v = b.Address
		}
		if v = strings.TrimSpace(v); v != "" {
			values[f.Path] = v
			n++
		}
	}
	return n
}

// Progress reports generation progress (batch i of n).
type Progress func(done, total int)

// Generate asks the model for new texts for every AI field and returns them by path.
func Generate(ctx context.Context, c Chatter, demo *Demo, b Brief, progress Progress) (map[string]string, int, error) {
	var fields []Field
	for _, f := range demo.Fields {
		if f.AI {
			fields = append(fields, f)
		}
	}
	values := map[string]string{}
	FromBrief(demo.Fields, b, values)
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
			s = Clean(f.Kind, s)
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
