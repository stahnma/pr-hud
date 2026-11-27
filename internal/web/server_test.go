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

type fakeFetcher struct {
	repos    []github.Repo
	authored []github.RepoPR
}

func (f fakeFetcher) Viewer(context.Context) (string, error) { return "me", nil }
func (f fakeFetcher) RateLimit() github.RateLimit            { return github.RateLimit{Remaining: 100} }
func (f fakeFetcher) OwnerRepos(context.Context, string, github.OwnerOptions) ([]github.Repo, error) {
	return f.repos, nil
}
func (f fakeFetcher) Repos(context.Context, []string) ([]github.Repo, []error) { return nil, nil }
func (f fakeFetcher) AuthoredPRs(context.Context, string, github.OwnerOptions) ([]github.RepoPR, error) {
	return f.authored, nil
}

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

func newServer(t *testing.T, repos ...github.Repo) http.Handler {
	t.Helper()
	return newServerWith(t, fakeFetcher{repos: repos})
}

func newServerWith(t *testing.T, f fakeFetcher) http.Handler {
	t.Helper()
	store := hud.NewStore(f, hud.Options{Interval: time.Hour}, quietLog)
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

var mine = github.RepoPR{Repo: "upstream/proj", RepoURL: "https://github.com/upstream/proj", PullRequest: github.PullRequest{
	Number: 42, Title: "Fix the thing", URL: "https://github.com/upstream/proj/pull/42", Author: "me", UpdatedAt: now, ReviewDecision: "CHANGES_REQUESTED",
}}

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

func TestAuthoredTab(t *testing.T) {
	h := newServerWith(t, fakeFetcher{repos: []github.Repo{busy}, authored: []github.RepoPR{mine}})

	body := get(t, h, "/authored").Body.String()
	for _, want := range []string{"upstream/proj", "Fix the thing", "#42", "changes requested", `href="https://github.com/upstream/proj/pulls"`, "(1) My PRs · PR HUD", `name="tab" value="authored"`} {
		if !strings.Contains(body, want) {
			t.Errorf("authored page missing %q", want)
		}
	}
	if strings.Contains(body, "me/busy</a>") {
		t.Error("authored page shows repo view")
	}
	if strings.Contains(body, "<span>by me</span>") {
		t.Error("authored page shows the author")
	}

	// The repos tab doesn't show authored PRs but links to them, and keeps authors.
	body = get(t, h, "/").Body.String()
	if strings.Contains(body, "Fix the thing") || !strings.Contains(body, `href="/authored"`) || !strings.Contains(body, "<span>by alice</span>") {
		t.Error("repos tab should link to, not show, authored PRs")
	}
}

func TestAuthoredTabEmpty(t *testing.T) {
	body := get(t, newServer(t, busy), "/authored").Body.String()
	if !strings.Contains(body, "has no open pull requests") {
		t.Error("expected empty-state message")
	}
}

func TestRefreshRedirectsToTab(t *testing.T) {
	h := newServer(t, idle)
	for tab, want := range map[string]string{"authored": "/authored", "ci": "/ci", "repos": "/", "https://evil.example": "/"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/refresh", strings.NewReader("tab="+tab))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		h.ServeHTTP(rec, req)
		if got := rec.Header().Get("Location"); got != want {
			t.Errorf("tab=%q: Location = %q, want %q", tab, got, want)
		}
	}
}

func TestAPIAuthored(t *testing.T) {
	var resp struct {
		Summary hud.Summary  `json:"summary"`
		PRs     []hud.FlatPR `json:"prs"`
	}
	h := newServerWith(t, fakeFetcher{authored: []github.RepoPR{mine}})
	if err := json.Unmarshal(get(t, h, "/api/v1/authored").Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.PRs) != 1 || resp.PRs[0].Repo != "upstream/proj" || resp.PRs[0].Number != 42 || resp.Summary.AuthoredPRs != 1 {
		t.Errorf("resp = %+v", resp)
	}
}

var broken = github.Repo{NameWithOwner: "me/broken", Name: "broken", URL: "https://github.com/me/broken", Source: "owned", PRs: []github.PullRequest{},
	DefaultBranch: &github.BranchCI{Branch: "main", State: github.CIFailure, CommitURL: "https://github.com/me/broken/commit/abc", CommittedAt: now, Checks: []github.Check{
		{Name: "nightly", Event: "schedule", State: github.CIFailure, Conclusion: "FAILURE", URL: "https://github.com/me/broken/actions/runs/7", At: now},
		{Name: "CI", Event: "push", State: github.CISuccess, Conclusion: "SUCCESS", URL: "https://github.com/me/broken/actions/runs/6", At: now},
	}}}

func TestCITab(t *testing.T) {
	green := idle
	green.DefaultBranch = &github.BranchCI{Branch: "main", State: github.CISuccess}
	h := newServer(t, busy, broken, green)

	body := get(t, h, "/ci").Body.String()
	for _, want := range []string{
		"(2) CI · PR HUD", `href="https://github.com/me/broken/actions"`, "nightly", ">scheduled<",
		`href="https://github.com/me/broken/actions/runs/7"`, "failure just now", // failing check and its run
		"Add &lt;feature&gt;", "by alice", // alice's failing PR on me/busy
		"1 repository passing on the default branch", "1 repository with no checks",
		`class="n bad">2<`, `name="tab" value="ci"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("CI page missing %q", want)
		}
	}
	if strings.Contains(body, "actions/runs/6") {
		t.Error("CI page lists a passing check for a failing repo")
	}
	if strings.Contains(body, "All green") {
		t.Error("CI page says all green despite failures")
	}
}

func TestCITabAllGreen(t *testing.T) {
	body := get(t, newServer(t, idle), "/ci").Body.String()
	if !strings.Contains(body, "All green") || !strings.Contains(body, "✓ CI · PR HUD") {
		t.Error("expected all-green state")
	}
}

func TestAPICI(t *testing.T) {
	var resp struct {
		Summary hud.Summary `json:"summary"`
		hud.CIView
	}
	if err := json.Unmarshal(get(t, newServer(t, busy, broken), "/api/v1/ci").Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Failing) != 1 || resp.Failing[0].DefaultBranch.Failing()[0].Name != "nightly" {
		t.Errorf("failing = %+v", resp.Failing)
	}
	if len(resp.FailingPRs) != 1 || resp.FailingPRs[0].Number != 1 || resp.Pending == nil || resp.NoCI == nil {
		t.Errorf("resp = %+v", resp.CIView)
	}
	if resp.Summary.CIFailingRepos != 1 || resp.Summary.FailingPRs != 1 {
		t.Errorf("summary = %+v", resp.Summary)
	}
}

func TestFavicon(t *testing.T) {
	h := newServer(t)

	rec := get(t, h, "/favicon.svg")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/svg+xml" || !strings.Contains(rec.Body.String(), "<svg") {
		t.Errorf("favicon.svg: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec := get(t, h, "/favicon.ico"); rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/favicon.svg" {
		t.Errorf("favicon.ico: %d -> %q", rec.Code, rec.Header().Get("Location"))
	}
	if !strings.Contains(get(t, h, "/").Body.String(), `<link rel="icon" href="/favicon.svg"`) {
		t.Error("page doesn't link the favicon")
	}
}
