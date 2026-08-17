// Package httpapi exposes the tarindex service over HTTP.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"task042-tarindex/internal/tarindex"
)

// maxArchiveSize caps an uploaded archive to avoid unbounded memory use.
const maxArchiveSize = 64 << 20

// API wires a tarindex.Service to HTTP endpoints.
type API struct {
	svc *tarindex.Service
}

// New creates an API bound to the given service.
func New(svc *tarindex.Service) *API { return &API{svc: svc} }

// Handler returns the HTTP handler for all routes.
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.health)
	mux.HandleFunc("POST /archives", a.create)
	mux.HandleFunc("GET /archives/{id}", a.get)
	mux.HandleFunc("GET /archives/{id}/entries", a.entries)
	mux.HandleFunc("DELETE /archives/{id}", a.del)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, tarindex.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, tarindex.ErrInvalidArchive),
		errors.Is(err, tarindex.ErrInvalidType),
		errors.Is(err, tarindex.ErrInvalidSort),
		errors.Is(err, tarindex.ErrInvalidLimit),
		errors.Is(err, tarindex.ErrInvalidOffset),
		errors.Is(err, tarindex.ErrInvalidSize),
		errors.Is(err, tarindex.ErrBadPattern):
		status = http.StatusBadRequest
	}
	writeJSON(w, status, map[string]any{"error": err.Error(), "status": status})
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) create(w http.ResponseWriter, r *http.Request) {
	summary, err := a.svc.Create(io.LimitReader(r.Body, maxArchiveSize))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (a *API) get(w http.ResponseWriter, r *http.Request) {
	summary, err := a.svc.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (a *API) entries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := tarindex.Filters{
		TypeF: q.Get("type"),
		Name:  q.Get("name"),
		Sort:  q.Get("sort"),
	}
	if v := q.Get("min_size"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeError(w, tarindex.ErrInvalidSize)
			return
		}
		f.MinSize = n
		f.MinSet = true
	}
	if v := q.Get("max_size"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeError(w, tarindex.ErrInvalidSize)
			return
		}
		f.MaxSize = n
		f.MaxSet = true
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeError(w, tarindex.ErrInvalidLimit)
			return
		}
		f.Limit = n
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, tarindex.ErrInvalidOffset)
			return
		}
		f.Offset = n
	}
	resp, err := a.svc.Search(r.PathValue("id"), f)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *API) del(w http.ResponseWriter, r *http.Request) {
	if !a.svc.Delete(r.PathValue("id")) {
		writeError(w, tarindex.ErrNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
