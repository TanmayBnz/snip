package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestAPI(t *testing.T) *API {
	t.Helper()
	store := newTestStore(t)
	return NewAPI(store, "http://localhost:8080")
}

func TestShorten_ValidURL(t *testing.T) {
	api := newTestAPI(t)

	body := strings.NewReader(`{"url":"https://example.com"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/shorten", body)
	rec := httptest.NewRecorder()

	api.Shorten(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	var resp struct {
		Code     string `json:"code"`
		ShortURL string `json:"short_url"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Code == "" || resp.ShortURL != "http://localhost:8080/"+resp.Code {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestShorten_InvalidURL(t *testing.T) {
	api := newTestAPI(t)

	body := strings.NewReader(`{"url":"not-a-url"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/shorten", body)
	rec := httptest.NewRecorder()

	api.Shorten(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestRedirect_KnownCode(t *testing.T) {
	api := newTestAPI(t)
	code, err := api.Store.CreateLink("https://example.com/target")
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/"+code, nil)
	req.SetPathValue("code", code)
	rec := httptest.NewRecorder()

	api.Redirect(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if loc := rec.Header().Get("Location"); loc != "https://example.com/target" {
		t.Errorf("Location = %q, want %q", loc, "https://example.com/target")
	}

	link, err := api.Store.GetLink(code)
	if err != nil {
		t.Fatalf("GetLink: %v", err)
	}
	if link.Clicks != 1 {
		t.Errorf("Clicks = %d, want 1", link.Clicks)
	}
}

func TestRedirect_UnknownCode(t *testing.T) {
	api := newTestAPI(t)

	req := httptest.NewRequest(http.MethodGet, "/doesnotexist", nil)
	req.SetPathValue("code", "doesnotexist")
	rec := httptest.NewRecorder()

	api.Redirect(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
