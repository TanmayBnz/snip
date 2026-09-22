package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestInstrument_RecordsRequestCount(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	handler := m.Instrument("/hello", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/hello", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	got := testutil.ToFloat64(m.requests.WithLabelValues("/hello", "GET", "200"))
	if got != 1 {
		t.Errorf("http_requests_total = %v, want 1", got)
	}
}

func TestInstrument_DefaultsToStatus200WhenWriteHeaderNotCalled(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	handler := m.Instrument("/ok", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok")) // no explicit WriteHeader call
	})

	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	got := testutil.ToFloat64(m.requests.WithLabelValues("/ok", "GET", "200"))
	if got != 1 {
		t.Errorf("http_requests_total = %v, want 1", got)
	}
}

func TestLinkCreatedAndRedirected(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	m.LinkCreated()
	m.LinkCreated()
	m.Redirected()

	if got := testutil.ToFloat64(m.linksCreated); got != 2 {
		t.Errorf("links_created_total = %v, want 2", got)
	}
	if got := testutil.ToFloat64(m.redirects); got != 1 {
		t.Errorf("redirects_total = %v, want 1", got)
	}
}
