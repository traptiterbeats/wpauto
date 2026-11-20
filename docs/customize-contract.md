# قرارداد شخصی‌سازی wpauto (نسخه‌ی ۱)

هدف: یک بریف مشتری، روی هر قالبی که این قرارداد را رعایت کند، درست بنشیند. هر قالب می‌تواند بخشی از نقش‌ها را داشته باشد؛ wpauto فقط همان‌هایی را پر می‌کند که قالب اعلام کرده.

## چیزهایی که قالب باید داشته باشد

1. **همه‌ی متن‌های ثابت صفحه در Customizer** (theme_mod) با مقدار پیش‌فرض؛ نه متن سخت‌کدشده در قالب.
2. **همه‌ی آن theme_modها در `demo.json`** تا ساایه آن‌ها را بنویسد و wpauto بتواند عوضشان کند.
3. **لوگو** با `add_theme_support( 'custom-logo' )` (یا یک theme_mod تصویری که در customize.json معرفی شود).
4. **رنگ اصلی** به‌صورت یک theme_mod هگز که قالب آن را به متغیر CSS تبدیل کند.
5. **خالی یعنی پنهان**: اگر ایمیل، تلفن یا لینک شبکه‌ی اجتماعی خالی بود، قالب آن را نشان ندهد. wpauto مقدارهای نمونه‌ی دمو را که مشتری نداده پاک می‌کند تا ایمیل و لینک نمونه روی سایت مشتری نماند.
6. **فایل `customize.json`** کنار `demo.json` در بسته‌ی دمو.

## customize.json

```json
{
  "contract": "wpauto-customize",
  "version": 1,
  "theme": "my-theme",
  "supports": ["site", "brand.logo", "color.primary", "contact.email", "social", "hero", "about", "services"],
  "sections": [{"id": "hero", "label": "بخش معرفی"}],
  "logo": {"theme_mod": "custom_logo", "label": "لوگو"},
  "images": [{"id": "hero-bg", "label": "تصویر پس‌زمینه‌ی بالا"}],
  "fields": [
    {"path": "options.blogname", "label": "نام سایت", "role": "site.name"},
    {"path": "theme_mods.mt_hero_title", "label": "تیتر", "section": "hero", "role": "hero.title",
     "kind": "inline", "allow": ["br", "span.grad"], "hint": "حداکثر ۸ کلمه"},
    {"path": "theme_mods.mt_email", "label": "ایمیل", "section": "contact", "role": "contact.email"},
    {"path": "posts[slug=web-design].title", "label": "عنوان خدمت", "role": "services.item.title"}
  ]
}
```

- `path`: جای متن در demo.json. `posts[slug=x]` آیتم را با نامک پیدا می‌کند (بهتر از شماره).
- `kind`: `short` (یک خط)، `text` (چندخطی)، `html` (محتوای نوشته)، `inline` (HTML محدود با `allow`)، `email`، `url`، `color`.
- `hint`: دستور سازنده‌ی قالب برای هوش مصنوعی (طول، لحن، شکل).
- `ai: false`: هوش مصنوعی به آن دست نزند (فقط دستی).
- `keep: true`: اگر بریف مقدار نداشت، مقدار دمو بماند (به‌جای پاک شدن).
- فیلدهایی که در `fields` نیستند تغییر نمی‌کنند، مگر `"auto": true`.

## نقش‌ها

**از بریف (بدون هوش مصنوعی):**
`site.name` · `brand.latin` (اگر خالی بود هوش مصنوعی می‌نویسد) · `contact.email` · `contact.phone` · `contact.address` · `contact.hours` · `social.instagram` · `social.telegram` · `social.whatsapp` · `social.linkedin` · `social.x` · `social.youtube` · `social.aparat` · `social.github` · `social.website` · `color.primary` · `color.secondary`

**نوشته‌ی هوش مصنوعی:**
`site.tagline` · `hero.title` · `hero.subtitle` · `cta.primary` · `cta.secondary` · `about.title` · `about.text` · `services.title` · `services.text` · `services.item.title` · `services.item.text` · `portfolio.title` · `portfolio.text` · `portfolio.item.title` · `portfolio.item.summary` · `portfolio.item.content` · `blog.title` · `blog.post.title` · `blog.post.summary` · `blog.post.content` · `contact.title` · `contact.text` · `menu.item` · `footer.text` · `testimonial.item.text` · `faq.item.question` · `faq.item.answer` · `product.item.title` · `product.item.description`

نقش‌هایی که چند بار تکرار می‌شوند (مثل `services.item.title`) فهرست حساب می‌شوند و به ترتیب از خدمات بریف پر می‌شوند. فیلدی بدون نقش هم پذیرفته است؛ هوش مصنوعی آن را با `label` و `hint` و متن دمو می‌فهمد.

## بدون customize.json

wpauto متن‌ها را خودکار از demo.json پیدا می‌کند (عنوان و متن برگه‌ها و نوشته‌ها، متای متنی، منو، theme_modهای متنی، ایمیل و لینک شبکه‌های اجتماعی از روی نام کلید). برای کیفیت بهتر customize.json بگذارید.

## Required core roles (standard check)

The panel marks a theme "استاندارد شخصی‌سازی" only when its demo has `customize.json` with
`"contract": "wpauto-customize"` and fields for all of these roles: `site.name`, `hero.title`,
`hero.subtitle`, `about.text` (or another `about.*` text), `services.item.title`, `contact.title`.
Every visible text of the theme must be a Customizer theme_mod (or post/menu content) listed in
`customize.json`; a text hard-coded in a template can never follow the client's brief, so a
carpentry site would keep the demo's software wording. Themes that fail the check are flagged in
«قالب‌ها», in the new-draft form and on the draft page.
