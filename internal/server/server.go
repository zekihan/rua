// Package server serves the read-only /api, POST /api/fetch, /healthz and
// the embedded SPA, with strict security headers.
package server

import (
	"io/fs"
	"net/http"

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

// Handler returns the root handler.
func (srv *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if srv.opts.Ping != nil && srv.opts.Ping(r) != nil {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)

			return
		}

		_, _ = w.Write([]byte("ok"))
	})

	return mux
}
