package app

import (
	"embed"
	"html/template"
	"io"
)

//go:embed templates/*
var templatesFS embed.FS

var (
	dashboardTmpl *template.Template
)

func init() {
	// Инициализация шаблонов с использованием ParseFS и glob-паттернов
	dashboardTmpl = template.Must(template.ParseFS(templatesFS, 
		"templates/dashboard.html",
		"templates/partials/*.html",
	))
}

func (a *App) renderTemplate(w io.Writer, name string, data interface{}) error {
	target := name
	switch name {
	case "dashboard":
		target = "dashboard.html"
	case "channel-list":
		target = "channel_list.html"
	case "channel-card":
		target = "channel_card.html"
	case "feed-list":
		target = "feed_list.html"
	case "ai-limits":
		target = "ai_limits.html"
	case "cron-preview":
		target = "cron_preview.html"
	}

	return dashboardTmpl.ExecuteTemplate(w, target, data)
}
