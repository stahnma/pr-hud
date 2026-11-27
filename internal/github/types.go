package github

import (
	"strings"
	"time"
)

// Repo is a repository and its open pull requests.
type Repo struct {
	NameWithOwner string `json:"name_with_owner"`
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	URL           string `json:"url"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
	Fork          bool   `json:"fork"`
	// Source is "owned" or "extra"; set by the caller that collected the repo.
	Source         string    `json:"source"`
	PushedAt       time.Time `json:"pushed_at,omitzero"`
	LastPRActivity time.Time `json:"last_pr_activity,omitzero"`

	// DefaultBranch is the CI state of the default branch's head commit; nil
	// for an empty repo.
	DefaultBranch *BranchCI `json:"default_branch,omitempty"`

	// OpenPRCount is the total number of open PRs, which may exceed len(PRs).
	OpenPRCount  int           `json:"open_pr_count"`
	PRsTruncated bool          `json:"prs_truncated"`
	PRs          []PullRequest `json:"prs"`
}

// MorePRs is the number of open PRs that were not fetched.
func (r Repo) MorePRs() int { return r.OpenPRCount - len(r.PRs) }

// PullRequest is an open pull request.
type PullRequest struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	Author      string    `json:"author"`
	AuthorIsBot bool      `json:"author_is_bot"`
	Draft       bool      `json:"draft"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	// ReviewDecision is APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED, or empty.
	ReviewDecision string `json:"review_decision,omitempty"`
	// CIStatus is the head commit's check rollup: SUCCESS, FAILURE, ERROR,
	// PENDING, EXPECTED, or empty when there are no checks.
	CIStatus string `json:"ci_status,omitempty"`
}

// CI states, normalized across check runs and commit statuses.
const (
	CISuccess = "SUCCESS"
	CIFailure = "FAILURE"
	CIPending = "PENDING"
	// CINeutral covers runs that neither passed nor failed: cancelled,
	// skipped, neutral, or stale.
	CINeutral = "NEUTRAL"
)

// BranchCI is the CI state of a branch's head commit.
type BranchCI struct {
	Branch      string    `json:"branch"`
	CommitURL   string    `json:"commit_url,omitempty"`
	CommittedAt time.Time `json:"committed_at,omitzero"`
	// State is the worst of Checks: FAILURE, PENDING, SUCCESS, or empty
	// when nothing has run.
	State  string  `json:"state,omitempty"`
	Checks []Check `json:"checks"`
	// ChecksHidden is set when GitHub withheld some check suites from the
	// token (fine-grained tokens can't read them on private repos). State
	// then comes from the commit's overall check rollup, and Checks lists
	// only what was visible.
	ChecksHidden bool `json:"checks_hidden,omitempty"`
}

// Failing returns the checks that failed.
func (b BranchCI) Failing() []Check { return b.withState(CIFailure) }

// Pending returns the checks that are still running.
func (b BranchCI) Pending() []Check { return b.withState(CIPending) }

func (b BranchCI) withState(state string) []Check {
	var out []Check
	for _, c := range b.Checks {
		if c.State == state {
			out = append(out, c)
		}
	}
	return out
}

// Check is the latest result of one workflow, app, or commit status context
// on a commit.
type Check struct {
	// Name is the workflow name, the app name for non-Actions checks, or the
	// commit status context.
	Name string `json:"name"`
	// Event is what triggered an Actions workflow: push, schedule, dynamic
	// (Dependabot updates, CodeQL default setup), workflow_dispatch, ….
	Event string `json:"event,omitempty"`
	// State is SUCCESS, FAILURE, PENDING, or NEUTRAL.
	State string `json:"state"`
	// Conclusion is GitHub's raw result, e.g. TIMED_OUT or CANCELLED.
	Conclusion string    `json:"conclusion,omitempty"`
	URL        string    `json:"url,omitempty"`
	At         time.Time `json:"at"`
}

// RepoPR is a pull request tagged with the repo it belongs to.
type RepoPR struct {
	Repo    string `json:"repo"`
	RepoURL string `json:"repo_url,omitempty"`
	PullRequest
}

// RateLimit is the GraphQL rate limit as of the last query.
type RateLimit struct {
	Remaining int       `json:"remaining"`
	ResetAt   time.Time `json:"reset_at"`
}

// GraphQL response shapes. encoding/json matches field names case-insensitively.

type gqlRateLimit struct {
	Remaining int
	ResetAt   time.Time
}

type gqlRepo struct {
	NameWithOwner    string
	Name             string
	URL              string
	IsPrivate        bool
	IsArchived       bool
	IsFork           bool
	PushedAt         time.Time
	Owner            struct{ Login string }
	DefaultBranchRef *gqlRef
	PullRequests     struct {
		TotalCount int
		Nodes      []gqlPR
	}
}

type gqlPR struct {
	Number         int
	Title          string
	URL            string
	IsDraft        bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ReviewDecision string
	// Repository is only selected by the search query.
	Repository *struct {
		NameWithOwner string
		URL           string
	}
	Author *struct {
		Login    string
		Typename string `json:"__typename"`
	}
	Commits struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct{ State string }
			}
		}
	}
}

type gqlRef struct {
	Name   string
	Target struct {
		URL           string
		CommittedDate time.Time
		// StatusCheckRollup is the overall state of every check and status
		// on the commit. It stays readable when check suites are not.
		StatusCheckRollup *struct{ State string }
		Status            *struct {
			Contexts []struct {
				Context   string
				State     string
				TargetURL string `json:"targetUrl"`
				CreatedAt time.Time
			}
		}
		CheckSuites struct {
			// A nil node is a suite the token isn't allowed to read.
			Nodes []*gqlCheckSuite
		}
	}
}

