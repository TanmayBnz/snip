package main

import (
	"log"
	"net/http"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func getenv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func buildMux(api *API, metrics *Metrics) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", metrics.Instrument("/", api.Index))
	mux.HandleFunc("POST /api/shorten", metrics.Instrument("/api/shorten", func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusRecorder{ResponseWriter: w}
		api.Shorten(recorder, r)
		if recorder.status == http.StatusCreated {
			metrics.LinkCreated()
		}
	}))
	mux.HandleFunc("GET /{code}", metrics.Instrument("/{code}", func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusRecorder{ResponseWriter: w}
		api.Redirect(recorder, r)
		if recorder.status == http.StatusFound {
			metrics.Redirected()
		}
	}))

	mux.Handle("GET /metrics", promhttp.HandlerFor(metrics.gatherer, promhttp.HandlerOpts{}))

	return mux
}

func main() {
	port := getenv("PORT", "8080")
	dbPath := getenv("DB_PATH", "snip.db")
	baseURL := getenv("BASE_URL", "http://localhost:"+port)

	store, err := NewStore(dbPath)
	if err != nil {
		panic(err)
	}
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)
	api := NewAPI(store, baseURL)
	mux := buildMux(api, metrics)
	log.Printf("snip listening on port %s, base URL: %s", port, baseURL)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}
