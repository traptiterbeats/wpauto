# قرارداد شخصی‌سازی wpauto (نسخه‌ی ۱ و ۲)

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
`site.tagline` · `hero.title` · `hero.subtitle` · `cta.primary` · `cta.secondary` · `about.title` · `about.text` · `services.title` · `services.text` · `services.item.title` · `services.item.text` · `portfolio.title` · `portfolio.text` · `portfolio.item.title` · `portfolio.item.summary` · `portfolio.item.content` · `blog.title` · `blog.post.title` · `blog.post.summary` · `blog.post.content` · `contact.title` · `contact.text` · `menu.item` · `footer.text` · `testimonial.item.text` · `faq.item.question` · `faq.item.answer` · `product.item.title` · `product.item.description` · `product.item.short` (نسخه‌ی ۲: فقط در حالت «هوش مصنوعی محصولات نمونه بسازد»)

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

## نسخه‌ی ۲: تصاویر و محصولات

قالبی که `"version": 2` دارد، برای هر جای تصویر اندازه و نسبت اعلام می‌کند و می‌تواند بگوید محصول دارد. همه‌ی قاعده‌های نسخه‌ی ۱ (فیلدها، نقش‌ها، لوگو) همان است؛ قالب‌های نسخه‌ی ۱ بدون تغییر کار می‌کنند (عکسشان بدون برش و همان‌طور که بارگذاری شده جای تصویر دمو می‌نشیند).

```json
{
  "contract": "wpauto-customize",
  "version": 2,
  "theme": "my-shop",
  "supports": ["site", "brand.logo", "color.primary", "hero", "about", "services", "products"],
  "products": {"post_type": "product", "default_mode": "keep"},
  "logo": {"theme_mod": "custom_logo", "label": "لوگو"},
  "images": [
    {"id": "hero-bg",   "label": "پس‌زمینه‌ی بالا", "ratio": "16:9", "width": 1920, "height": 1080, "priority": 1},
    {"id": "about-pic", "label": "عکس درباره",     "ratio": "4:3",  "width": 1200, "height": 900,  "priority": 2},
    {"id": "team-1",    "label": "عکس تیم",        "ratio": "1:1",  "width": 800,  "height": 800,  "priority": 3},
    {"id": "side-pic",  "label": "عکس کناری",      "ratio": "3:4",  "width": 900,  "height": 1200, "priority": 4},
    {"id": "prod-mug",  "label": "عکس ماگ",        "ratio": "1:1",  "width": 1000, "height": 1000, "priority": 10, "group": "products"},
    {"id": "prod-tee",  "label": "عکس تی‌شرت",     "ratio": "1:1",  "width": 1000, "height": 1000, "priority": 11, "group": "products"}
  ],
  "fields": [
    {"path": "options.blogname", "label": "نام سایت", "role": "site.name"},
    {"path": "theme_mods.ms_hero_title", "label": "تیتر", "role": "hero.title", "hint": "حداکثر ۸ کلمه"},
    {"path": "products[slug=mug].name", "label": "نام ماگ", "role": "product.item.title"},
    {"path": "products[slug=mug].short_description", "label": "توضیح کوتاه ماگ", "role": "product.item.short"},
    {"path": "products[slug=mug].description", "label": "توضیح ماگ", "role": "product.item.description", "kind": "html"},
    {"path": "products[slug=mug].regular_price", "label": "قیمت ماگ", "role": "product.item.price"},
    {"path": "products[slug=mug].sku", "label": "کد کالای ماگ", "role": "product.item.sku"}
  ]
}
```

### `images` (جاهای تصویر)

