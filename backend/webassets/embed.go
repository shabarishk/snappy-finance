package webassets

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed dist/*
var content embed.FS

func Handler() http.Handler {
	staticFiles, err := fs.Sub(content, "dist")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(staticFiles))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}

		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name != "." {
			if info, err := fs.Stat(staticFiles, name); err == nil && !info.IsDir() {
				fileServer.ServeHTTP(w, r)
				return
			}
		}

		indexRequest := r.Clone(r.Context())
		urlCopy := *r.URL
		urlCopy.Path = "/"
		indexRequest.URL = &urlCopy
		fileServer.ServeHTTP(w, indexRequest)
	})
}
