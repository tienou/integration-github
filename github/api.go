package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Repository is the subset of the GitHub API repository object we care
// about, plus the raw JSON kept verbatim for the metadata dump.
type Repository struct {
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	CloneURL      string `json:"clone_url"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	Fork          bool   `json:"fork"`
	Archived      bool   `json:"archived"`
	Size          int64  `json:"size"`

	raw json.RawMessage
}

func (g *GithubImporter) apiRequest(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.apiURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if cred, err := g.bearer(ctx); err != nil {
		return nil, err
	} else if cred != "" {
		req.Header.Set("Authorization", "Bearer "+cred)
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api %s: %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func parseRepos(data []byte) ([]*Repository, error) {
	var raws []json.RawMessage
	if err := json.Unmarshal(data, &raws); err != nil {
		return nil, err
	}
	repos := make([]*Repository, 0, len(raws))
	for _, raw := range raws {
		repo := &Repository{}
		if err := json.Unmarshal(raw, repo); err != nil {
			return nil, err
		}
		repo.raw = raw
		repos = append(repos, repo)
	}
	return repos, nil
}

// listRepos enumerates all repositories of the configured owner, or the
// single configured repository. Tries the org endpoint first then falls
// back to the user endpoint.
func (g *GithubImporter) listRepos(ctx context.Context) ([]*Repository, error) {
	if g.repo != "" {
		raw, err := g.apiRequest(ctx, "/repos/"+url.PathEscape(g.owner)+"/"+url.PathEscape(g.repo))
		if err != nil {
			return nil, err
		}
		repo := &Repository{}
		if err := json.Unmarshal(raw, repo); err != nil {
			return nil, err
		}
		repo.raw = raw
		return []*Repository{repo}, nil
	}

	var all []*Repository
	var err error
	if g.app != nil {
		// installation tokens list exactly the repositories the App
		// installation was granted access to
		all, err = g.paginateInstallation(ctx)
		if err != nil {
			return nil, err
		}
	} else {
		all, err = g.paginate(ctx, "/orgs/"+url.PathEscape(g.owner)+"/repos?type=all&per_page=100&page=")
		if err != nil {
			var userErr error
			all, userErr = g.paginate(ctx, "/users/"+url.PathEscape(g.owner)+"/repos?type=all&per_page=100&page=")
			if userErr != nil {
				return nil, err
			}
		}
	}

	filtered := all[:0]
	for _, repo := range all {
		if repo.Fork && !g.includeForks {
			continue
		}
		if repo.Archived && !g.includeArchived {
			continue
		}
		filtered = append(filtered, repo)
	}
	return filtered, nil
}

func (g *GithubImporter) paginateInstallation(ctx context.Context) ([]*Repository, error) {
	var all []*Repository
	for page := 1; ; page++ {
		data, err := g.apiRequest(ctx, "/installation/repositories?per_page=100&page="+strconv.Itoa(page))
		if err != nil {
			return nil, err
		}
		var wrapper struct {
			Repositories []json.RawMessage `json:"repositories"`
		}
		if err := json.Unmarshal(data, &wrapper); err != nil {
			return nil, err
		}
		for _, raw := range wrapper.Repositories {
			repo := &Repository{}
			if err := json.Unmarshal(raw, repo); err != nil {
				return nil, err
			}
			repo.raw = raw
			all = append(all, repo)
		}
		if len(wrapper.Repositories) < 100 {
			break
		}
	}
	return all, nil
}

func (g *GithubImporter) paginate(ctx context.Context, endpoint string) ([]*Repository, error) {
	var all []*Repository
	for page := 1; ; page++ {
		data, err := g.apiRequest(ctx, endpoint+strconv.Itoa(page))
		if err != nil {
			return nil, err
		}
		repos, err := parseRepos(data)
		if err != nil {
			return nil, err
		}
		all = append(all, repos...)
		if len(repos) < 100 {
			break
		}
	}
	return all, nil
}
