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
	// CIFailingRepos counts repos whose default branch is failing CI, and
	// FailingPRs counts failing PRs on watched repos opened by someone else.
	CIFailingRepos int `json:"ci_failing_repos"`
	FailingPRs     int `json:"failing_prs"`
}

// CIProblems is the count shown on the CI tab.
func (s Summary) CIProblems() int { return s.CIFailingRepos + s.FailingPRs }

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
	CI        CIView
	Errors    []string
	RateLimit github.RateLimit
}

// FlatPR is a pull request tagged with its repo, for the flat PR lists.
type FlatPR = github.RepoPR

// CIView is the CI health of the watched repos. Archived repos are left out
// since nothing runs in them any more.
type CIView struct {
	// Failing and Pending are repos whose default branch is failing or still
	// running CI, most recent first.
	Failing []github.Repo `json:"failing"`
	Pending []github.Repo `json:"pending"`
	// FailingPRs are open PRs on watched repos with failing CI, opened by
	// someone other than the user (bots included), most recently updated
	// first. The user's own PRs are on the authored tab.
	FailingPRs []FlatPR `json:"failing_prs"`
	// Passing repos have green default branches; NoCI repos have no checks
	// on their default branch head.
	Passing []github.Repo `json:"passing"`
	NoCI    []github.Repo `json:"no_ci"`
}

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

	v.CI = buildCI(s.User, s.Repos)
	v.Summary.CIFailingRepos = len(v.CI.Failing)
	v.Summary.FailingPRs = len(v.CI.FailingPRs)
	return v
}

func buildCI(user string, repos []github.Repo) CIView {
	ci := CIView{Failing: []github.Repo{}, Pending: []github.Repo{}, FailingPRs: []FlatPR{}, Passing: []github.Repo{}, NoCI: []github.Repo{}}
	for _, r := range repos {
		if r.Archived {
			continue
		}
		for _, p := range r.PRs {
			if (p.CIStatus == "FAILURE" || p.CIStatus == "ERROR") && !strings.EqualFold(p.Author, user) {
				ci.FailingPRs = append(ci.FailingPRs, FlatPR{Repo: r.NameWithOwner, RepoURL: r.URL, PullRequest: p})
			}
		}
		state := ""
		if r.DefaultBranch != nil {
			state = r.DefaultBranch.State
		}
		switch state {
		case github.CIFailure:
			ci.Failing = append(ci.Failing, r)
		case github.CIPending:
			ci.Pending = append(ci.Pending, r)
		case github.CISuccess:
			ci.Passing = append(ci.Passing, r)
		default:
			ci.NoCI = append(ci.NoCI, r)
		}
	}
	slices.SortStableFunc(ci.FailingPRs, newestFirst)
	slices.SortFunc(ci.Failing, latestCheckFirst)
	slices.SortFunc(ci.Pending, latestCheckFirst)
	slices.SortFunc(ci.Passing, byName)
	slices.SortFunc(ci.NoCI, byName)
	return ci
}

func latestCheckFirst(a, b github.Repo) int {
	if c := lastCheck(b).Compare(lastCheck(a)); c != 0 {
		return c
	}
	return byName(a, b)
}

func lastCheck(r github.Repo) time.Time {
	var t time.Time
	if r.DefaultBranch != nil {
		for _, c := range r.DefaultBranch.Checks {
			if c.At.After(t) {
				t = c.At
			}
		}
	}
	return t
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
