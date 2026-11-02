package hud

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stahnma/pr-hud/internal/github"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func pr(n int, bot bool, updated time.Duration) github.PullRequest {
	return github.PullRequest{Number: n, AuthorIsBot: bot, UpdatedAt: t0.Add(updated)}
}

func repo(name string, prs ...github.PullRequest) github.Repo {
	return github.Repo{NameWithOwner: name, OpenPRCount: len(prs), PRs: prs}
}

func names(rs []github.Repo) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.NameWithOwner)
	}
	return out
}

func TestBuildViewOrdering(t *testing.T) {
	snap := &Snapshot{Repos: []github.Repo{
		repo("me/zeta"),
		repo("me/old", pr(1, false, time.Hour)),
		repo("me/Alpha"),
		repo("me/new", pr(2, false, 3*time.Hour), pr(3, false, 2*time.Hour)),
		repo("me/botonly", pr(4, true, 5*time.Hour)),
	}}

	v := BuildView(snap)
	if got, want := names(v.Active), []string{"me/botonly", "me/new", "me/old"}; !slices.Equal(got, want) {
		t.Errorf("active = %v, want %v", got, want)
	}
	if got, want := names(v.Quiet), []string{"me/Alpha", "me/zeta"}; !slices.Equal(got, want) {
		t.Errorf("quiet = %v, want %v", got, want)
	}
	if v.Summary != (Summary{Repos: 5, ReposWithPRs: 3, OpenPRs: 4}) {
		t.Errorf("summary = %+v", v.Summary)
	}
	if prs := v.PRs(); len(prs) != 4 || prs[0].Number != 4 || prs[0].Repo != "me/botonly" {
		t.Errorf("flat PRs = %+v", prs)
	}
}

type fakeFetcher struct {
	owners map[string][]github.Repo
	calls  int
}

func (f *fakeFetcher) Viewer(context.Context) (string, error) { return "me", nil }
func (f *fakeFetcher) RateLimit() github.RateLimit            { return github.RateLimit{Remaining: 42} }

func (f *fakeFetcher) OwnerRepos(_ context.Context, login string, _ github.OwnerOptions) ([]github.Repo, error) {
	f.calls++
	rs, ok := f.owners[login]
	if !ok {
		return nil, errors.New("no such owner")
	}
	return rs, nil
}

func (f *fakeFetcher) Repos(_ context.Context, names []string) ([]github.Repo, []error) {
	var rs []github.Repo
	var errs []error
	for _, n := range names {
		if n == "x/missing" {
			errs = append(errs, errors.New(n+": not found"))
			continue
		}
		rs = append(rs, repo(n))
	}
	return rs, errs
}

func TestStoreRefresh(t *testing.T) {
	f := &fakeFetcher{owners: map[string][]github.Repo{
		"me":  {repo("me/a")},
		"org": {repo("org/b"), repo("me/a")},
	}}
	s := NewStore(f, Options{
		ExtraOwners: []string{"org", "ghost"},
		ExtraRepos:  []string{"ME/A", "x/c", "x/missing"},
		Interval:    time.Hour,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	snap, err := s.State()
	if err != nil || snap == nil {
		t.Fatalf("State() = %v, %v", snap, err)
	}
	if snap.User != "me" || snap.RateLimit.Remaining != 42 {
		t.Errorf("snap = %+v", snap)
	}
	if got, want := names(snap.Repos), []string{"me/a", "org/b", "x/c"}; !slices.Equal(got, want) {
		t.Errorf("repos = %v, want %v", got, want)
	}
	if snap.Repos[0].Source != SourceOwned || snap.Repos[1].Source != SourceExtra {
		t.Errorf("sources = %q, %q", snap.Repos[0].Source, snap.Repos[1].Source)
	}
	if len(snap.Errors) != 2 || !strings.HasPrefix(snap.Errors[0], "ghost/*") || !strings.Contains(snap.Errors[1], "x/missing") {
		t.Errorf("errors = %v", snap.Errors)
	}

	// A second refresh right away is coalesced and doesn't hit GitHub.
	calls := f.calls
	s.Refresh(context.Background())
	if f.calls != calls {
		t.Errorf("refresh within %s was not coalesced", minRefreshGap)
	}
}

func TestStoreKeepsSnapshotOnFailure(t *testing.T) {
	f := &fakeFetcher{owners: map[string][]github.Repo{"me": {repo("me/a")}}}
	s := NewStore(f, Options{Interval: time.Hour}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	delete(f.owners, "me")
	s.lastDone = time.Time{} // bypass the coalescing window
	if err := s.Refresh(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	snap, err := s.State()
	if snap == nil || len(snap.Repos) != 1 || err == nil {
		t.Errorf("State() = %v, %v; want old snapshot plus error", snap, err)
	}
}
