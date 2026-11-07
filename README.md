# pr-hud

A small daemon that shows which of your GitHub repositories have open pull
requests. Repos with open PRs are listed first and the rest are folded away.
When nothing is open you get an "All clear 🎉".

The **Opened by me** tab (`/authored`) shows the other direction: every open
pull request you've opened, in any repo, most recently updated first.

It runs as a single binary. Configuration comes from the environment and,
if present, a `.env` file in the working directory (real environment
variables win). The cache is in memory, and logs go to stdout.

## Run

```sh
cp .env.example .env   # set GITHUB_TOKEN in it
make run
# open http://127.0.0.1:8080
```

`make install` puts the binary in `~/.local/bin` (override with `PREFIX=`).
Run `make help` to see every target.

A read-only fine-grained token is enough. Give it **Metadata**, **Pull
requests**, and **Commit statuses** read access on the repos you want to see.
The **Opened by me** tab covers PRs in public repos and in private repos the
token can read.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `GITHUB_TOKEN` | *(required)* | GitHub token |
| `PRHUD_USER` | token owner | User whose repos are listed, and whose PRs show under **Opened by me** |
| `PRHUD_EXTRA_REPOS` | | Extra repos to watch, comma or space separated: `owner/repo`, or `org/*` for every repo an owner has |
| `PRHUD_INCLUDE_PRIVATE` | `true` | Include private repos |
| `PRHUD_INCLUDE_ARCHIVED` | `true` | Include archived repos |
| `PRHUD_INCLUDE_FORKS` | `true` | Include forks |
| `PRHUD_REFRESH_INTERVAL` | `15m` | How often to poll GitHub (minimum `1m`) |
| `GITHUB_GRAPHQL_URL` | `https://api.github.com/graphql` | GraphQL endpoint; for GHE use `https://HOST/api/graphql` |
| `LISTEN_ADDR` | `127.0.0.1` | Bind address |
| `PORT` | `8080` | Bind port |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

See `.env.example`, or run `./pr-hud --help`.

The **Refresh now** button refreshes on demand. Requests that arrive within
10 seconds of the last refresh reuse its result. A refresh costs roughly one
GraphQL point per 25 repos, plus one per 50 PRs you've opened.

## API

All responses are JSON. Bot-authored PRs (Dependabot, Renovate, …) are
treated like any other PR; `author_is_bot` just labels them. Until the first
fetch finishes, the list endpoints return `503`.

| Endpoint | Description |
|---|---|
| `GET /api/v1/repos` | Every repo with its open PRs. Active repos come first, most recent activity first. `?has_prs=true` returns only repos with open PRs. |
| `GET /api/v1/prs` | Every open PR across all repos in one flat list, most recently updated first. |
| `GET /api/v1/authored` | Every open PR opened by the user, in any repo, most recently updated first. |
| `POST /api/v1/refresh` | Refresh from GitHub now. |
| `GET /healthz` | Liveness, plus whether data has been loaded yet. |

The list endpoints include `user`, `fetched_at`, `summary`, `rate_limit`, and
`errors` (watched repos that couldn't be loaded). Each repo lists up to 25 PRs.
`open_pr_count` is the real total, and `prs_truncated` tells you when there are
more. PR fields: `number`, `title`, `url`, `author`, `author_is_bot`, `draft`,
`created_at`, `updated_at`, `review_decision` (`APPROVED`,
`CHANGES_REQUESTED`, `REVIEW_REQUIRED`), and `ci_status` (`SUCCESS`,
`FAILURE`, `ERROR`, `PENDING`, `EXPECTED`). The last two are left out when
empty.

```sh
curl -s localhost:8080/api/v1/prs | jq -r '.prs[] | "\(.repo)#\(.number) \(.title)"'
```

## Development

```sh
make check   # gofmt check, go vet, go test
```

Dependencies are standard library only.
