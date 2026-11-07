package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var assets embed.FS

// Handler serves the embedded frontend without relying on the working directory.
func Handler() http.Handler {
	static, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	files := http.FileServer(http.FS(static))
	mux.Handle("GET /{$}", files)
	mux.Handle("GET /app.css", files)
	mux.Handle("GET /app.js", files)
	return mux
}
