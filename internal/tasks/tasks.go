// Package tasks lists work that is planned but not runnable yet. Runnable tasks are workflows
// (internal/engine/workflows/*.yaml).
package tasks

// Task describes a planned task.
type Task struct {
	ID          string
	Title       string
	Description string
	Link        string // panel page that starts it; empty = coming soon
	Order       int    // place among the workflows (their "order:")
}

// Catalogue is shown on the dashboard after the workflows: tasks with a Link open that page,
// the rest are "coming soon".
var Catalogue = []Task{
	{ID: "ai-customize", Title: "شخصی‌سازی با هوش مصنوعی", Description: "درباره‌ی مشتری بنویسید؛ بریف و همه‌ی متن‌های دمو با هوش مصنوعی برای او نوشته می‌شود و لوگو و تصاویرش جای دمو می‌نشیند. مرحله‌ی قبل از نصب قالب؛ پیش از نصب قالب", Link: "customize", Order: 35},
	{ID: "wp-list", Title: "وردپرس‌های هاست", Description: "همه‌ی وردپرس‌های روی هاست (دامنه‌ها، ساب‌دامین‌ها و پوشه‌ها) را پیدا می‌کند تا وردپرس‌های تستی را از فهرست انتخاب و کامل پاک کنید.", Link: "wordpress", Order: 90},
}
