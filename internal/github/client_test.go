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
  "defaultBranchRef":{"name":"main","target":{"checkSuites":{"nodes":[{"status":"COMPLETED","conclusion":"FAILURE","workflowRun":{"event":"schedule","workflow":{"name":"nightly"}}}]}}},
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
	if a.DefaultBranch == nil || a.DefaultBranch.State != CIFailure || a.DefaultBranch.Checks[0].Event != "schedule" {
		t.Errorf("default branch = %+v", a.DefaultBranch)
	}
	if repos[1].DefaultBranch != nil {
		t.Errorf("repo without defaultBranchRef has CI %+v", repos[1].DefaultBranch)
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

func TestBranchCI(t *testing.T) {
	const raw = `{"name":"main","target":{"url":"https://github.com/me/a/commit/abc","committedDate":"2026-01-01T00:00:00Z",
 "status":{"contexts":[{"context":"ci/circleci","state":"SUCCESS","targetUrl":"https://circleci.example/1","createdAt":"2026-01-01T00:05:00Z"}]},
 "checkSuites":{"nodes":[
  {"status":"QUEUED","conclusion":null,"createdAt":"2026-01-01T00:00:01Z","url":"s0","app":{"name":"Fly.io"},"workflowRun":null},
  {"status":"COMPLETED","conclusion":"SUCCESS","createdAt":"2026-01-01T00:00:02Z","url":"s1","app":{"name":"GitHub Actions"},
   "workflowRun":{"event":"push","url":"run1","workflow":{"name":"CI"}}},
  {"status":"COMPLETED","conclusion":"FAILURE","createdAt":"2026-01-02T00:00:00Z","url":"s2","app":{"name":"GitHub Actions"},
   "workflowRun":{"event":"schedule","url":"run2","workflow":{"name":"nightly"}}},
  {"status":"COMPLETED","conclusion":"SUCCESS","createdAt":"2026-01-03T00:00:00Z","url":"s3","app":{"name":"GitHub Actions"},
   "workflowRun":{"event":"schedule","url":"run3","workflow":{"name":"nightly"}}},
  {"status":"COMPLETED","conclusion":"TIMED_OUT","createdAt":"2026-01-04T00:00:00Z","url":"s4","app":{"name":"GitHub Actions"},
   "workflowRun":{"event":"dynamic","url":"run4","workflow":{"name":"Dependabot Updates"}}},
  {"status":"COMPLETED","conclusion":"SUCCESS","createdAt":"2026-01-04T00:00:01Z","url":"s5","app":{"name":"GitHub Actions"},
   "workflowRun":{"event":"dynamic","url":"run5","workflow":{"name":"Dependabot Updates"}}},
  {"status":"COMPLETED","conclusion":"CANCELLED","createdAt":"2026-01-04T00:00:00Z","url":"s6","app":{"name":"GitHub Actions"},
   "workflowRun":{"event":"workflow_dispatch","url":"run6","workflow":{"name":"deploy"}}}
 ]}}}`
	var ref gqlRef
	if err := json.Unmarshal([]byte(raw), &ref); err != nil {
		t.Fatal(err)
	}
	b := ref.toBranchCI()
	if b.Branch != "main" || b.CommitURL != "https://github.com/me/a/commit/abc" || b.CommittedAt.IsZero() {
		t.Errorf("branch = %+v", b)
	}
	got := map[string]Check{}
	for _, c := range b.Checks {
		got[c.Name+"/"+c.Event] = c
	}
	if len(b.Checks) != 5 {
		t.Fatalf("checks = %+v (want the queued app suite dropped and one per workflow)", b.Checks)
	}
	if c := got["CI/push"]; c.State != CISuccess || c.URL != "run1" {
		t.Errorf("CI = %+v", c)
	}
	// A newer scheduled success clears an older scheduled failure.
	if c := got["nightly/schedule"]; c.State != CISuccess || c.URL != "run3" {
		t.Errorf("nightly = %+v", c)
	}
	// Dependabot runs started together: any failure counts.
	if c := got["Dependabot Updates/dynamic"]; c.State != CIFailure || c.Conclusion != "TIMED_OUT" || c.URL != "run4" {
		t.Errorf("dependabot = %+v", c)
	}
	if c := got["deploy/workflow_dispatch"]; c.State != CINeutral {
		t.Errorf("cancelled = %+v", c)
	}
	if c := got["ci/circleci/"]; c.State != CISuccess || c.URL != "https://circleci.example/1" {
		t.Errorf("status context = %+v", c)
	}
	if b.State != CIFailure || len(b.Failing()) != 1 || len(b.Pending()) != 0 {
		t.Errorf("state = %s, failing = %+v", b.State, b.Failing())
	}
}

func TestBranchCIStates(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
	}{
		{`{"name":"main","target":{}}`, ""},
		{`{"name":"main","target":{"checkSuites":{"nodes":[{"status":"COMPLETED","conclusion":"SKIPPED","workflowRun":{"workflow":{"name":"x"}}}]}}}`, CISuccess},
		{`{"name":"main","target":{"checkSuites":{"nodes":[{"status":"IN_PROGRESS","workflowRun":{"workflow":{"name":"x"}}},
		  {"status":"COMPLETED","conclusion":"SUCCESS","workflowRun":{"workflow":{"name":"y"}}}]}}}`, CIPending},
		{`{"name":"main","target":{"status":{"contexts":[{"context":"c","state":"ERROR"}]}}}`, CIFailure},
	} {
		var ref gqlRef
		if err := json.Unmarshal([]byte(tc.raw), &ref); err != nil {
			t.Fatal(err)
		}
		if got := ref.toBranchCI().State; got != tc.want {
			t.Errorf("%s: state = %q, want %q", tc.raw, got, tc.want)
		}
	}
	if (*gqlRef)(nil).toBranchCI() != nil {
		t.Error("empty repo should have no branch CI")
	}
}

