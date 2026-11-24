package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/zekihan/rua/internal/imapsrc"
	"github.com/zekihan/rua/internal/query"
)

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)

	err := json.NewEncoder(writer).Encode(value)
	if err != nil {
		slog.Error("Encode response", slog.Any("error", err))
	}
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, query.ErrorBody{
		Error: query.ErrorDetail{Code: code, Message: message},
	})
}

func handleQueryError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, query.ErrBadRequest):
		writeError(writer, http.StatusBadRequest, "bad_request", err.Error())
	case errors.Is(err, query.ErrNotFound):
		writeError(writer, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, query.ErrUnavailable):
		writer.Header().Set("Retry-After", "60")
		writeError(writer, http.StatusServiceUnavailable, "unavailable", err.Error())
	default:
		slog.ErrorContext(request.Context(), "API request failed", slog.Any("error", err))
		writeError(
			writer,
			http.StatusInternalServerError,
			"internal",
			"The request could not be completed.",
		)
	}
}

func (srv *Server) parseFilter(
	writer http.ResponseWriter, request *http.Request,
) (query.Filter, bool) {
	var empty query.Filter

	if srv.opts.Query == nil {
		writeError(
			writer,
			http.StatusInternalServerError,
			"internal",
			"The request could not be completed.",
		)

		return empty, false
	}

	filter, err := srv.opts.Query.ParseFilter(request.URL.Query())
	if err != nil {
		handleQueryError(writer, request, err)

		return empty, false
	}

	return filter, true
}

func parseID(writer http.ResponseWriter, request *http.Request) (int64, bool) {
	raw := request.PathValue("id")

	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeError(writer, http.StatusBadRequest, "bad_request", "invalid id")

		return 0, false
	}

	return id, true
}

func (srv *Server) handleDomains(writer http.ResponseWriter, request *http.Request) {
	if srv.opts.Query == nil {
		writeError(
			writer,
			http.StatusInternalServerError,
			"internal",
			"The request could not be completed.",
		)

		return
	}

	value, err := srv.opts.Query.Domains(request.Context())
	if err != nil {
		handleQueryError(writer, request, err)

		return
	}

	writeJSON(writer, http.StatusOK, value)
}

func (srv *Server) handleOverview(writer http.ResponseWriter, request *http.Request) {
	filter, ok := srv.parseFilter(writer, request)
	if !ok {
		return
	}

	value, err := srv.opts.Query.Overview(request.Context(), filter)
	if err != nil {
		handleQueryError(writer, request, err)

		return
	}

	writeJSON(writer, http.StatusOK, value)
}

func (srv *Server) handleReporters(writer http.ResponseWriter, request *http.Request) {
	filter, ok := srv.parseFilter(writer, request)
	if !ok {
		return
	}

	value, err := srv.opts.Query.Reporters(request.Context(), filter)
	if err != nil {
		handleQueryError(writer, request, err)

		return
	}

	writeJSON(writer, http.StatusOK, value)
}

func (srv *Server) handleSources(writer http.ResponseWriter, request *http.Request) {
	filter, ok := srv.parseFilter(writer, request)
	if !ok {
		return
	}

	value, err := srv.opts.Query.Sources(request.Context(), filter)
	if err != nil {
		handleQueryError(writer, request, err)

		return
	}

	writeJSON(writer, http.StatusOK, value)
}

func (srv *Server) handleSource(writer http.ResponseWriter, request *http.Request) {
	filter, ok := srv.parseFilter(writer, request)
	if !ok {
		return
	}

	value, err := srv.opts.Query.Source(request.Context(), request.PathValue("key"), filter)
	if err != nil {
		handleQueryError(writer, request, err)

		return
	}

	writeJSON(writer, http.StatusOK, value)
}

func (srv *Server) handleReports(writer http.ResponseWriter, request *http.Request) {
	filter, ok := srv.parseFilter(writer, request)
	if !ok {
		return
	}

	value, err := srv.opts.Query.Reports(request.Context(), filter)
	if err != nil {
		handleQueryError(writer, request, err)

		return
	}

	writeJSON(writer, http.StatusOK, value)
}

func (srv *Server) handleReport(writer http.ResponseWriter, request *http.Request) {
	id, ok := parseID(writer, request)
	if !ok {
		return
	}

	filter, ok := srv.parseFilter(writer, request)
	if !ok {
		return
	}

	value, err := srv.opts.Query.Report(request.Context(), id, filter)
	if err != nil {
		handleQueryError(writer, request, err)

		return
	}

	writeJSON(writer, http.StatusOK, value)
}

