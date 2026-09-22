package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestBuildMux_EndToEnd(t *testing.T) {
	store := newTestStore(t)
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)
	api := NewAPI(store, "") // BaseURL filled below once we know the server's URL

	mux := buildMux(api, metrics)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	api.BaseURL = srv.URL

	// Create a link.
	resp, err := http.Post(srv.URL+"/api/shorten", "application/json", strings.NewReader(`{"url":"https://example.com"}`))
	if err != nil {
		t.Fatalf("POST /api/shorten: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	// Follow the redirect manually (don't let the client auto-follow).
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	getRoot, _ := client.Get(srv.URL + "/")
	if getRoot.StatusCode != http.StatusOK {
		t.Errorf("GET / status = %d, want 200", getRoot.StatusCode)
	}

	metricsResp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	body, _ := io.ReadAll(metricsResp.Body)
	if !strings.Contains(string(body), "links_created_total 1") {
		t.Errorf("/metrics does not show links_created_total 1:\n%s", body)
	}
}
