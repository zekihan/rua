package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/zekihan/rua/internal/imapsrc"
	"github.com/zekihan/rua/internal/ingest"
	"github.com/zekihan/rua/internal/query"
	"github.com/zekihan/rua/internal/sources"
	"github.com/zekihan/rua/internal/store"
)

var serverNow = time.Date(2025, time.June, 15, 12, 0, 0, 0, time.UTC)

func eq[T comparable](t *testing.T, got, want T) {
	t.Helper()

	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func unixDay(year int, month time.Month, day int) int64 {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC).Unix()
}

func aggregateDoc(reportID string, begin, end int64) string {
	return fmt.Sprintf(
		`<feedback><report_metadata><org_name>Google</org_name>`+
			`<email>reports@example.net</email><report_id>%s</report_id>`+
			`<date_range><begin>%d</begin><end>%d</end></date_range></report_metadata>`+
			`<policy_published><domain>example.com</domain><adkim>r</adkim><aspf>r</aspf>`+
			`<p>reject</p><sp>reject</sp><pct>100</pct></policy_published>`+
			`<record><row><source_ip>192.0.2.1</source_ip><count>10</count>`+
			`<policy_evaluated><disposition>none</disposition><dkim>pass</dkim><spf>fail</spf></policy_evaluated></row>`+
			`<identifiers><header_from>example.com</header_from><envelope_from>example.com</envelope_from>`+
			`<envelope_to>dest.example.net</envelope_to></identifiers>`+
			`<auth_results><dkim><domain>example.com</domain><selector>s1</selector><result>pass</result></dkim>`+
			`<spf><domain>example.com</domain><result>fail</result></spf></auth_results></record></feedback>`,
		reportID, begin, end,
	)
}

type fakeFetcher struct {
	err   error
	state imapsrc.State
}

func (fake *fakeFetcher) Trigger() error { return fake.err }

func (fake *fakeFetcher) State() imapsrc.State { return fake.state }

func fetcherWith(err error) *fakeFetcher {
	var fake fakeFetcher

	fake.err = err

	var state imapsrc.State

	fake.state = state

	return &fake
}

func fetcherCooldown() *fakeFetcher {
	var fake fakeFetcher

	fake.err = imapsrc.ErrCooldown

	var state imapsrc.State

	state.Enabled = false
	state.Running = false
	state.NextFetch = time.Time{}
	state.CooldownUntil = time.Now().Add(2 * time.Minute)
	fake.state = state

	return &fake
}

type testEnv struct {
	handler http.Handler
	st      *store.Store
}

func newEnv(t *testing.T, fetcher Fetcher) *testEnv {
	t.Helper()

	st, err := store.Open(t.Context(), store.Memory)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = st.Close() })

	var opts ingest.Options

	opts.Metrics = nil
	opts.Now = func() time.Time { return serverNow }
	opts.NewIPs = nil
	opts.RetentionDays = 0

	pipe := ingest.New(st, opts)

	var src ingest.Source

	_, ingestErr := pipe.Ingest(
		t.Context(),
		[]byte(aggregateDoc("r1", unixDay(2025, time.June, 9), unixDay(2025, time.June, 11))),
		src,
	)
	if ingestErr != nil {
		t.Fatal(ingestErr)
	}

	var deps query.Deps

	deps.Store = st
	deps.Sources = sources.Empty
	deps.Fetch = nil
	deps.Reparsing = nil
	deps.EnrichQueue = nil
	deps.Now = func() time.Time { return serverNow }
	deps.StaleAfter = 3 * time.Hour
	deps.IMAPEnabled = false

	var indexFile fstest.MapFile

	indexFile.Data = []byte("<html>rua app</html>")
	indexFile.Mode = 0
	indexFile.ModTime = time.Time{}
	indexFile.Sys = nil

	var assetFile fstest.MapFile

	assetFile.Data = []byte("console.log(1)")
	assetFile.Mode = 0
	assetFile.ModTime = time.Time{}
	assetFile.Sys = nil

	webFS := fstest.MapFS{"index.html": &indexFile, "assets/app-abc.js": &assetFile}

	var serverOpts Options

	serverOpts.Query = query.New(deps)
	serverOpts.Fetcher = fetcher
	serverOpts.Web = webFS
	serverOpts.Ping = nil

	return &testEnv{handler: New(serverOpts).Handler(), st: st}
}

func (env *testEnv) do(
	t *testing.T, method, target string, headers map[string]string,
) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, target, http.NoBody)

	for key, value := range headers {
		req.Header.Set(key, value)
	}

	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)

	return rec
}

func checkHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	eq(t, strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'self'"), true)
	eq(t, rec.Header().Get("X-Content-Type-Options"), "nosniff")
	eq(t, rec.Header().Get("Referrer-Policy"), "no-referrer")
}

func decodeJSON[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()

	var value T

	err := json.Unmarshal(rec.Body.Bytes(), &value)
	if err != nil {
		t.Fatal(err)
	}

	return value
}

func TestServerListRoutes(t *testing.T) {
	t.Parallel()

	env := newEnv(t, fetcherWith(imapsrc.ErrDisabled))

	routes := []string{
		"/api/domains",
		"/api/overview?from=2025-06-01&to=2025-06-15&tz=UTC",
		"/api/sources?from=2025-06-01&to=2025-06-15&tz=UTC",
		"/api/reports?from=2025-06-01&to=2025-06-15&tz=UTC",
		"/api/tls?from=2025-06-01&to=2025-06-15&tz=UTC",
		"/api/tls/example.com?from=2025-06-01&to=2025-06-15&tz=UTC",
		"/api/status",
	}

	for _, route := range routes {
		rec := env.do(t, http.MethodGet, route, nil)
		eq(t, rec.Code, http.StatusOK)
		eq(t, rec.Header().Get("Cache-Control"), "no-store")
		checkHeaders(t, rec)
	}
}

func TestServerDetailRoutes(t *testing.T) {
	t.Parallel()

	env := newEnv(t, fetcherWith(imapsrc.ErrDisabled))

	list := decodeJSON[query.ReportsResponse](
		t, env.do(t, http.MethodGet, "/api/reports?from=2025-06-01&to=2025-06-15&tz=UTC", nil),
	)
	eq(t, len(list.Reports), 1)

	id := list.Reports[0].ID
	eq(
		t,
		env.do(t, http.MethodGet, fmt.Sprintf("/api/reports/%d?tz=UTC", id), nil).Code,
		http.StatusOK,
	)

	sourcesBody := decodeJSON[query.SourcesResponse](
		t, env.do(t, http.MethodGet, "/api/sources?from=2025-06-01&to=2025-06-15&tz=UTC", nil),
	)
	eq(t, len(sourcesBody.Sources), 1)

	key := sourcesBody.Sources[0].Key
	sourceURL := "/api/sources/" + key + "?from=2025-06-01&to=2025-06-15&tz=UTC"
	eq(t, env.do(t, http.MethodGet, sourceURL, nil).Code, http.StatusOK)
}

func TestServerRawAndErrors(t *testing.T) {
	t.Parallel()

	env := newEnv(t, fetcherWith(imapsrc.ErrDisabled))

	list := decodeJSON[query.ReportsResponse](
		t, env.do(t, http.MethodGet, "/api/reports?from=2025-06-01&to=2025-06-15&tz=UTC", nil),
	)
	id := list.Reports[0].ID

	raw := env.do(t, http.MethodGet, fmt.Sprintf("/api/reports/%d/raw", id), nil)
	eq(t, raw.Code, http.StatusOK)
	eq(t, raw.Header().Get("Content-Type"), "application/octet-stream")
	eq(t, strings.Contains(raw.Header().Get("Content-Disposition"), "attachment"), true)

	msgRaw := env.do(t, http.MethodGet, "/api/messages/1/raw", nil)
	eq(t, msgRaw.Code, http.StatusOK)
	eq(t, strings.Contains(msgRaw.Header().Get("Content-Disposition"), "message-1.eml"), true)

	eq(t, env.do(t, http.MethodGet, "/api/reports/99999?tz=UTC", nil).Code, http.StatusNotFound)
	eq(
		t,
		env.do(t, http.MethodGet, "/api/sources/ip:203.0.113.9?tz=UTC", nil).Code,
		http.StatusNotFound,
	)
	eq(t, env.do(t, http.MethodGet, "/api/overview?tz=Local", nil).Code, http.StatusBadRequest)
	eq(t, env.do(t, http.MethodGet, "/api/reports/notanumber", nil).Code, http.StatusBadRequest)

	unknown := env.do(t, http.MethodGet, "/api/unknown", nil)
	eq(t, unknown.Code, http.StatusNotFound)
	eq(t, strings.Contains(unknown.Body.String(), `"not_found"`), true)

	health := env.do(t, http.MethodGet, "/healthz", nil)
	eq(t, health.Code, http.StatusOK)
	eq(t, health.Body.String(), "ok")
	checkHeaders(t, health)
}