type gqlCheckSuite struct {
	Status     string
	Conclusion string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	URL        string
	App        *struct{ Name string }
	// WorkflowRun is set for GitHub Actions suites.
	WorkflowRun *struct {
		Event    string
		URL      string
		Workflow struct{ Name string }
	}
}

// parallelRunWindow groups runs of the same workflow and event that started
// this close to the newest one. Dependabot starts one "Dependabot Updates"
// run per ecosystem at once, all with the same name; any of them failing
// should count.
const parallelRunWindow = 10 * time.Minute

func (g *gqlRef) toBranchCI() *BranchCI {
	if g == nil {
		return nil
	}
	b := &BranchCI{
		Branch:      g.Name,
		CommitURL:   g.Target.URL,
		CommittedAt: g.Target.CommittedDate,
		Checks:      []Check{},
	}

	// Suites come oldest first. Keep the newest run of each workflow (or
	// app), plus any that ran in parallel with it.
	type key struct{ name, event string }
	groups := map[key][]Check{}
	var order []key
	for _, s := range g.Target.CheckSuites.Nodes {
		if s == nil {
			b.ChecksHidden = true
			continue
		}
		// Apps create a queued suite on every push whether or not they
		// ever run anything; skip those. (Asking for checkRuns.totalCount
		// instead would triple the query's cost.)
		if s.WorkflowRun == nil && s.Status == "QUEUED" {
			continue
		}
		c := Check{State: suiteState(s.Status, s.Conclusion), Conclusion: s.Conclusion, URL: s.URL, At: s.CreatedAt}
		switch {
		case s.WorkflowRun != nil:
			c.Name, c.Event, c.URL = s.WorkflowRun.Workflow.Name, s.WorkflowRun.Event, s.WorkflowRun.URL
		case s.App != nil:
			c.Name = s.App.Name
		default:
			c.Name = "checks"
		}
		k := key{c.Name, c.Event}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], c)
	}
	for _, k := range order {
		runs := groups[k]
		newest := runs[0]
		for _, r := range runs[1:] {
			if !r.At.Before(newest.At) {
				newest = r
			}
		}
		chosen := newest
		for _, r := range runs {
			if newest.At.Sub(r.At) <= parallelRunWindow && severity(r.State) > severity(chosen.State) {
				chosen = r
			}
		}
		b.Checks = append(b.Checks, chosen)
	}

	if g.Target.Status != nil {
		for _, s := range g.Target.Status.Contexts {
			b.Checks = append(b.Checks, Check{Name: s.Context, State: statusState(s.State), Conclusion: s.State, URL: s.TargetURL, At: s.CreatedAt})
		}
	}

	for _, c := range b.Checks {
		if b.State == "" || severity(c.State) > severity(b.State) {
			b.State = c.State
		}
	}
	// Hidden suites may hold the failure; the rollup covers them.
	if r := g.Target.StatusCheckRollup; b.ChecksHidden && r != nil {
		if s := statusState(r.State); b.State == "" || severity(s) > severity(b.State) {
			b.State = s
		}
	}
	if b.State == CINeutral {
		b.State = CISuccess // nothing failed or is running
	}
	return b
}

// severity orders states so the worst one wins.
func severity(state string) int {
	switch state {
	case CIFailure:
		return 3
	case CIPending:
		return 2
	case CISuccess:
		return 1
	}
	return 0
}

func suiteState(status, conclusion string) string {
	if status != "COMPLETED" {
		return CIPending
	}
	switch conclusion {
	case "SUCCESS":
		return CISuccess
	case "FAILURE", "TIMED_OUT", "STARTUP_FAILURE", "ACTION_REQUIRED":
		return CIFailure
	}
	return CINeutral // CANCELLED, SKIPPED, NEUTRAL, STALE
}

func statusState(state string) string {
	switch state {
	case "SUCCESS":
		return CISuccess
	case "FAILURE", "ERROR":
		return CIFailure
	case "PENDING", "EXPECTED":
		return CIPending
	}
	return CINeutral
}

func (g gqlRepo) toRepo() Repo {
	r := Repo{
		NameWithOwner: g.NameWithOwner,
		Owner:         g.Owner.Login,
		Name:          g.Name,
		URL:           g.URL,
		Private:       g.IsPrivate,
		Archived:      g.IsArchived,
		Fork:          g.IsFork,
		PushedAt:      g.PushedAt,
		DefaultBranch: g.DefaultBranchRef.toBranchCI(),
		OpenPRCount:   g.PullRequests.TotalCount,
		PRs:           make([]PullRequest, 0, len(g.PullRequests.Nodes)),
	}
	for _, p := range g.PullRequests.Nodes {
		r.PRs = append(r.PRs, p.toPR())
	}
	r.PRsTruncated = r.OpenPRCount > len(r.PRs)
	return r
}

func (g gqlPR) toPR() PullRequest {
	p := PullRequest{
		Number:         g.Number,
		Title:          g.Title,
		URL:            g.URL,
		Author:         "ghost", // GitHub's name for deleted accounts
		Draft:          g.IsDraft,
		CreatedAt:      g.CreatedAt,
		UpdatedAt:      g.UpdatedAt,
		ReviewDecision: g.ReviewDecision,
	}
	if g.Author != nil {
		p.Author = g.Author.Login
		p.AuthorIsBot = g.Author.Typename == "Bot" || strings.HasSuffix(g.Author.Login, "[bot]")
	}
	if n := g.Commits.Nodes; len(n) > 0 && n[0].Commit.StatusCheckRollup != nil {
		p.CIStatus = n[0].Commit.StatusCheckRollup.State
	}
	return p
}
