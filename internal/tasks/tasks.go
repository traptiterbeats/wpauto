// Package tasks holds the registry of jobs the panel can run against a cPanel host.
// Phase 1 only lists them; the workflow engine arrives in Phase 3.
package tasks

// Status of a task in the catalogue.
type Status string

const (
	StatusReady Status = "ready"
	StatusSoon  Status = "soon"
)

// Task describes one job in the panel's task list.
type Task struct {
	ID          string
	Title       string
	Description string
	Status      Status
	Link        string
}

// Catalogue is the ordered list shown on the dashboard.
var Catalogue = []Task{
	{ID: "cpanel-connect", Title: "اتصال به cPanel", Description: "افزودن هاست با پروفایل، ذخیره‌ی رمزنگاری‌شده و تست ورود", Status: StatusReady, Link: "hosts"},
	{ID: "host-preflight", Title: "بررسی هاست", Description: "تشخیص امکانات، نسخه‌ی PHP، دیتابیس و فضای دیسک", Status: StatusReady, Link: "hosts"},
	{ID: "wp-install", Title: "نصب وردپرس", Description: "ساخت دیتابیس، نصب فایل‌ها، تنظیم ادمین و تست", Status: StatusSoon},
	{ID: "theme-install", Title: "نصب قالب و افزونه‌ها", Description: "انتخاب قالب از کاتالوگ و نصب افزونه‌های وابسته", Status: StatusSoon},
	{ID: "ai-customize", Title: "شخصی‌سازی با هوش مصنوعی", Description: "پر کردن محتوای قالب از روی اطلاعات کسب‌وکار", Status: StatusSoon},
}