- `id`: **همان `id` آیتم در `images[]` فایل `demo.json`** که این جا جایش را می‌گیرد (کلید پیدا کردن تصویر دمو، مثل نسخه‌ی ۱). تصویری از demo.json که در این فهرست نباشد جای تصویر حساب نمی‌شود و دست نمی‌خورد.
- `label`: نامی که در پنل دیده می‌شود.
- `ratio`: فقط `16:9`، `4:3`، `1:1` یا `3:4`.
- `width` و `height`: اندازه‌ی خروجی به پیکسل (حداکثر ۴۰۰۰). اگر فقط یکی بیاید، دیگری از نسبت حساب می‌شود؛ اگر هیچ‌کدام نیاید: 16:9 → 1920×1080، 4:3 → 1600×1200، 1:1 → 1200×1200، 3:4 → 1200×1600. اگر اندازه با نسبت نخواند، اندازه ملاک است و هشدار داده می‌شود.
- `priority`: عدد کوچک‌تر یعنی مهم‌تر؛ جای مهم‌تر زودتر پر می‌شود (پیش‌فرض ۱۰۰، و در اولویت برابر ترتیب فهرست).
- `group`: `site` (پیش‌فرض) یا `products` برای عکس محصولات.

مقدار نادرست (نسبت غیراستاندارد، گروه ناشناخته) با مقدار امن جایگزین می‌شود و هشدارش در صفحه‌ی «لوگو و تصاویر» پیش‌نویس می‌آید.

**چیدن عکس‌ها:** کاربر هر تعداد عکس با هر اندازه بارگذاری می‌کند (png، jpg، webp، gif؛ هر عکس حداکثر ۴۰ مگابایت و ۵۰ مگاپیکسل). هر عکس به ترتیب بارگذاری به جای خالی‌ای می‌رود که نسبتش به نسبت عکس نزدیک‌تر است و بین جاهای هم‌نسبت، به جایی که `priority` کوچک‌تری دارد. عکس از وسط برش می‌خورد، به `width`×`height` درمی‌آید و WebP (کیفیت ۸۵) ذخیره می‌شود؛ جهت عکس گوشی (EXIF) درست می‌شود. جایی که عکس نگیرد تصویر دمو را نگه می‌دارد؛ دو عکس هم کافی است. کاربر در پیش‌نویس می‌تواند برای هر جا عکس دیگری یا «تصویر دمو» را دستی انتخاب کند و پیش‌نمایش برش را ببیند؛ چیدن خودکار به انتخاب دستی دست نمی‌زند. عکسی که از جایش کوچک‌تر است بزرگ می‌شود و هشدار می‌گیرد.

### `products` (محصولات)

`"products": {"post_type": "product", "default_mode": "keep"}` یعنی دموی قالب محصول دارد. در هر اجرا (پیش‌نویس یا AutoPilot) یکی انتخاب می‌شود؛ پیش‌فرض همان `default_mode` است (`keep` یا `ai`):

- **«محصولات دمو بمانند» (`keep`)**: فیلدهای `product.*` اصلاً عوض نمی‌شوند (نه هوش مصنوعی می‌نویسد، نه پاک می‌شوند) و جاهای تصویر گروه `products` عکس نمی‌گیرند.
- **«هوش مصنوعی محصولات نمونه بسازد» (`ai`)**: نام (`product.item.title`)، توضیح کوتاه (`product.item.short`) و توضیح (`product.item.description`) برای کسب‌وکار مشتری نوشته می‌شود و جاهای تصویر محصول هم عکس می‌گیرند. قیمت، کد کالا و هر نقش دیگر `product.*` مثل دمو می‌ماند، مگر قالب برای آن فیلد `"ai": true` بگذارد. محصولی که عکس نگیرد تصویر دمو را نگه می‌دارد و پنل هشدار می‌دهد که عکس دمو ممکن است با متن تازه‌ی محصول جور نباشد.

نقش‌های محصول: `product.item.title` · `product.item.short` · `product.item.description` (نوشته‌ی هوش مصنوعی در حالت `ai`)، و هر `product.item.*` دیگر (مثل `product.item.price`، `product.item.sku`) که دست نمی‌خورد.

کاتالوگ قالب‌ها تعداد جاهای تصویر، نسخه‌ی قرارداد و داشتن محصول را نشان می‌دهد.

**نکته برای سازنده‌ی قالب:** فایل خروجی WebP است (`images/client-<id>.webp` در بسته‌ی دمو)؛ وردپرس ۵.۸ به بعد WebP را می‌پذیرد، ولی برای ساختن اندازه‌های کوچک‌تر، GD یا Imagick هاست باید WebP را پشتیبانی کند. قالب باید تصویر را با اندازه‌ای که اعلام کرده (یا `object-fit: cover`) نشان دهد.
