package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

func fakeServer(t *testing.T, handle func(req gqlRequest) string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "bearer tok" {
			t.Errorf("Authorization = %q", got)
		}
		var req gqlRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(handle(req)))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "tok")
}

const pageOne = `{"data":{"rateLimit":{"remaining":4990,"resetAt":"2026-01-01T00:00:00Z"},
"repositoryOwner":{"repositories":{"pageInfo":{"hasNextPage":true,"endCursor":"c1"},"nodes":[
 {"nameWithOwner":"me/alpha","name":"alpha","url":"https://github.com/me/alpha","owner":{"login":"me"},
  "pullRequests":{"totalCount":30,"nodes":[
   {"number":7,"title":"Bump x","url":"u","isDraft":false,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-02T00:00:00Z",
    "reviewDecision":null,"author":{"login":"dependabot","__typename":"Bot"},
    "commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"SUCCESS"}}}]}},
   {"number":8,"title":"Feature","url":"u","isDraft":true,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-02T00:00:00Z",
    "reviewDecision":"APPROVED","author":null,"commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}}
  ]}},
 {"nameWithOwner":"me/old","name":"old","isArchived":true,"owner":{"login":"me"},"pullRequests":{"totalCount":0,"nodes":[]}}
]}}}}`

const pageTwo = `{"data":{"repositoryOwner":{"repositories":{"pageInfo":{"hasNextPage":false},"nodes":[
 {"nameWithOwner":"me/beta","name":"beta","owner":{"login":"me"},"pullRequests":{"totalCount":0,"nodes":[]}}
]}}}}`

func TestOwnerRepos(t *testing.T) {
	c := fakeServer(t, func(req gqlRequest) string {
		if req.Variables["isFork"] != false {
			t.Errorf("isFork = %v, want false", req.Variables["isFork"])
		}
		if _, ok := req.Variables["privacy"]; ok {
			t.Errorf("privacy should be unset when including private repos")
		}
		if req.Variables["cursor"] == "c1" {
			return pageTwo
		}
		return pageOne
	})

	repos, err := c.OwnerRepos(context.Background(), "me", OwnerOptions{IncludePrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 || repos[0].NameWithOwner != "me/alpha" || repos[1].NameWithOwner != "me/beta" {
		t.Fatalf("repos = %+v (archived repo should be filtered)", repos)
	}

	a := repos[0]
	if a.OpenPRCount != 30 || !a.PRsTruncated || a.MorePRs() != 28 {
		t.Errorf("count=%d truncated=%v more=%d", a.OpenPRCount, a.PRsTruncated, a.MorePRs())
	}
	bot, human := a.PRs[0], a.PRs[1]
	if !bot.AuthorIsBot || bot.CIStatus != "SUCCESS" || bot.ReviewDecision != "" {
		t.Errorf("bot PR = %+v", bot)
	}
	if human.AuthorIsBot || human.Author != "ghost" || !human.Draft || human.CIStatus != "" || human.ReviewDecision != "APPROVED" {
		t.Errorf("human PR = %+v", human)
	}
	if rl := c.RateLimit(); rl.Remaining != 4990 {
		t.Errorf("rate limit = %+v", rl)
	}
}

func TestReposPartialFailure(t *testing.T) {
	c := fakeServer(t, func(req gqlRequest) string {
		if !strings.Contains(req.Query, "r1: repository(owner: $o1, name: $n1)") {
			t.Errorf("query missing alias r1:\n%s", req.Query)
		}
		if req.Variables["o1"] != "other" || req.Variables["n1"] != "missing" {
			t.Errorf("variables = %v", req.Variables)
		}
		return `{"data":{"r0":{"nameWithOwner":"golang/go","name":"go","owner":{"login":"golang"},"pullRequests":{"totalCount":0,"nodes":[]}},"r1":null},
"errors":[{"type":"NOT_FOUND","path":["r1"],"message":"Could not resolve to a Repository with the name 'other/missing'."}]}`
	})

	repos, errs := c.Repos(context.Background(), []string{"golang/go", "other/missing"})
	if len(repos) != 1 || repos[0].NameWithOwner != "golang/go" {
		t.Errorf("repos = %+v", repos)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "other/missing: Could not resolve") {
		t.Errorf("errs = %v", errs)
	}
}

func TestHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := New(srv.URL, "bad").Viewer(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Bad credentials") {
		t.Errorf("err = %v", err)
	}
}

func TestAuthoredPRs(t *testing.T) {
	c := fakeServer(t, func(req gqlRequest) string {
		q, _ := req.Variables["q"].(string)
		if !strings.Contains(q, "is:pr is:open author:me") || !strings.Contains(q, "is:public") || strings.Contains(q, "archived:") {
			t.Errorf("search query = %q", q)
		}
		if req.Variables["cursor"] == "c1" {
			return `{"data":{"search":{"pageInfo":{"hasNextPage":false},"nodes":[
 {"number":3,"title":"Second","url":"u3","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z",
  "author":{"login":"me","__typename":"User"},"repository":{"nameWithOwner":"b/two","url":"https://github.com/b/two"},"commits":{"nodes":[]}}
]}}}`
		}
		return `{"data":{"rateLimit":{"remaining":4000,"resetAt":"2026-01-01T00:00:00Z"},"search":{"pageInfo":{"hasNextPage":true,"endCursor":"c1"},"nodes":[
 {"number":9,"title":"First","url":"u9","isDraft":true,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-02T00:00:00Z",
  "reviewDecision":"APPROVED","author":{"login":"me","__typename":"User"},"repository":{"nameWithOwner":"a/one","url":"https://github.com/a/one"},
  "commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"PENDING"}}}]}},
 {}
]}}}`
	})

	prs, err := c.AuthoredPRs(context.Background(), "me", OwnerOptions{IncludeArchived: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 2 {
		t.Fatalf("prs = %+v", prs)
	}
	if p := prs[0]; p.Repo != "a/one" || p.RepoURL != "https://github.com/a/one" || p.Number != 9 || !p.Draft || p.CIStatus != "PENDING" || p.ReviewDecision != "APPROVED" {
		t.Errorf("first = %+v", p)
	}
	if prs[1].Repo != "b/two" || prs[1].Number != 3 {
		t.Errorf("second = %+v", prs[1])
	}
	if c.RateLimit().Remaining != 4000 {
		t.Errorf("rate limit = %+v", c.RateLimit())
	}
}
