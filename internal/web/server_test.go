package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stahnma/pr-hud/internal/github"
	"github.com/stahnma/pr-hud/internal/hud"
)

type fakeFetcher struct{ repos []github.Repo }

func (f fakeFetcher) Viewer(context.Context) (string, error) { return "me", nil }
func (f fakeFetcher) RateLimit() github.RateLimit            { return github.RateLimit{Remaining: 100} }
func (f fakeFetcher) OwnerRepos(context.Context, string, github.OwnerOptions) ([]github.Repo, error) {
	return f.repos, nil
}
func (f fakeFetcher) Repos(context.Context, []string) ([]github.Repo, []error) { return nil, nil }

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

func newServer(t *testing.T, repos ...github.Repo) http.Handler {
	t.Helper()
	store := hud.NewStore(fakeFetcher{repos}, hud.Options{Interval: time.Hour}, quietLog)
	if err := store.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return New(store, quietLog).Handler()
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

var now = time.Now()

var (
	busy = github.Repo{NameWithOwner: "me/busy", Name: "busy", URL: "https://github.com/me/busy", OpenPRCount: 2, PRs: []github.PullRequest{
		{Number: 1, Title: "Add <feature>", Author: "alice", UpdatedAt: now, ReviewDecision: "APPROVED", CIStatus: "FAILURE"},
		{Number: 2, Title: "Bump deps", Author: "dependabot", AuthorIsBot: true, UpdatedAt: now.Add(-time.Hour)},
	}}
	idle = github.Repo{NameWithOwner: "me/idle", Name: "idle", URL: "https://github.com/me/idle", PRs: []github.PullRequest{}}
)

func TestIndex(t *testing.T) {
	rec := get(t, newServer(t, busy, idle), "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`target="_blank" rel="noopener" href="https://github.com/me/busy/pulls"`, "me/busy", "Add &lt;feature&gt;", "approved", "ci-failure", "1 repository with no open pull requests", ">bot<"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(body, "All clear") {
		t.Error("page says all clear despite open PRs")
	}
}

func TestIndexAllClear(t *testing.T) {
	body := get(t, newServer(t, idle), "/").Body.String()
	if !strings.Contains(body, "All clear") {
		t.Error("expected all-clear banner")
	}
}

func TestBotOnlyIsNeverAllClear(t *testing.T) {
	botOnly := github.Repo{NameWithOwner: "me/deps", Name: "deps", URL: "https://github.com/me/deps", OpenPRCount: 1, PRs: []github.PullRequest{
		{Number: 9, Title: "Bump x", Author: "dependabot", AuthorIsBot: true, UpdatedAt: now},
	}}
	h := newServer(t, botOnly, idle)
	body := get(t, h, "/").Body.String()
	if strings.Contains(body, "All clear") {
		t.Error("all clear shown with an open bot PR")
	}
	if !strings.Contains(body, "me/deps") || !strings.Contains(body, "Bump x") || !strings.Contains(body, "(1) PR HUD") {
		t.Error("bot-only repo not shown as active")
	}
}

func TestAPIRepos(t *testing.T) {
	h := newServer(t, idle, busy)
	var resp struct {
		User    string        `json:"user"`
		Summary hud.Summary   `json:"summary"`
		Errors  []string      `json:"errors"`
		Repos   []github.Repo `json:"repos"`
	}
	if err := json.Unmarshal(get(t, h, "/api/v1/repos").Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.User != "me" || len(resp.Repos) != 2 || resp.Repos[0].NameWithOwner != "me/busy" || resp.Errors == nil {
		t.Errorf("resp = %+v", resp)
	}
	if resp.Summary.OpenPRs != 2 {
		t.Errorf("summary = %+v", resp.Summary)
	}

	if err := json.Unmarshal(get(t, h, "/api/v1/repos?has_prs=true").Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Repos) != 1 || len(resp.Repos[0].PRs) != 2 {
		t.Errorf("filtered resp = %+v", resp)
	}
}

func TestAPIPRs(t *testing.T) {
	var resp struct {
		PRs []hud.FlatPR `json:"prs"`
	}
	if err := json.Unmarshal(get(t, newServer(t, busy), "/api/v1/prs").Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.PRs) != 2 || resp.PRs[0].Repo != "me/busy" || resp.PRs[0].Number != 1 {
		t.Errorf("prs = %+v", resp.PRs)
	}
}

func TestAPIWarmingUp(t *testing.T) {
	store := hud.NewStore(fakeFetcher{}, hud.Options{Interval: time.Hour}, quietLog)
	rec := get(t, New(store, quietLog).Handler(), "/api/v1/repos")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d", rec.Code)
	}
}
