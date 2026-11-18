// Package tasks lists work that is planned but not runnable yet. Runnable tasks are workflows
// (internal/engine/workflows/*.yaml).
package tasks

// Task describes a planned task.
type Task struct {
	ID          string
	Title       string
	Description string
}

// Catalogue is shown on the dashboard as "coming soon".
var Catalogue = []Task{
	{ID: "wp-install", Title: "نصب وردپرس", Description: "روی ساب‌دامین یا فولدر جدا؛ ساخت دیتابیس، نصب فایل‌ها، ادمین و تست"},
	{ID: "theme-install", Title: "نصب قالب و افزونه‌ها", Description: "انتخاب قالب از کاتالوگ و نصب افزونه‌های وابسته"},
	{ID: "ai-customize", Title: "شخصی‌سازی با هوش مصنوعی", Description: "پر کردن محتوای قالب از روی اطلاعات کسب‌وکار"},
}
