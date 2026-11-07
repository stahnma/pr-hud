// Package hud keeps an in-memory snapshot of repos and open PRs, refreshed
// from GitHub on an interval.
package hud

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/stahnma/pr-hud/internal/github"
)

// Repo sources.
const (
	SourceOwned = "owned"
	SourceExtra = "extra"
)

const (
	refreshTimeout = 2 * time.Minute
	// minRefreshGap stops manual refreshes from hammering the API: a request
	// arriving within this long of the last completed refresh reuses it.
	minRefreshGap = 10 * time.Second
)

// Fetcher is the subset of the GitHub client the store needs.
type Fetcher interface {
	Viewer(ctx context.Context) (string, error)
	OwnerRepos(ctx context.Context, login string, opt github.OwnerOptions) ([]github.Repo, error)
	Repos(ctx context.Context, names []string) ([]github.Repo, []error)
	AuthoredPRs(ctx context.Context, login string, opt github.OwnerOptions) ([]github.RepoPR, error)
	RateLimit() github.RateLimit
}

// Options configures what the store fetches.
type Options struct {
	User        string // empty means the token's owner
	ExtraOwners []string
	ExtraRepos  []string
	Owner       github.OwnerOptions
	Interval    time.Duration
}

// Snapshot is the result of one successful refresh.
type Snapshot struct {
	User      string
	FetchedAt time.Time
	Repos     []github.Repo
	// Authored are open PRs opened by User, in any repo.
	Authored []github.RepoPR
	// Errors are non-fatal problems, such as an extra repo that wasn't found.
	Errors    []string
	RateLimit github.RateLimit
}

// Store holds the latest snapshot.
type Store struct {
	gh  Fetcher
	opt Options
	log *slog.Logger

	refreshMu sync.Mutex // serializes refreshes
	viewer    string     // cached token owner; guarded by refreshMu

	mu       sync.RWMutex
	snap     *Snapshot
	lastErr  error
	lastDone time.Time
}

// NewStore returns a store that has not fetched anything yet.
func NewStore(gh Fetcher, opt Options, log *slog.Logger) *Store {
	return &Store{gh: gh, opt: opt, log: log}
}

// State returns the latest snapshot (nil before the first successful refresh)
// and the error from the most recent refresh, if it failed.
func (s *Store) State() (*Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap, s.lastErr
}

// Run refreshes immediately and then on every interval until ctx is done.
func (s *Store) Run(ctx context.Context) {
	t := time.NewTicker(s.opt.Interval)
	defer t.Stop()
	for {
		s.Refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Refresh fetches fresh data from GitHub. Concurrent callers wait for the
// in-flight refresh and share its result. On failure the previous snapshot
// is kept.
func (s *Store) Refresh(ctx context.Context) error {
	requested := time.Now()
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	s.mu.RLock()
	lastDone, lastErr := s.lastDone, s.lastErr
	s.mu.RUnlock()
	if lastDone.After(requested.Add(-minRefreshGap)) {
		return lastErr
	}

	start := time.Now()
	snap, err := s.fetch(ctx)

	s.mu.Lock()
	s.lastDone, s.lastErr = time.Now(), err
	if err == nil {
		s.snap = snap
	}
	s.mu.Unlock()

	if err != nil {
		s.log.Error("refresh failed", "err", err)
		return err
	}
	open := 0
	for _, r := range snap.Repos {
		open += r.OpenPRCount
	}
	s.log.Info("refreshed",
		"repos", len(snap.Repos),
		"open_prs", open,
		"authored_prs", len(snap.Authored),
		"problems", len(snap.Errors),
		"took", time.Since(start).Round(time.Millisecond),
		"rate_limit_remaining", snap.RateLimit.Remaining)
	for _, e := range snap.Errors {
		s.log.Warn("repo problem", "err", e)
	}
	return nil
}

func (s *Store) fetch(ctx context.Context) (*Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, refreshTimeout)
	defer cancel()

	login := s.opt.User
	if login == "" {
		if s.viewer == "" {
			v, err := s.gh.Viewer(ctx)
			if err != nil {
				return nil, fmt.Errorf("looking up token owner: %w", err)
			}
			s.viewer = v
		}
		login = s.viewer
	}

	snap := &Snapshot{User: login}
	seen := map[string]bool{}
	add := func(repos []github.Repo, source string) {
		for _, r := range repos {
			key := strings.ToLower(r.NameWithOwner)
			if seen[key] {
				continue
			}
			seen[key] = true
			r.Source = source
			snap.Repos = append(snap.Repos, r)
		}
	}

	owned, err := s.gh.OwnerRepos(ctx, login, s.opt.Owner)
	if err != nil {
		return nil, fmt.Errorf("listing repos for %s: %w", login, err)
	}
	add(owned, SourceOwned)

	for _, owner := range s.opt.ExtraOwners {
		if strings.EqualFold(owner, login) {
			continue
		}
		repos, err := s.gh.OwnerRepos(ctx, owner, s.opt.Owner)
		if err != nil {
			snap.Errors = append(snap.Errors, fmt.Sprintf("%s/*: %v", owner, err))
			continue
		}
		add(repos, SourceExtra)
	}

	var extras []string
	for _, name := range s.opt.ExtraRepos {
		if !seen[strings.ToLower(name)] {
			extras = append(extras, name)
		}
	}
	if len(extras) > 0 {
		repos, errs := s.gh.Repos(ctx, extras)
		add(repos, SourceExtra)
		for _, e := range errs {
			snap.Errors = append(snap.Errors, e.Error())
		}
	}

	authored, err := s.gh.AuthoredPRs(ctx, login, s.opt.Owner)
	if err != nil {
		snap.Errors = append(snap.Errors, fmt.Sprintf("pull requests opened by %s: %v", login, err))
	}
	snap.Authored = authored

	snap.FetchedAt = time.Now()
	snap.RateLimit = s.gh.RateLimit()
	return snap, nil
}
