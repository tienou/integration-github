package github

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"

	git "github.com/go-git/go-git/v5"
	"github.com/gofrs/flock"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// GithubImporter backs up all the repositories of a GitHub organization
// (or user, or a single repository) as bare git mirrors, plus a JSON
// dump of each repository's API metadata.
//
// Locations:
//
//	github://<org>            all repositories of an org or user
//	github://<org>/<repo>     a single repository
//
// Configuration:
//
//	token            GitHub token (falls back to $GITHUB_TOKEN)
//	api_url          API base URL, for GitHub Enterprise Server
//	                 (default https://api.github.com)
//	cache_dir        persistent directory for the git mirrors; reused
//	                 across runs so only new objects are fetched
//	                 (default: a throwaway temp dir)
//	include_forks    back up forked repositories (default true)
//	include_archived back up archived repositories (default true)
//	metadata         dump per-repo API metadata JSON (default true)
type GithubImporter struct {
	opts  *connectors.Options
	owner string
	repo  string

	token  string
	app    *appAuth
	apiURL string
	origin string

	cacheDir  string
	keepCache bool

	includeForks    bool
	includeArchived bool
	withMetadata    bool

	client *http.Client
}

func init() {
	importer.Register("github", 0, NewGithubImporter)
}

func configBool(config map[string]string, key string, def bool) (bool, error) {
	value, ok := config[key]
	if !ok || value == "" {
		return def, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return def, fmt.Errorf("invalid value for %s: %q", key, value)
	}
	return parsed, nil
}

func NewGithubImporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (importer.Importer, error) {
	target := strings.TrimPrefix(config["location"], "github://")
	target = strings.Trim(target, "/")
	if target == "" {
		return nil, fmt.Errorf("missing organization in location, expected github://<org>[/<repo>]")
	}

	parts := strings.SplitN(target, "/", 2)
	owner := parts[0]
	repo := ""
	if len(parts) == 2 {
		repo = parts[1]
	}

	token := config["token"]
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}

	apiURL := strings.TrimSuffix(config["api_url"], "/")
	if apiURL == "" {
		apiURL = "https://api.github.com"
	}
	parsed, err := url.Parse(apiURL)
	if err != nil {
		return nil, fmt.Errorf("invalid api_url: %w", err)
	}
	origin := strings.TrimPrefix(parsed.Host, "api.")

	includeForks, err := configBool(config, "include_forks", true)
	if err != nil {
		return nil, err
	}
	includeArchived, err := configBool(config, "include_archived", true)
	if err != nil {
		return nil, err
	}
	withMetadata, err := configBool(config, "metadata", true)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 5 * time.Minute}

	var app *appAuth
	if appID := config["app_id"]; appID != "" {
		pemData := config["private_key_pem"]
		if pemData == "" {
			path := config["private_key"]
			if path == "" {
				return nil, fmt.Errorf("app_id given but no private_key (path) or private_key_pem")
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("reading private_key: %w", err)
			}
			pemData = string(raw)
		}
		app, err = newAppAuth(appID, config["installation_id"], pemData, apiURL, owner, client)
		if err != nil {
			return nil, err
		}
	}

	cacheDir := config["cache_dir"]
	keepCache := cacheDir != ""
	if cacheDir == "" {
		cacheDir, err = os.MkdirTemp("", "plakar-github-")
		if err != nil {
			return nil, err
		}
	} else {
		if err := os.MkdirAll(cacheDir, 0o700); err != nil {
			return nil, err
		}
	}

	return &GithubImporter{
		opts:            opts,
		owner:           owner,
		repo:            repo,
		token:           token,
		app:             app,
		apiURL:          apiURL,
		origin:          origin,
		cacheDir:        cacheDir,
		keepCache:       keepCache,
		includeForks:    includeForks,
		includeArchived: includeArchived,
		withMetadata:    withMetadata,
		client:          client,
	}, nil
}

// bearer returns the credential to authenticate API calls and git
// operations: the App installation token (refreshed as needed) when App
// auth is configured, the static token otherwise, or "" for anonymous
// access to public repositories.
func (g *GithubImporter) bearer(ctx context.Context) (string, error) {
	if g.app != nil {
		return g.app.currentToken(ctx)
	}
	return g.token, nil
}

func (g *GithubImporter) Root() string          { return "/" }
func (g *GithubImporter) Origin() string        { return g.origin }
func (g *GithubImporter) Type() string          { return "github" }
func (g *GithubImporter) Flags() location.Flags { return 0 }

func (g *GithubImporter) Ping(ctx context.Context) error {
	if g.app != nil {
		_, err := g.apiRequest(ctx, "/installation/repositories?per_page=1")
		return err
	}
	if g.repo != "" {
		_, err := g.apiRequest(ctx, "/repos/"+url.PathEscape(g.owner)+"/"+url.PathEscape(g.repo))
		return err
	}
	if _, err := g.apiRequest(ctx, "/orgs/"+url.PathEscape(g.owner)); err != nil {
		if _, userErr := g.apiRequest(ctx, "/users/"+url.PathEscape(g.owner)); userErr != nil {
			return err
		}
	}
	return nil
}