func TestGQLErrorStringIncludesPath(t *testing.T) {
	e := gqlError{
		Message: "Resource not accessible by personal access token",
		Path:    []any{"repositoryOwner", "repositories", "nodes", float64(6), "defaultBranchRef"},
	}
	want := "Resource not accessible by personal access token (at repositoryOwner.repositories.nodes.6.defaultBranchRef)"
	if got := e.String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	if got := (gqlError{Message: "boom"}).String(); got != "boom" {
		t.Errorf("String() without path = %q, want %q", got, "boom")
	}
}

func TestBranchCIHiddenSuitesUseRollup(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
	}{
		{"hidden failure", `{"name":"main","target":{"statusCheckRollup":{"state":"FAILURE"},
		  "checkSuites":{"nodes":[null,null]}}}`, CIFailure},
		{"hidden pending beats visible success", `{"name":"main","target":{"statusCheckRollup":{"state":"PENDING"},
		  "checkSuites":{"nodes":[null,{"status":"COMPLETED","conclusion":"SUCCESS","workflowRun":{"workflow":{"name":"x"}}}]}}}`, CIPending},
		{"hidden success", `{"name":"main","target":{"statusCheckRollup":{"state":"SUCCESS"},
		  "checkSuites":{"nodes":[null]}}}`, CISuccess},
	} {
		var ref gqlRef
		if err := json.Unmarshal([]byte(tc.raw), &ref); err != nil {
			t.Fatal(err)
		}
		b := ref.toBranchCI()
		if b.State != tc.want || !b.ChecksHidden {
			t.Errorf("%s: state = %q hidden = %v, want %q hidden", tc.name, b.State, b.ChecksHidden, tc.want)
		}
		for _, c := range b.Checks {
			if c.Name == "" {
				t.Errorf("%s: hidden suite became a check: %+v", tc.name, c)
			}
		}
	}

	// With every suite visible the rollup is not consulted.
	var ref gqlRef
	json.Unmarshal([]byte(`{"name":"main","target":{"statusCheckRollup":{"state":"FAILURE"},
	  "checkSuites":{"nodes":[{"status":"COMPLETED","conclusion":"SUCCESS","workflowRun":{"workflow":{"name":"x"}}}]}}}`), &ref)
	if b := ref.toBranchCI(); b.ChecksHidden || b.State != CISuccess {
		t.Errorf("visible suites: state = %q hidden = %v", b.State, b.ChecksHidden)
	}
}

func TestOwnerReposToleratesHiddenCheckSuites(t *testing.T) {
	const hidden = `{"data":{"repositoryOwner":{"repositories":{"pageInfo":{"hasNextPage":false},"nodes":[
 {"nameWithOwner":"me/secret","name":"secret","isPrivate":true,"owner":{"login":"me"},
  "defaultBranchRef":{"name":"main","target":{"statusCheckRollup":{"state":"FAILURE"},"checkSuites":{"nodes":[null]}}},
  "pullRequests":{"totalCount":0,"nodes":[]}}]}}},
"errors":[{"type":"FORBIDDEN","message":"Resource not accessible by personal access token",
  "path":["repositoryOwner","repositories","nodes",0,"defaultBranchRef","target","checkSuites","nodes",0]}]}`
	c := fakeServer(t, func(gqlRequest) string { return hidden })
	repos, err := c.OwnerRepos(context.Background(), "me", OwnerOptions{IncludePrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].DefaultBranch == nil || repos[0].DefaultBranch.State != CIFailure || !repos[0].DefaultBranch.ChecksHidden {
		t.Errorf("repos = %+v", repos)
	}

	// Any other field error still fails the refresh, naming the field.
	other := strings.Replace(hidden, `,"target","checkSuites","nodes",0]`, `]`, 1)
	c = fakeServer(t, func(gqlRequest) string { return other })
	if _, err := c.OwnerRepos(context.Background(), "me", OwnerOptions{IncludePrivate: true}); err == nil ||
		!strings.Contains(err.Error(), "(at repositoryOwner.repositories.nodes.0.defaultBranchRef)") {
		t.Errorf("err = %v", err)
	}
}
