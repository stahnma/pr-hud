package hud

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/stahnma/pr-hud/internal/github"
)

// Summary holds headline counts for a View.
type Summary struct {
	Repos        int `json:"repos"`
	ReposWithPRs int `json:"repos_with_prs"`
	OpenPRs      int `json:"open_prs"`
	// AuthoredPRs is the number of open PRs opened by the user, in any repo.
	AuthoredPRs int `json:"authored_prs"`
}

// View is a snapshot arranged for display: repos with open PRs first, most
// recently active first, then the quiet ones alphabetically.
type View struct {
	User      string
	FetchedAt time.Time
	Summary   Summary
	Active    []github.Repo
	Quiet     []github.Repo
	// Authored are open PRs the user opened, most recently updated first.
	Authored  []FlatPR
	Errors    []string
	RateLimit github.RateLimit
}

// FlatPR is a pull request tagged with its repo, for the flat PR lists.
type FlatPR = github.RepoPR

// BuildView arranges s for display.
func BuildView(s *Snapshot) View {
	v := View{
		User:      s.User,
		FetchedAt: s.FetchedAt,
		Errors:    s.Errors,
		RateLimit: s.RateLimit,
	}
	for _, r := range s.Repos {
		for _, p := range r.PRs {
			if p.UpdatedAt.After(r.LastPRActivity) {
				r.LastPRActivity = p.UpdatedAt
			}
		}
		if r.OpenPRCount > 0 {
			v.Active = append(v.Active, r)
			v.Summary.OpenPRs += r.OpenPRCount
		} else {
			v.Quiet = append(v.Quiet, r)
		}
	}
	v.Summary.Repos = len(s.Repos)
	v.Summary.ReposWithPRs = len(v.Active)

	v.Authored = append([]FlatPR{}, s.Authored...)
	v.Summary.AuthoredPRs = len(v.Authored)
	slices.SortStableFunc(v.Authored, newestFirst)

	slices.SortFunc(v.Active, func(a, b github.Repo) int {
		if c := b.LastPRActivity.Compare(a.LastPRActivity); c != 0 {
			return c
		}
		return byName(a, b)
	})
	slices.SortFunc(v.Quiet, byName)
	return v
}

// PRs returns every PR in the view, most recently updated first.
func (v View) PRs() []FlatPR {
	prs := []FlatPR{}
	for _, r := range v.Active {
		for _, p := range r.PRs {
			prs = append(prs, FlatPR{Repo: r.NameWithOwner, RepoURL: r.URL, PullRequest: p})
		}
	}
	slices.SortStableFunc(prs, newestFirst)
	return prs
}

func newestFirst(a, b FlatPR) int { return b.UpdatedAt.Compare(a.UpdatedAt) }

func byName(a, b github.Repo) int {
	return cmp.Compare(strings.ToLower(a.NameWithOwner), strings.ToLower(b.NameWithOwner))
}
