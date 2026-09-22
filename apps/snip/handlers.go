package main

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
)

var indexTmpl = template.Must(template.ParseFiles("templates/index.html.tmpl"))

type API struct {
	Store   *Store
	BaseURL string
}

func NewAPI(store *Store, baseURL string) *API {
	return &API{
		Store:   store,
		BaseURL: baseURL,
	}
}

func (a *API) Index(w http.ResponseWriter, r *http.Request) {
	err := indexTmpl.Execute(w, nil)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
}

func (a *API) Shorten(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	parsedURL, err := url.ParseRequestURI(req.URL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		http.Error(w, "Invalid URL", http.StatusBadRequest)
		return
	}

	code, err := a.Store.CreateLink(req.URL)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	resp := struct {
		Code     string `json:"code"`
		ShortURL string `json:"short_url"`
	}{
		Code:     code,
		ShortURL: a.BaseURL + "/" + code,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

func (a *API) Redirect(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")

	link, err := a.Store.GetLink(code)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	a.Store.IncrementClicks(code)

	http.Redirect(w, r, link.URL, http.StatusFound)
}
