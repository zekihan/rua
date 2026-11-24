// Package server serves the read-only /api, POST /api/fetch, /healthz and
// the embedded SPA, with strict security headers.
package server

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/zekihan/go-commons/metrics"

	"github.com/zekihan/rua/internal/imapsrc"
	"github.com/zekihan/rua/internal/query"
)

// Fetcher is the part of *imapsrc.Fetcher the server uses.
type Fetcher interface {
	Trigger() error
	State() imapsrc.State
}

type Options struct {
	Query   *query.Service
	Fetcher Fetcher
	Web     fs.FS
	Ping    func(*http.Request) error
}

type Server struct {
	opts Options
}

func New(opts Options) *Server {
	return &Server{opts: opts}
}

const contentSecurityPolicy = "default-src 'self'; img-src 'self' data:; " +
	"style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// Handler returns the root handler.
func (srv *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", srv.handleHealth)
	mux.HandleFunc("GET /api/domains", srv.handleDomains)
	mux.HandleFunc("GET /api/overview", srv.handleOverview)
	mux.HandleFunc("GET /api/sources", srv.handleSources)
	mux.HandleFunc("GET /api/sources/{key}", srv.handleSource)
	mux.HandleFunc("GET /api/reports", srv.handleReports)
	mux.HandleFunc("GET /api/reports/{id}", srv.handleReport)
	mux.HandleFunc("GET /api/reports/{id}/raw", srv.handleReportRaw)
	mux.HandleFunc("GET /api/messages/{id}/raw", srv.handleMessageRaw)
	mux.HandleFunc("GET /api/tls", srv.handleTLS)
	mux.HandleFunc("GET /api/tls/{domain}", srv.handleTLSDomain)
	mux.HandleFunc("GET /api/status", srv.handleStatus)
	mux.HandleFunc("POST /api/fetch", srv.handleFetch)
	mux.HandleFunc("/", srv.serveFallback)

	handler := metrics.CapturePattern(mux)

	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Referrer-Policy", "no-referrer")

		if request.URL.Path == "/api" || strings.HasPrefix(request.URL.Path, "/api/") {
			writer.Header().Set("Cache-Control", "no-store")
		}

		handler.ServeHTTP(writer, request)
	})
}

func (srv *Server) handleHealth(writer http.ResponseWriter, request *http.Request) {
	if srv.opts.Ping != nil && srv.opts.Ping(request) != nil {
		http.Error(writer, "unavailable", http.StatusServiceUnavailable)

		return
	}

	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")

	_, err := writer.Write([]byte("ok"))
	if err != nil {
		return
	}
}

func (srv *Server) handleAPINotFound(writer http.ResponseWriter, _ *http.Request) {
	writeError(writer, http.StatusNotFound, "not_found", "API route not found.")
}

func (srv *Server) serveFallback(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/api" || strings.HasPrefix(request.URL.Path, "/api/") {
		srv.handleAPINotFound(writer, request)

		return
	}

	if request.Method != http.MethodGet {
		http.NotFound(writer, request)

		return
	}

	srv.serveWeb(writer, request)
}
