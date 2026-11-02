// Command pr-hud is a small daemon that shows which of your GitHub repos have
// open pull requests, as a web page and a JSON API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/stahnma/pr-hud/internal/config"
	"github.com/stahnma/pr-hud/internal/github"
	"github.com/stahnma/pr-hud/internal/hud"
	"github.com/stahnma/pr-hud/internal/web"
)

const usage = `pr-hud: a heads-up display for open pull requests.

All configuration comes from environment variables:

  GITHUB_TOKEN            (required) GitHub token with read access to repos and PRs
  PRHUD_USER              user whose repos to list (default: the token's owner)
  PRHUD_EXTRA_REPOS       extra repos to watch: "owner/repo,org/*" (comma or space separated)
  PRHUD_INCLUDE_PRIVATE   include private repos (default: true)
  PRHUD_INCLUDE_ARCHIVED  include archived repos (default: true)
  PRHUD_INCLUDE_FORKS     include forks (default: true)
  PRHUD_REFRESH_INTERVAL  how often to poll GitHub, min 1m (default: 15m)
  GITHUB_GRAPHQL_URL      GraphQL endpoint (default: https://api.github.com/graphql)
  LISTEN_ADDR             address to bind (default: 127.0.0.1)
  PORT                    port to bind (default: 8080)
  LOG_LEVEL               debug, info, warn, or error (default: info)
`

func main() {
	if len(os.Args) > 1 {
		fmt.Print(usage)
		if a := os.Args[1]; a == "-h" || a == "--help" || a == "help" {
			return
		}
		os.Exit(2)
	}
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.FromEnv(os.Getenv)
	if err != nil {
		return fmt.Errorf("configuration:\n%w", err)
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store := hud.NewStore(github.New(cfg.GraphQLURL, cfg.Token), hud.Options{
		User:        cfg.User,
		ExtraOwners: cfg.ExtraOwners,
		ExtraRepos:  cfg.ExtraRepos,
		Owner: github.OwnerOptions{
			IncludePrivate:  cfg.IncludePrivate,
			IncludeArchived: cfg.IncludeArchived,
			IncludeForks:    cfg.IncludeForks,
		},
		Interval: cfg.RefreshInterval,
	}, log)
	go store.Run(ctx)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           web.New(store, log).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("listening", "addr", "http://"+cfg.ListenAddr, "refresh_interval", cfg.RefreshInterval,
		"extra_repos", len(cfg.ExtraRepos), "extra_owners", len(cfg.ExtraOwners))

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
