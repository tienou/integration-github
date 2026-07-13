# Plakar GitHub integration

A [Plakar](https://github.com/PlakarKorp/plakar) source connector (importer)
that backs up all repositories of a GitHub organization or user — or a single
repository — into a Kloset store.

Each repository is captured as a **bare git mirror** (`<repo>.git/`): the full
history, every branch, tag and ref. The repository's GitHub API metadata is
also stored as JSON under `_metadata/<repo>.json`.

Pure Go (built on [go-git](https://github.com/go-git/go-git)) — no `git`
binary required at runtime. Works with github.com and GitHub Enterprise
Server.

## Locations

```
github://<org>            all repositories of an org or user
github://<org>/<repo>     a single repository
```

## Configuration

| Key                | Default                  | Description                                          |
|--------------------|--------------------------|------------------------------------------------------|
| `token`            | `$GITHUB_TOKEN`          | GitHub token; omit for public repositories           |
| `api_url`          | `https://api.github.com` | API base URL, for GitHub Enterprise Server           |
| `cache_dir`        | throwaway temp dir       | persistent mirror cache, enables incremental fetches |
| `include_forks`    | `true`                   | include forked repositories                          |
| `include_archived` | `true`                   | include archived repositories                        |
| `metadata`         | `true`                   | dump per-repo API metadata JSON                      |

For private repositories, use a fine-grained PAT with **Contents: read** and
**Metadata: read** (or a classic PAT with the `repo` scope).

## Usage

```sh
# register the source once
plakar source add myorg github://MY_ORG \
    token=github_pat_XXXX \
    cache_dir=/var/cache/plakar-github

# back up
plakar at /path/to/kloset backup @myorg

# or ad hoc, without a configured source
plakar at /path/to/kloset backup -o token=github_pat_XXXX github://MY_ORG
```

### Restoring

`plakar restore` writes the bare mirrors back to disk; push one to recreate a
repository:

```sh
plakar at /path/to/kloset restore -to /tmp/restore <snapshot>
cd /tmp/restore/*/myrepo.git
git push --mirror https://github.com/MY_ORG/myrepo.git
```

## Build and install

```sh
make            # builds the github-importer binary (CGO_ENABLED=0 recommended)
make install    # packages with `plakar pkg create` and installs with `plakar pkg add`
```

Requires Go ≥ 1.25 and Plakar ≥ v1.1.x (plugin API v1.1.0).

## Implementation notes

- With a persistent `cache_dir`, each run only fetches new git objects
  (`+refs/*:refs/*`, forced, pruned), so backups after the first one are
  fast and cheap.
- Plakar runs two instances of the importer for a single backup (scan and
  content passes): mirror updates are serialized with a per-repository
  file lock so both instances can share the cache safely.
- go-git's transient files (`tmp_pack_*`, `*.lock`) are excluded from
  snapshots.
- Empty repositories are kept as empty bare repos so they still appear in
  the snapshot.

## Roadmap

- optional issues / pull requests / releases metadata dumps
- exporter (restore by pushing mirrors back to GitHub directly)

## License

ISC — see [LICENSE](LICENSE).
