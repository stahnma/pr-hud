// Package github is a small GitHub GraphQL client that fetches repositories
// together with their open pull requests.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	userAgent = "pr-hud"
	// repoBatchSize is how many explicitly named repos are fetched per query.
	repoBatchSize = 25
)

// repoFragment selects everything pr-hud shows for a repo. Up to 25 PRs are
// fetched per repo; totalCount reports the real number.
const repoFragment = `
fragment RepoFields on Repository {
  nameWithOwner name url isPrivate isArchived isFork pushedAt
  owner { login }
  pullRequests(states: OPEN, first: 25, orderBy: {field: UPDATED_AT, direction: DESC}) {
    totalCount
    nodes {
      number title url isDraft createdAt updatedAt reviewDecision
      author { login __typename }
      commits(last: 1) { nodes { commit { statusCheckRollup { state } } } }
    }
  }
}`

const ownerReposQuery = `
query($login: String!, $cursor: String, $isFork: Boolean, $privacy: RepositoryPrivacy) {
  rateLimit { remaining resetAt }
  repositoryOwner(login: $login) {
    repositories(first: 50, after: $cursor, ownerAffiliations: [OWNER], isFork: $isFork, privacy: $privacy) {
      pageInfo { hasNextPage endCursor }
      nodes { ...RepoFields }
    }
  }
}` + repoFragment

// Client talks to the GitHub GraphQL API.
type Client struct {
	url   string
	token string
	http  *http.Client

	mu        sync.Mutex
	rateLimit RateLimit
}

// New returns a client for the GraphQL endpoint at url.
func New(url, token string) *Client {
	return &Client{
		url:   url,
		token: token,
		http:  &http.Client{Timeout: 60 * time.Second},
	}
}

// OwnerOptions filters the repos returned by OwnerRepos.
type OwnerOptions struct {
	IncludePrivate  bool
	IncludeArchived bool
	IncludeForks    bool
}

// RateLimit returns the rate limit reported by the most recent query.
func (c *Client) RateLimit() RateLimit {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rateLimit
}

// Viewer returns the login of the token's owner.
func (c *Client) Viewer(ctx context.Context) (string, error) {
	var data struct{ Viewer struct{ Login string } }
	errs, err := c.query(ctx, `query { viewer { login } }`, nil, &data)
	if err != nil {
		return "", err
	}
	if len(errs) > 0 {
		return "", fmt.Errorf("github: %s", errs[0].Message)
	}
	return data.Viewer.Login, nil
}

// OwnerRepos lists every repo owned by login (a user or an org).
func (c *Client) OwnerRepos(ctx context.Context, login string, opt OwnerOptions) ([]Repo, error) {
	vars := map[string]any{"login": login}
	if !opt.IncludeForks {
		vars["isFork"] = false
	}
	if !opt.IncludePrivate {
		vars["privacy"] = "PUBLIC"
	}

	var repos []Repo
	for {
		var data struct {
			RateLimit       *gqlRateLimit
			RepositoryOwner *struct {
				Repositories struct {
					PageInfo struct {
						HasNextPage bool
						EndCursor   string
					}
					Nodes []gqlRepo
				}
			}
		}
		errs, err := c.query(ctx, ownerReposQuery, vars, &data)
		if err != nil {
			return nil, err
		}
		if len(errs) > 0 {
			return nil, fmt.Errorf("github: %s", errs[0].Message)
		}
		c.noteRateLimit(data.RateLimit)
		if data.RepositoryOwner == nil {
			return nil, fmt.Errorf("github: owner %q not found", login)
		}
		conn := data.RepositoryOwner.Repositories
		for _, n := range conn.Nodes {
			if n.IsArchived && !opt.IncludeArchived {
				continue
			}
			repos = append(repos, n.toRepo())
		}
		if !conn.PageInfo.HasNextPage {
			return repos, nil
		}
		vars["cursor"] = conn.PageInfo.EndCursor
	}
}

