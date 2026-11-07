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
	NameWithOwner string
	Name          string
	URL           string
	IsPrivate     bool
	IsArchived    bool
	IsFork        bool
	PushedAt      time.Time
	Owner         struct{ Login string }
	PullRequests  struct {
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
