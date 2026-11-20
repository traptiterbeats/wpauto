// Package tasks lists work that is planned but not runnable yet. Runnable tasks are workflows
// (internal/engine/workflows/*.yaml).
package tasks

// Task describes a planned task.
type Task struct {
	ID          string
	Title       string
	Description string
	Link        string // panel page that starts it; empty = coming soon
}

// Catalogue is shown on the dashboard after the workflows: tasks with a Link open that page,
// the rest are "coming soon".
var Catalogue = []Task{
	{ID: "ai-customize", Title: "شخصی‌سازی با هوش مصنوعی", Description: "متن‌ها، لوگو و تصاویر دمو از روی اطلاعات کسب‌وکار، پیش از درون‌ریزی", Link: "customize"},
}