func (g *GithubImporter) Close(ctx context.Context) error {
	if !g.keepCache {
		return os.RemoveAll(g.cacheDir)
	}
	return nil
}

func dirRecord(pathname string, modTime time.Time) *connectors.Record {
	return connectors.NewRecord(pathname, "", objects.FileInfo{
		Lname:    filepath.Base(pathname),
		Lmode:    os.ModeDir | 0o755,
		LmodTime: modTime,
	}, nil, nil)
}

func (g *GithubImporter) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	repos, err := g.listRepos(ctx)
	if err != nil {
		return fmt.Errorf("listing repositories of %s: %w", g.owner, err)
	}

	now := time.Now()
	records <- dirRecord("/", now)
	if g.withMetadata {
		records <- dirRecord("/_metadata", now)
	}

	workers := g.opts.MaxConcurrency
	if workers < 1 {
		workers = 1
	}
	if workers > 4 {
		workers = 4
	}

	jobs := make(chan *Repository)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for repo := range jobs {
				if err := g.backupRepository(ctx, repo, records); err != nil {
					records <- connectors.NewError("/"+repo.Name+".git", err)
				}
			}
		}()
	}

	for _, repo := range repos {
		select {
		case jobs <- repo:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()

	return ctx.Err()
}

func (g *GithubImporter) backupRepository(ctx context.Context, repo *Repository, records chan<- *connectors.Record) error {
	dest := filepath.Join(g.cacheDir, repo.Name+".git")

	// plakar runs two instances of the importer for a single backup
	// (scan and content passes); serialize clone/fetch/walk of each
	// repository so both don't mutate the same mirror concurrently
	lock := flock.New(filepath.Join(g.cacheDir, "."+repo.Name+".flock"))
	if err := lock.Lock(); err != nil {
		return err
	}
	defer lock.Unlock()

	if err := g.mirror(ctx, repo, dest); err != nil {
		return err
	}

	if g.withMetadata && len(repo.raw) > 0 {
		raw := repo.raw
		records <- connectors.NewRecord("/_metadata/"+repo.Name+".json", "", objects.FileInfo{
			Lname:    repo.Name + ".json",
			Lsize:    int64(len(raw)),
			Lmode:    0o644,
			LmodTime: time.Now(),
		}, nil, func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(raw)), nil
		})
	}

	prefix := "/" + repo.Name + ".git"
	return filepath.WalkDir(dest, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			records <- connectors.NewError(path, err)
			return nil
		}

		entrypath := prefix
		if path != dest {
			rel, err := filepath.Rel(dest, path)
			if err != nil {
				records <- connectors.NewError(path, err)
				return nil
			}
			entrypath = prefix + "/" + filepath.ToSlash(rel)
		}

		// go-git leaves transient files around during clone/fetch;
		// they are not part of the repository state
		base := filepath.Base(path)
		if !d.IsDir() && (strings.HasPrefix(base, "tmp_pack_") ||
			strings.HasPrefix(base, "tmp_obj_") ||
			strings.HasSuffix(base, ".lock")) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			records <- connectors.NewError(path, err)
			return nil
		}

		fileinfo := objects.FileInfoFromStat(info)
		var open func() (io.ReadCloser, error)
		if info.Mode().IsRegular() {
			localpath := path
			open = func() (io.ReadCloser, error) {
				return os.Open(localpath)
			}
		}
		records <- connectors.NewRecord(entrypath, "", fileinfo, nil, open)
		return nil
	})
}

// mirror clones repo into dest as a bare mirror, or fetches updates if
// dest already holds a previous clone. A corrupted or unfetchable cache
// entry is discarded and cloned from scratch.
func (g *GithubImporter) mirror(ctx context.Context, repo *Repository, dest string) error {
	var auth transport.AuthMethod
	if cred, err := g.bearer(ctx); err != nil {
		return err
	} else if cred != "" {
		auth = &githttp.BasicAuth{Username: "x-access-token", Password: cred}
	}

	if _, err := os.Stat(dest); err == nil {
		existing, err := git.PlainOpen(dest)
		if err == nil {
			err = existing.FetchContext(ctx, &git.FetchOptions{
				RemoteName: "origin",
				RefSpecs:   []gitconfig.RefSpec{"+refs/*:refs/*"},
				Auth:       auth,
				Force:      true,
				Prune:      true,
				Tags:       git.AllTags,
			})
			if err == nil || err == git.NoErrAlreadyUpToDate {
				return nil
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := os.RemoveAll(dest); err != nil {
			return err
		}
	}

	_, err := git.PlainCloneContext(ctx, dest, true, &git.CloneOptions{
		URL:    repo.CloneURL,
		Auth:   auth,
		Mirror: true,
	})
	if err == transport.ErrEmptyRemoteRepository {
		// keep a placeholder so the repository still shows up in
		// the snapshot and can be recreated on restore
		_, err = git.PlainInit(dest, true)
	}
	if err != nil {
		return fmt.Errorf("cloning %s: %w", repo.FullName, err)
	}
	return nil
}