func (srv *Server) handleReportRaw(writer http.ResponseWriter, request *http.Request) {
	id, ok := parseID(writer, request)
	if !ok {
		return
	}

	if srv.opts.Query == nil {
		writeError(
			writer,
			http.StatusInternalServerError,
			"internal",
			"The request could not be completed.",
		)

		return
	}

	data, filename, err := srv.opts.Query.ReportRaw(request.Context(), id)
	if err != nil {
		handleQueryError(writer, request, err)

		return
	}

	writeRaw(writer, data, filename)
}

func (srv *Server) handleMessageRaw(writer http.ResponseWriter, request *http.Request) {
	id, ok := parseID(writer, request)
	if !ok {
		return
	}

	if srv.opts.Query == nil {
		writeError(
			writer,
			http.StatusInternalServerError,
			"internal",
			"The request could not be completed.",
		)

		return
	}

	data, filename, err := srv.opts.Query.MessageRaw(request.Context(), id)
	if err != nil {
		handleQueryError(writer, request, err)

		return
	}

	writeRaw(writer, data, filename)
}

func writeRaw(writer http.ResponseWriter, data []byte, filename string) {
	writer.Header().Set("Content-Type", "application/octet-stream")
	writer.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	writer.Header().Set("Content-Length", strconv.Itoa(len(data)))
	writer.WriteHeader(http.StatusOK)

	//nolint:gosec // Raw downloads are served as application/octet-stream, not HTML.
	_, writeErr := writer.Write(data)
	if writeErr != nil {
		slog.Error("Write raw response", slog.Any("error", writeErr))
	}
}

func (srv *Server) handleTLS(writer http.ResponseWriter, request *http.Request) {
	filter, ok := srv.parseFilter(writer, request)
	if !ok {
		return
	}

	value, err := srv.opts.Query.TLS(request.Context(), filter)
	if err != nil {
		handleQueryError(writer, request, err)

		return
	}

	writeJSON(writer, http.StatusOK, value)
}

func (srv *Server) handleTLSDomain(writer http.ResponseWriter, request *http.Request) {
	filter, ok := srv.parseFilter(writer, request)
	if !ok {
		return
	}

	value, err := srv.opts.Query.TLSDomain(request.Context(), request.PathValue("domain"), filter)
	if err != nil {
		handleQueryError(writer, request, err)

		return
	}

	writeJSON(writer, http.StatusOK, value)
}

func (srv *Server) handleStatus(writer http.ResponseWriter, request *http.Request) {
	if srv.opts.Query == nil {
		writeError(
			writer,
			http.StatusInternalServerError,
			"internal",
			"The request could not be completed.",
		)

		return
	}

	value, err := srv.opts.Query.Status(request.Context())
	if err != nil {
		handleQueryError(writer, request, err)

		return
	}

	writeJSON(writer, http.StatusOK, value)
}

func (srv *Server) handleFetch(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Sec-Fetch-Site") != "same-origin" && request.Header.Get("X-Rua") != "1" {
		writeError(
			writer,
			http.StatusForbidden,
			"forbidden",
			"Fetch requires a same-origin request.",
		)

		return
	}

	if srv.opts.Fetcher == nil {
		writeError(
			writer,
			http.StatusServiceUnavailable,
			"fetch_disabled",
			"IMAP is not configured.",
		)

		return
	}

	err := srv.opts.Fetcher.Trigger()
	if err != nil {
		switch {
		case errors.Is(err, imapsrc.ErrRunning):
			writeError(writer, http.StatusConflict, "fetch_running", "A fetch is already running.")
		case errors.Is(err, imapsrc.ErrCooldown):
			if retry := retryAfter(srv.opts.Fetcher); retry > 0 {
				writer.Header().Set("Retry-After", strconv.FormatInt(retry, 10))
			}

			writeError(
				writer,
				http.StatusTooManyRequests,
				"fetch_cooldown",
				"Fetch was requested too recently.",
			)
		case errors.Is(err, imapsrc.ErrDisabled):
			writeError(
				writer,
				http.StatusServiceUnavailable,
				"fetch_disabled",
				"IMAP is not configured.",
			)
		default:
			slog.ErrorContext(request.Context(), "Fetch trigger failed", slog.Any("error", err))
			writeError(
				writer,
				http.StatusInternalServerError,
				"internal",
				"The request could not be completed.",
			)
		}

		return
	}

	writeJSON(writer, http.StatusAccepted, query.FetchResponse{Status: "accepted"})
}

func retryAfter(fetcher Fetcher) int64 {
	state := fetcher.State()
	if state.CooldownUntil.IsZero() {
		return 0
	}

	seconds := int64(time.Until(state.CooldownUntil).Seconds())
	if seconds < 0 {
		return 0
	}

	// Round up so a sub-second remainder still yields a positive hint.
	if time.Until(state.CooldownUntil) > time.Duration(seconds)*time.Second {
		seconds++
	}

	return seconds
}
