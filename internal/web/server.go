// Package web serves the HTML dashboard and the JSON API.
package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/stahnma/pr-hud/internal/github"
	"github.com/stahnma/pr-hud/internal/hud"
)

//go:embed templates/*.html
var templateFS embed.FS

// Server is the HTTP front end over a hud.Store.
type Server struct {
	store *hud.Store
	log   *slog.Logger
	tmpl  *template.Template
}

// New returns a Server.
func New(store *hud.Store, log *slog.Logger) *Server {
	tmpl := template.Must(template.New("").Funcs(funcs).ParseFS(templateFS, "templates/*.html"))
	return &Server{store: store, log: log, tmpl: tmpl}
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.page(tabRepos))
	mux.HandleFunc("GET /authored", s.page(tabAuthored))
	mux.HandleFunc("POST /refresh", s.refreshForm)
	mux.HandleFunc("GET /api/v1/repos", s.apiRepos)
	mux.HandleFunc("GET /api/v1/prs", s.apiPRs)
	mux.HandleFunc("GET /api/v1/authored", s.apiAuthored)
	mux.HandleFunc("POST /api/v1/refresh", s.apiRefresh)
	mux.HandleFunc("GET /healthz", s.healthz)
	return s.logRequests(mux)
}

// Page tabs.
const (
	tabRepos    = "repos"    // PRs against the user's repos
	tabAuthored = "authored" // PRs the user opened
)

// tabPaths maps each tab to its URL path.
var tabPaths = map[string]string{tabRepos: "/", tabAuthored: "/authored"}

type pageData struct {
	Tab          string
	View         *hud.View
	RefreshError string
}

func (s *Server) page(tab string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { s.render(w, tab) }
}

func (s *Server) render(w http.ResponseWriter, tab string) {
	data := pageData{Tab: tab}
	snap, lastErr := s.store.State()
	if lastErr != nil {
		data.RefreshError = lastErr.Error()
	}
	if snap != nil {
		v := hud.BuildView(snap)
		data.View = &v
	}

	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, "index.html", data); err != nil {
		s.log.Error("rendering page", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (s *Server) refreshForm(w http.ResponseWriter, r *http.Request) {
	s.refresh(r) // a failure is shown on the page via the store's last error
	back, ok := tabPaths[r.FormValue("tab")]
	if !ok {
		back = "/"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// refresh runs a refresh that survives the client going away.
func (s *Server) refresh(r *http.Request) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Minute)
	defer cancel()
	return s.store.Refresh(ctx)
}

type apiMeta struct {
	User             string           `json:"user"`
	FetchedAt        time.Time        `json:"fetched_at"`
	Summary          hud.Summary      `json:"summary"`
	RateLimit        github.RateLimit `json:"rate_limit"`
	Errors           []string         `json:"errors"`
	LastRefreshError string           `json:"last_refresh_error,omitempty"`
}

// apiView returns the view for an API request, or writes a 503 and returns
// false while the cache is still empty.
func (s *Server) apiView(w http.ResponseWriter) (hud.View, apiMeta, bool) {
	snap, lastErr := s.store.State()
	if snap == nil {
		body := map[string]string{"error": "cache is warming up; try again shortly"}
		if lastErr != nil {
			body["last_refresh_error"] = lastErr.Error()
		}
		writeJSON(w, http.StatusServiceUnavailable, body)
		return hud.View{}, apiMeta{}, false
	}
	v := hud.BuildView(snap)
	meta := apiMeta{
		User:      v.User,
		FetchedAt: v.FetchedAt,
		Summary:   v.Summary,
		RateLimit: v.RateLimit,
		Errors:    v.Errors,
	}
	if meta.Errors == nil {
		meta.Errors = []string{}
	}
	if lastErr != nil {
		meta.LastRefreshError = lastErr.Error()
	}
	return v, meta, true
}

func (s *Server) apiRepos(w http.ResponseWriter, r *http.Request) {
	v, meta, ok := s.apiView(w)
	if !ok {
		return
	}
	repos := slices.Clip(v.Active)
	if r.URL.Query().Get("has_prs") != "true" {
		repos = append(repos, v.Quiet...)
	}
	if repos == nil {
		repos = []github.Repo{}
	}
	writeJSON(w, http.StatusOK, struct {
		apiMeta
		Repos []github.Repo `json:"repos"`
	}{meta, repos})
}

func (s *Server) apiPRs(w http.ResponseWriter, r *http.Request) {
	v, meta, ok := s.apiView(w)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, struct {
		apiMeta
		PRs []hud.FlatPR `json:"prs"`
	}{meta, v.PRs()})
}

func (s *Server) apiAuthored(w http.ResponseWriter, r *http.Request) {
	v, meta, ok := s.apiView(w)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, struct {
		apiMeta
		PRs []hud.FlatPR `json:"prs"`
	}{meta, v.Authored})
}

func (s *Server) apiRefresh(w http.ResponseWriter, r *http.Request) {
	if err := s.refresh(r); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	snap, _ := s.store.State()
	writeJSON(w, http.StatusOK, map[string]any{"fetched_at": snap.FetchedAt})
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	snap, _ := s.store.State()
	body := map[string]any{"status": "ok", "has_data": snap != nil}
	if snap != nil {
		body["fetched_at"] = snap.FetchedAt
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.log.Debug("request", "method", r.Method, "path", r.URL.Path, "took", time.Since(start).Round(time.Microsecond))
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

var funcs = template.FuncMap{
	"ago": ago,
	// withoutAuthor blanks the author so the "pr" template leaves it out,
	// for lists where every PR has the same author.
	"withoutAuthor": func(p github.PullRequest) github.PullRequest { p.Author = ""; return p },
	"abs":           func(t time.Time) string { return t.Local().Format("2006-01-02 15:04 MST") },
	"plural": func(n int, one, many string) string {
		if n == 1 {
			return one
		}
		return many
	},
	"ciClass": func(s string) string { return ci(s).class },
	"ciIcon":  func(s string) string { return ci(s).icon },
	"ciLabel": func(s string) string { return ci(s).label },
	"reviewClass": func(s string) string {
		switch s {
		case "APPROVED":
			return "review-approved"
		case "CHANGES_REQUESTED":
			return "review-changes"
		}
		return "review-required"
	},
	"reviewLabel": func(s string) string {
		switch s {
		case "APPROVED":
			return "approved"
		case "CHANGES_REQUESTED":
			return "changes requested"
		case "REVIEW_REQUIRED":
			return "review required"
		}
		return s
	},
}

type ciStyle struct{ class, icon, label string }

func ci(state string) ciStyle {
	switch state {
	case "SUCCESS":
		return ciStyle{"ci-success", "✓", "passing"}
	case "FAILURE", "ERROR":
		return ciStyle{"ci-failure", "✗", "failing"}
	case "PENDING", "EXPECTED":
		return ciStyle{"ci-pending", "●", "pending"}
	}
	return ciStyle{"ci-none", "○", "no checks"}
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/24/30))
	}
	return fmt.Sprintf("%dy ago", int(d.Hours()/24/365))
}
