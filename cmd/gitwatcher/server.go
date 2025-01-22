package main

import (
	"net/http"
	"strings"
	"log"
	"io/fs"
)

func setupStaticFileServer(r *http.ServeMux, fsys fs.FS) {
	fileServer := http.FileServer(http.FS(fsys))

	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		log.Printf("Requested path: %s", path)

		// Special handling for Next.js static files
		if strings.HasPrefix(path, "/_next/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000")
			if strings.HasSuffix(path, ".js") {
				w.Header().Set("Content-Type", "application/javascript")
			} else if strings.HasSuffix(path, ".css") {
				w.Header().Set("Content-Type", "text/css")
			}
			fileServer.ServeHTTP(w, r)
			return
		}

		// For all other paths, serve index.html
		content, err := fs.ReadFile(fsys, "index.html")
		if err != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write(content)
	})

	// Serve static files under /_next/
	r.Handle("/_next/", http.StripPrefix("/", fileServer))
}