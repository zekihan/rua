// Package telemetry defines rua's Prometheus metrics. Labels are bounded:
// never an IP, source name, reporter or other report-derived value except
// the policy domain on records_total.
package telemetry

import (
	"github.com/zekihan/go-commons/metrics"
)

// Metrics groups every metric rua exports. A nil *Metrics is valid and
// records nothing, for tests and one-shot commands.
type Metrics struct {
	registry *metrics.Registry
}

// New registers rua's metrics on registry.
func New(registry *metrics.Registry) *Metrics {
	return &Metrics{registry: registry}
}

// FetchFinished records a fetch attempt in rua_fetch_runs_total{result}
// and rua_fetch_duration_seconds.
func (mt *Metrics) FetchFinished(seconds float64, ok bool) {
	if mt == nil {
		return
	}

	result := "ok"
	if !ok {
		result = "error"
	}

	mt.registry.Counter("rua_fetch_runs_total", metrics.Labels{"result": result}).Inc()
	mt.registry.Histogram("rua_fetch_duration_seconds", nil).Observe(seconds)
}

// LastSuccess sets rua_last_successful_fetch_timestamp_seconds.
func (mt *Metrics) LastSuccess(unix int64) {
	if mt == nil {
		return
	}

	mt.registry.Gauge("rua_last_successful_fetch_timestamp_seconds", nil).Set(float64(unix))
}

// Stale sets rua_ingest_stale to 1 or 0.
func (mt *Metrics) Stale(stale bool) {
	if mt == nil {
		return
	}

	value := 0.0
	if stale {
		value = 1
	}

	mt.registry.Gauge("rua_ingest_stale", nil).Set(value)
}

// MessageIngested counts a newly stored message by class and parse status.
func (mt *Metrics) MessageIngested(class, status string) {
	if mt == nil {
		return
	}

	mt.registry.Counter("rua_messages_ingested_total",
		metrics.Labels{"class": class, "status": status}).Inc()
}

// ReportsIngested counts stored reports by kind (duplicates included).
func (mt *Metrics) ReportsIngested(kind string, n int) {
	if mt == nil || n == 0 {
		return
	}

	mt.registry.Counter("rua_reports_ingested_total", metrics.Labels{"kind": kind}).Add(n)
}

// Messages sets rua_messages{class,status}, the stored message totals.
func (mt *Metrics) Messages(class, status string, n int64) {
	if mt == nil {
		return
	}

	mt.registry.Gauge("rua_messages", metrics.Labels{"class": class, "status": status}).
		Set(float64(n))
}

// Records sets rua_records_messages{domain,verdict}, the message count of
// stored aggregate records.
func (mt *Metrics) Records(domain, verdict string, n int64) {
	if mt == nil {
		return
	}

	mt.registry.Gauge("rua_records_messages",
		metrics.Labels{"domain": domain, "verdict": verdict}).Set(float64(n))
}

// EnrichQueue sets rua_enrich_queue_length.
func (mt *Metrics) EnrichQueue(n int) {
	if mt == nil {
		return
	}

	mt.registry.Gauge("rua_enrich_queue_length", nil).Set(float64(n))
}
