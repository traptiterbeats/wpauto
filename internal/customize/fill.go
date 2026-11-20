package customize

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/traptiterbeats/wpauto/internal/ai"
)

const fillSystem = `You prepare the brief for a client's website. The user describes the client and what they want, in free words.
Return ONE JSON object with these keys (all strings unless noted):
site_name, brand_latin (the brand name in Latin letters, short), business (field of work, one line), about (2-4 sentences introducing the business),
services (array of strings, one service or product each), audience, tone (a few words), email, phone, address, hours,
socials (object; keys only from: %s), color_primary and color_secondary (hex like #1a2b3c, colours that fit the brand), notes (anything else the copywriter should respect, e.g. things to avoid or highlight), language.
Rules:
- email, phone, address, hours and socials: ONLY copy what the description states. Never invent contact details; leave them "" (or out of socials).
- Never invent prices, awards, numbers, years or client names. Other fields may be written from the description in fluent wording.
- language: the language the website should be written in (default: the language of the description).
- Write in the language of the site (for Persian: correct half-spaces).
- No other keys, no comments.`

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// FillBrief asks the AI to fill the brief from the free description in b.Story. Empty fields are
// filled; with overwrite every field the AI returned replaces the current one. Contact details are
// only taken when the description states them.
func FillBrief(ctx context.Context, c Chatter, b Brief, overwrite bool) (Brief, ai.Usage, error) {
	story := strings.TrimSpace(b.Story)
	if story == "" {
		return b, ai.Usage{}, errors.New("اول درباره‌ی مشتری و خواسته‌هایش بنویسید")
	}
	ids := make([]string, len(Socials))
	for i, s := range Socials {
		ids[i] = s.ID
	}
	user := "Client description:\n" + story
	nb := b
	nb.Story = ""
	if cur := briefText(nb); cur != "" {
		user += "\n\nAlready filled by the user (keep consistent):\n" + cur
	}
	out, u, err := c.Chat(ctx, fmt.Sprintf(fillSystem, strings.Join(ids, ", ")), user, true)
	if err != nil {
		return b, u, err
	}
	m, err := ai.JSONObject(out)
	if err != nil {
		return b, u, err
	}
	str := func(k string) string {
		switch v := m[k].(type) {
		case string:
			return strings.TrimSpace(v)
		case []any:
			var lines []string
			for _, x := range v {
				if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
					lines = append(lines, strings.TrimSpace(s))
				}
			}
			return strings.Join(lines, "\n")
		}
		return ""
	}
	set := func(dst *string, k string) {
		if v := str(k); v != "" && (overwrite || strings.TrimSpace(*dst) == "") {
			*dst = v
		}
	}
	set(&b.SiteName, "site_name")
	set(&b.BrandLatin, "brand_latin")
	set(&b.Business, "business")
	set(&b.About, "about")
	set(&b.Services, "services")
	set(&b.Audience, "audience")
	set(&b.Tone, "tone")
	set(&b.Notes, "notes")
	set(&b.Language, "language")
	// contact details only when the description really contains them
	inStory := func(v string) bool {
		norm := func(s string) string {
			return strings.ToLower(strings.NewReplacer(" ", "", "-", "", "@", "", "https://", "", "http://", "").Replace(s))
		}
		return v != "" && strings.Contains(norm(story), norm(v))
	}
	for _, f := range []struct {
		dst *string
		k   string
	}{{&b.Email, "email"}, {&b.Phone, "phone"}} {
		if v := str(f.k); inStory(v) && (overwrite || *f.dst == "") {
			*f.dst = v
		}
	}
	set(&b.Address, "address")
	set(&b.Hours, "hours")
	if so, ok := m["socials"].(map[string]any); ok {
		if b.Socials == nil {
			b.Socials = map[string]string{}
		}
		for _, id := range ids {
			v, _ := so[id].(string)
			v = strings.TrimSpace(v)
			if inStory(strings.TrimPrefix(v, "@")) && (overwrite || b.Socials[id] == "") {
				b.Socials[id] = v
			}
		}
	}
	if p, s := str("color_primary"), str("color_secondary"); hexColor.MatchString(p) && (overwrite || b.ColorPrimary == "") {
		b.ColorPrimary = p
		if hexColor.MatchString(s) {
			b.ColorSecondary = s
		}
	}
	return b, u, nil
}
