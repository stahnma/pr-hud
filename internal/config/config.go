// Package config loads pr-hud settings from the environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	defaultGraphQLURL = "https://api.github.com/graphql"
	defaultRefresh    = 15 * time.Minute
	minRefresh        = time.Minute
)

// Config holds every runtime setting. All values come from environment variables.
type Config struct {
	Token      string
	GraphQLURL string

	// User whose repos are listed. Empty means the owner of Token.
	User string

	// ExtraRepos are explicit "owner/name" repos to watch.
	ExtraRepos []string
	// ExtraOwners come from "owner/*" entries: every repo of that owner is watched.
	ExtraOwners []string

	IncludePrivate  bool
	IncludeArchived bool
	IncludeForks    bool

	RefreshInterval time.Duration
	ListenAddr      string
	LogLevel        slog.Level
}

// FromEnv builds a Config using getenv (normally os.Getenv).
func FromEnv(getenv func(string) string) (Config, error) {
	c := Config{
		Token:           strings.TrimSpace(getenv("GITHUB_TOKEN")),
		GraphQLURL:      orDefault(getenv("GITHUB_GRAPHQL_URL"), defaultGraphQLURL),
		User:            strings.TrimSpace(getenv("PRHUD_USER")),
		RefreshInterval: defaultRefresh,
	}

	var errs []error
	if c.Token == "" {
		errs = append(errs, errors.New("GITHUB_TOKEN is required"))
	}

	boolVar := func(dst *bool, key string, def bool) {
		v, err := parseBool(getenv(key), def)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", key, err))
		}
		*dst = v
	}
	boolVar(&c.IncludePrivate, "PRHUD_INCLUDE_PRIVATE", true)
	boolVar(&c.IncludeArchived, "PRHUD_INCLUDE_ARCHIVED", true)
	boolVar(&c.IncludeForks, "PRHUD_INCLUDE_FORKS", true)

	if v := strings.TrimSpace(getenv("PRHUD_REFRESH_INTERVAL")); v != "" {
		d, err := time.ParseDuration(v)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("PRHUD_REFRESH_INTERVAL: %w", err))
		case d < minRefresh:
			errs = append(errs, fmt.Errorf("PRHUD_REFRESH_INTERVAL: must be at least %s", minRefresh))
		default:
			c.RefreshInterval = d
		}
	}

	repos, owners, err := parseExtras(getenv("PRHUD_EXTRA_REPOS"))
	if err != nil {
		errs = append(errs, fmt.Errorf("PRHUD_EXTRA_REPOS: %w", err))
	}
	c.ExtraRepos, c.ExtraOwners = repos, owners

	c.ListenAddr = net.JoinHostPort(
		orDefault(getenv("LISTEN_ADDR"), "127.0.0.1"),
		orDefault(getenv("PORT"), "8080"),
	)

	if v := strings.TrimSpace(getenv("LOG_LEVEL")); v != "" {
		if err := c.LogLevel.UnmarshalText([]byte(v)); err != nil {
			errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
		}
	}

	return c, errors.Join(errs...)
}

// parseExtras splits a comma/whitespace separated list of "owner/name" and
// "owner/*" entries.
func parseExtras(s string) (repos, owners []string, err error) {
	var errs []error
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
	for _, f := range fields {
		owner, name, ok := strings.Cut(f, "/")
		if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
			errs = append(errs, fmt.Errorf("%q is not owner/repo or owner/*", f))
			continue
		}
		if name == "*" {
			owners = append(owners, owner)
		} else {
			repos = append(repos, owner+"/"+name)
		}
	}
	return repos, owners, errors.Join(errs...)
}

func parseBool(s string, def bool) (bool, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(s)
	if err != nil {
		return def, fmt.Errorf("%q is not a boolean", s)
	}
	return b, nil
}

func orDefault(s, def string) string {
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return def
}
