# Plakar GitHub integration

Source connector (importer) that backs up all repositories of a GitHub
organization or user — or a single repository — into a Plakar kloset.

Each repository is captured as a **bare git mirror** (`<repo>.git/`),
i.e. the full history: every branch, tag and ref. The repository's API
metadata is also stored as JSON under `_metadata/<repo>.json`.

## Locations

```
github://<org>            all repositories of an org or user
github://<org>/<repo>     a single repository
```

## Configuration

| Key                | Default                  | Description                                        |
|--------------------|--------------------------|----------------------------------------------------|
| `token`            | `$GITHUB_TOKEN`          | GitHub token (fine-grained PAT, read-only is fine) |
| `api_url`          | `https://api.github.com` | API base URL, for GitHub Enterprise Server         |
| `cache_dir`        | throwaway temp dir       | persistent mirror cache → incremental fetches      |
| `include_forks`    | `true`                   | include forked repositories                        |
| `include_archived` | `true`                   | include archived repositories                      |
| `metadata`         | `true`                   | dump per-repo API metadata JSON                    |

## Usage

```sh
# build & install the plugin
make install

# register the source once
plakar source add ghorg github://MY_ORG \
    token=github_pat_XXXX \
    cache_dir=/var/cache/plakar-github

# back up
plakar at /path/to/kloset backup @ghorg

# restore (bare repos are written to disk)
plakar at /path/to/kloset restore -to /tmp/restore <snapshot>
cd /tmp/restore/.../myrepo.git
git push --mirror https://github.com/MY_ORG/myrepo.git
```

## Required token permissions

Fine-grained PAT scoped to the organization with **Contents: read**
and **Metadata: read** on all repositories.

## Build

Pure Go (go-git), no external `git` binary needed:

```sh
go build -o github-importer ./importer
```
