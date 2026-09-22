package main

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	requests     *prometheus.CounterVec
	duration     *prometheus.HistogramVec
	linksCreated prometheus.Counter
	redirects    prometheus.Counter
	gatherer     prometheus.Gatherer
}

func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total HTTP requests handled.",
		}, []string{"route", "method", "status"}),

		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "http_request_duration_seconds",
			Help: "HTTP request duration in seconds.",
		}, []string{"route", "method"}),

		linksCreated: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "links_created_total",
			Help: "Total short links created.",
		}),

		redirects: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "redirects_total",
			Help: "Total redirects served.",
		}),
	}

	reg.MustRegister(m.requests, m.duration, m.linksCreated, m.redirects)
	if g, ok := reg.(prometheus.Gatherer); ok {
		m.gatherer = g
	}
	return m
}

// statusRecorder wraps http.ResponseWriter to capture the status code that
// was written, since http.ResponseWriter doesn't expose it directly.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (m *Metrics) Instrument(route string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()

		next(rec, r)

		duration := time.Since(start).Seconds()
		status := strconv.Itoa(rec.status)

		m.requests.WithLabelValues(route, r.Method, status).Inc()
		m.duration.WithLabelValues(route, r.Method).Observe(duration)
	}
}

func (m *Metrics) LinkCreated() {
	m.linksCreated.Inc()
}

func (m *Metrics) Redirected() {
	m.redirects.Inc()
}