// Repos fetches explicitly named "owner/name" repos. Repos that cannot be
// fetched are reported in the returned errors; the rest are still returned.
func (c *Client) Repos(ctx context.Context, names []string) ([]Repo, []error) {
	var repos []Repo
	var errs []error
	for start := 0; start < len(names); start += repoBatchSize {
		r, e := c.repoBatch(ctx, names[start:min(start+repoBatchSize, len(names))])
		repos = append(repos, r...)
		errs = append(errs, e...)
	}
	return repos, errs
}

func (c *Client) repoBatch(ctx context.Context, names []string) ([]Repo, []error) {
	var decls []string
	var fields strings.Builder
	vars := make(map[string]any, 2*len(names))
	for i, full := range names {
		owner, name, _ := strings.Cut(full, "/")
		decls = append(decls, fmt.Sprintf("$o%d: String!, $n%d: String!", i, i))
		fmt.Fprintf(&fields, "  r%d: repository(owner: $o%d, name: $n%d) { ...RepoFields }\n", i, i, i)
		vars[fmt.Sprintf("o%d", i)] = owner
		vars[fmt.Sprintf("n%d", i)] = name
	}
	q := fmt.Sprintf("query(%s) {\n  rateLimit { remaining resetAt }\n%s}\n%s",
		strings.Join(decls, ", "), fields.String(), repoFragment)

	var data map[string]json.RawMessage
	gqlErrs, err := c.query(ctx, q, vars, &data)
	if err != nil {
		return nil, []error{fmt.Errorf("%s: %w", strings.Join(names, ", "), err)}
	}

	if raw, ok := data["rateLimit"]; ok {
		var rl gqlRateLimit
		if json.Unmarshal(raw, &rl) == nil {
			c.noteRateLimit(&rl)
		}
	}

	// Map each error back to the aliased field it belongs to.
	errByAlias := map[string]string{}
	for _, e := range gqlErrs {
		if len(e.Path) == 0 {
			continue
		}
		if alias, ok := e.Path[0].(string); ok {
			if _, seen := errByAlias[alias]; !seen {
				errByAlias[alias] = e.Message
			}
		}
	}

	var repos []Repo
	var errs []error
	for i, full := range names {
		alias := fmt.Sprintf("r%d", i)
		raw := data[alias]
		if len(raw) == 0 || string(raw) == "null" {
			msg := errByAlias[alias]
			if msg == "" {
				msg = "not found or not accessible"
			}
			errs = append(errs, fmt.Errorf("%s: %s", full, msg))
			continue
		}
		var g gqlRepo
		if err := json.Unmarshal(raw, &g); err != nil {
			errs = append(errs, fmt.Errorf("%s: decoding: %w", full, err))
			continue
		}
		repos = append(repos, g.toRepo())
	}
	return repos, errs
}

type gqlError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Path    []any  `json:"path"`
}

// query runs a GraphQL query and decodes "data" into out. GraphQL errors that
// came back alongside data are returned for the caller to judge.
func (c *Client) query(ctx context.Context, q string, vars map[string]any, out any) ([]gqlError, error) {
	body, err := json.Marshal(map[string]any{"query": q, "variables": vars})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("github: %s: %s", resp.Status, bytes.TrimSpace(snippet))
	}

	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []gqlError      `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("github: decoding response: %w", err)
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		if len(env.Errors) > 0 {
			return nil, fmt.Errorf("github: %s", env.Errors[0].Message)
		}
		return nil, errors.New("github: empty response")
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return nil, fmt.Errorf("github: decoding data: %w", err)
	}
	return env.Errors, nil
}

func (c *Client) noteRateLimit(rl *gqlRateLimit) {
	if rl == nil {
		return
	}
	c.mu.Lock()
	c.rateLimit = RateLimit{Remaining: rl.Remaining, ResetAt: rl.ResetAt}
	c.mu.Unlock()
}