func TestFetchAccepted(t *testing.T) {
	t.Parallel()

	env := newEnv(t, fetcherWith(nil))
	eq(t, env.do(t, http.MethodPost, "/api/fetch", nil).Code, http.StatusForbidden)

	accepted := env.do(t, http.MethodPost, "/api/fetch", map[string]string{"X-Rua": "1"})
	eq(t, accepted.Code, http.StatusAccepted)
	eq(t, strings.Contains(accepted.Body.String(), `"accepted"`), true)
	eq(
		t,
		env.do(
			t,
			http.MethodPost,
			"/api/fetch",
			map[string]string{"Sec-Fetch-Site": "same-origin"},
		).Code,
		http.StatusAccepted,
	)
}

func TestFetchStates(t *testing.T) {
	t.Parallel()

	running := newEnv(t, fetcherWith(imapsrc.ErrRunning))
	eq(
		t,
		running.do(t, http.MethodPost, "/api/fetch", map[string]string{"X-Rua": "1"}).Code,
		http.StatusConflict,
	)

	cooldown := newEnv(t, fetcherCooldown())

	rec := cooldown.do(t, http.MethodPost, "/api/fetch", map[string]string{"X-Rua": "1"})
	eq(t, rec.Code, http.StatusTooManyRequests)
	eq(t, rec.Header().Get("Retry-After") == "", false)

	disabled := newEnv(t, fetcherWith(imapsrc.ErrDisabled))
	eq(
		t,
		disabled.do(t, http.MethodPost, "/api/fetch", map[string]string{"X-Rua": "1"}).Code,
		http.StatusServiceUnavailable,
	)

	noFetcher := newEnv(t, nil)
	eq(
		t,
		noFetcher.do(t, http.MethodPost, "/api/fetch", map[string]string{"X-Rua": "1"}).Code,
		http.StatusServiceUnavailable,
	)
}

func TestSPAFallback(t *testing.T) {
	t.Parallel()

	env := newEnv(t, nil)

	root := env.do(t, http.MethodGet, "/", nil)
	eq(t, root.Code, http.StatusOK)
	eq(t, strings.Contains(root.Body.String(), "rua app"), true)
	eq(t, root.Header().Get("Cache-Control"), "no-cache")
	checkHeaders(t, root)

	eq(t, env.do(t, http.MethodGet, "/sources?tz=UTC", nil).Code, http.StatusOK)

	asset := env.do(t, http.MethodGet, "/assets/app-abc.js", nil)
	eq(t, asset.Code, http.StatusOK)
	eq(t, strings.Contains(asset.Header().Get("Cache-Control"), "immutable"), true)
	eq(t, env.do(t, http.MethodGet, "/assets/missing.js", nil).Code, http.StatusNotFound)
	eq(t, env.do(t, http.MethodGet, "/missing.png", nil).Code, http.StatusNotFound)
}

func TestReportsCursorOverHTTP(t *testing.T) {
	t.Parallel()

	env := newEnv(t, nil)

	var opts ingest.Options

	opts.Metrics = nil
	opts.Now = func() time.Time { return serverNow }
	opts.NewIPs = nil
	opts.RetentionDays = 0

	pipe := ingest.New(env.st, opts)

	var src ingest.Source

	_, err := pipe.Ingest(
		t.Context(),
		[]byte(aggregateDoc("r2", unixDay(2025, time.June, 11), unixDay(2025, time.June, 13))),
		src,
	)
	if err != nil {
		t.Fatal(err)
	}

	page1 := decodeJSON[query.ReportsResponse](
		t,
		env.do(t, http.MethodGet, "/api/reports?from=2025-06-01&to=2025-06-15&tz=UTC&limit=1", nil),
	)
	eq(t, len(page1.Reports), 1)
	eq(t, page1.NextCursor == "", false)

	page2 := decodeJSON[query.ReportsResponse](
		t,
		env.do(
			t,
			http.MethodGet,
			"/api/reports?from=2025-06-01&to=2025-06-15&tz=UTC&limit=1&cursor="+page1.NextCursor,
			nil,
		),
	)
	eq(t, len(page2.Reports), 1)
	eq(t, page1.Reports[0].ID == page2.Reports[0].ID, false)
	eq(t, env.do(t, http.MethodGet, "/api/reports?cursor=!!!", nil).Code, http.StatusBadRequest)

	status := decodeJSON[query.Status](t, env.do(t, http.MethodGet, "/api/status", nil))
	eq(t, status.Problems == nil, false)
	eq(t, status.Mailboxes == nil, false)
	eq(t, status.Classes == nil, false)
}
