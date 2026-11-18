# wpauto

پنل نصب خودکار وردپرس روی هاست‌های cPanel، نوشته‌شده با Go.

## نصب (Ubuntu 22.04 / 24.04، با دسترسی root)

```bash
bash <(curl -Ls https://raw.githubusercontent.com/traptiterbeats/wpauto/main/install.sh)
```

نصب‌کننده پورت پنل را می‌پرسد، نام کاربری و رمز تصادفی می‌سازد و آدرس پنل را نشان می‌دهد.

## مدیریت

```bash
wpauto            # منوی مدیریت
wpauto info       # آدرس پنل و وضعیت
wpauto update     # نصب آخرین نسخه
```

منو شامل: تغییر نام کاربری و رمز، تغییر پورت، SSL (Let's Encrypt یا self-signed)، استارت/استاپ/ری‌استارت، لاگ‌ها، آپدیت و حذف.

## پروفایل هاست

هر خانواده از هاست‌های cPanel یک فایل YAML کوچک دارد (پورت، روش ورود، نسخه‌ی PHP پیشنهادی، بررسی‌ها و ویژگی‌های خاص). موتور برای همه یکی است.
پروفایل‌های داخلی: `generic` و `autopilo`. برای هاست جدید، [generic.yaml](internal/profile/builtin/generic.yaml) را در `/etc/wpauto/profiles/<id>.yaml` کپی و ویرایش کنید؛ بدون ری‌استارت در فرم افزودن هاست ظاهر می‌شود.

رمز هاست‌ها با AES-256-GCM در `/etc/wpauto/hosts.json` ذخیره می‌شود و کلیدش در `/etc/wpauto/secret.key` است. بعد از یک ورود ناموفق، تلاش خودکار متوقف می‌شود تا فایروال هاست (CSF/cPHulk) IP سرور را بلاک نکند.

## کارها (Workflow)

هر کار یک فایل YAML است: ورودی‌های فرم و مراحل پشت‌سرهم. هر مرحله یکی از «نوع مرحله»های داخلی را اجرا می‌کند و خروجی‌اش با `{{.Steps.<id>.<key>}}` در مراحل بعد قابل استفاده است. ورودی‌ها با `{{.Inputs.<name>}}`.

```yaml
id: wp-check-domain
title: بررسی وردپرس یک دامنه
inputs:
  - {name: domain, label: دامنه, type: domain, required: true}
steps:
  - {id: login,  title: ورود,  uses: cpanel.login}
  - {id: detect, title: بررسی, uses: wp.detect, with: {domain: "{{.Inputs.domain}}"}}
```

نوع‌های مرحله فعلی: `cpanel.login`، `cpanel.preflight`، `cpanel.uapi` (فقط فراخوانی‌های خواندنی)، `wp.detect`، `log`.
کارهای سفارشی را در `/etc/wpauto/workflows/*.yaml` بگذارید. هر هاست هم‌زمان فقط یک کار اجرا می‌کند، لاگ زنده در مرورگر دیده می‌شود و تاریخچه در `/etc/wpauto/runs` می‌ماند.

## ساخت نسخه

با هر تگ `v*` (مثلاً `v0.1.0`)، GitHub Actions با GoReleaser باینری‌های `linux/amd64` و `linux/arm64` و فایل `checksums.txt` را در Releases منتشر می‌کند.

## نقشه‌ی راه

- [x] فاز ۱: باینری، پنل وب با ورود امن، منوی ترمینال، نصب یک‌خطی، انتشار خودکار
- [x] فاز ۲: اتصال به cPanel با پروفایل هاست (نشست یا API Token، UAPI) و گزارش امکانات هاست
- [x] فاز ۳: موتور تسک‌ها (YAML)، لاگ زنده، تاریخچه، تشخیص وردپرس نصب‌شده
- [ ] فاز ۴: نصب وردپرس و تست خودکار
- [ ] فاز ۵: کاتالوگ قالب و افزونه
- [ ] فاز ۶: شخصی‌سازی با هوش مصنوعی
