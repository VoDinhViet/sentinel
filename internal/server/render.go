package server

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"sentinel/internal/api"
	"sentinel/internal/server/service"
)

//go:embed web/templates/*.html web/static/*
var webFS embed.FS

var pages = []string{"login.html", "manage.html", "agent.html", "agent_new.html", "agent_token.html", "config.html"}

var templates = func() map[string]*template.Template {
	out := map[string]*template.Template{}
	for _, p := range pages {
		out[p] = template.Must(template.New(p).Funcs(funcs).
			ParseFS(webFS, "web/templates/layout.html", "web/templates/"+p))
	}
	return out
}()

func staticFS() fs.FS {
	sub, _ := fs.Sub(webFS, "web/static")
	return sub
}

var funcs = template.FuncMap{
	"ago": func(t time.Time) string {
		if t.IsZero() {
			return "never"
		}
		d := time.Since(t)
		switch {
		case d < 0:
			return "just now"
		case d < time.Minute:
			return fmt.Sprintf("%ds ago", int(d.Seconds()))
		case d < time.Hour:
			return fmt.Sprintf("%dm ago", int(d.Minutes()))
		case d < 48*time.Hour:
			return fmt.Sprintf("%dh ago", int(d.Hours()))
		}
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	},
	"bytes": humanBytes,
	"meter": func(label string, v float64) map[string]any {
		return map[string]any{"Label": label, "Value": v}
	},
	"commandSummary": commandSummary,
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// commandSummary turns a finished command's output into one line.
func commandSummary(c service.CommandRecord) string {
	if !c.Done() || len(c.Output) == 0 {
		return ""
	}
	switch c.Type {
	case api.CmdRestore:
		var o api.RestoreOutput
		if json.Unmarshal(c.Output, &o) == nil {
			return "restored to " + o.Path
		}
	case api.CmdSnapshots:
		var snaps []api.Snapshot
		if json.Unmarshal(c.Output, &snaps) == nil {
			return fmt.Sprintf("%d snapshots", len(snaps))
		}
	case api.CmdBackupRun:
		var o api.BackupRunOutput
		if json.Unmarshal(c.Output, &o) == nil {
			return o.Summary
		}
	}
	return ""
}

type pageData struct {
	CSRF      string
	Version   string
	Flash     string
	FlashKind string
	Data      any
}

// render executes a full page.
func (s *Server) render(w http.ResponseWriter, r *http.Request, page string, data any) {
	s.execute(w, r, page, "layout", pageData{CSRF: csrfFrom(r), Version: s.Version, Data: data})
}

// renderPartial executes one named block of a page, for htmx swaps.
func (s *Server) renderPartial(w http.ResponseWriter, r *http.Request, page, block string, data any) {
	s.execute(w, r, page, block, data)
}

func (s *Server) execute(w http.ResponseWriter, r *http.Request, page, name string, data any) {
	t, ok := templates[page]
	if !ok {
		s.internalError(w, r, fmt.Errorf("unknown page %q", page))
		return
	}
	// Render to a buffer so a template error never sends half a page.
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		slog.Error("render", "page", page, "block", name, "error", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-store")
	}
	buf.WriteTo(w)
}
