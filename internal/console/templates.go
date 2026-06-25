// internal/console/templates.go
package console

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// parsePage parses layout.html with one page template so {{template "content"}}
// resolves to that page. Returned templates are rendered via the "layout" name.
func parsePage(page string) (*template.Template, error) {
	return template.ParseFS(templateFS, "templates/layout.html", "templates/"+page)
}

func render(w io.Writer, t *template.Template, data any) error {
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		return fmt.Errorf("render template: %w", err)
	}
	return nil
}

func mustStaticSub() fs.FS {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err) // embedded FS is compile-time constant; this never fails
	}
	return sub
}
