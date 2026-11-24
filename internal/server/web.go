package server

import (
	"bytes"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

const (
	indexFile = "index.html"
	noCache   = "no-cache"
)

func (srv *Server) serveWeb(writer http.ResponseWriter, request *http.Request) {
	name := strings.TrimPrefix(request.URL.Path, "/")
	if name == "api" || strings.HasPrefix(name, "api/") || name == "healthz" {
		http.NotFound(writer, request)

		return
	}

	web := srv.opts.Web
	if web != nil && name != indexFile && fs.ValidPath(name) &&
		serveStatic(writer, request, web, name) {
		return
	}

	extension := path.Ext(strings.TrimRight(name, "/"))
	isAsset := name == "assets" || strings.HasPrefix(name, "assets/")

	if name != indexFile && (isAsset || extension != "") {
		http.NotFound(writer, request)

		return
	}

	serveIndex(writer, request, web, name)
}

func serveIndex(writer http.ResponseWriter, request *http.Request, web fs.FS, name string) {
	writer.Header().Set("Cache-Control", noCache)

	if web != nil {
		index, err := fs.ReadFile(web, indexFile)
		if err == nil {
			http.ServeContent(writer, request, indexFile, time.Time{}, bytes.NewReader(index))

			return
		}
	}

	if name == indexFile {
		http.NotFound(writer, request)

		return
	}

	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(writer, "Frontend unavailable. Run make web.")
}

func serveStatic(writer http.ResponseWriter, request *http.Request, web fs.FS, name string) bool {
	info, err := fs.Stat(web, name)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}

	switch {
	case name == "sw.js":
		writer.Header().Set("Cache-Control", noCache)
	case strings.HasSuffix(name, ".webmanifest"):
		writer.Header().Set("Cache-Control", noCache)
		writer.Header().Set("Content-Type", "application/manifest+json")
	case strings.HasPrefix(name, "assets/"):
		writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}

	//nolint:gosec // ValidPath restricts name to the embedded filesystem.
	http.ServeFileFS(writer, request, web, name)

	return true
}
