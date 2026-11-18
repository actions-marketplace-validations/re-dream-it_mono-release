// Package forge creates releases on GitHub and GitLab for tags that already exist.
package forge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Client creates a release page for a tag.
type Client interface {
	// CreateRelease creates a release for tag. created is false when the release already exists.
	CreateRelease(tag, title, body string, prerelease bool) (created bool, err error)
	// Name is "GitHub" or "GitLab", for messages.
	Name() string
}

// ErrNoToken is returned by Detect when the platform is known but no token is available.
type ErrNoToken struct{ Platform, Hint string }

func (e ErrNoToken) Error() string {
	return fmt.Sprintf("no %s token found (%s)", e.Platform, e.Hint)
}

// Detect picks the client for a repository from its web URL ("https://github.com/org/repo"),
// reading tokens and API addresses from the environment (getenv is os.Getenv in production).
// MONO_RELEASE_FORGE=github|gitlab overrides the detection for self-hosted installations.
func Detect(webURL string, getenv func(string) string) (Client, error) {
	u, err := url.Parse(webURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("cannot work out the repository from the remote URL %q", webURL)
	}
	path := strings.Trim(u.Path, "/")
	kind := strings.ToLower(getenv("MONO_RELEASE_FORGE"))
	if kind == "" {
		switch {
		case strings.Contains(u.Host, "github"):
			kind = "github"
		case strings.Contains(u.Host, "gitlab") || getenv("CI_API_V4_URL") != "" || getenv("GITLAB_TOKEN") != "":
			kind = "gitlab"
		default:
			return nil, fmt.Errorf("cannot tell whether %s is GitHub or GitLab; set MONO_RELEASE_FORGE=github or gitlab", u.Host)
		}
	}
	hc := &http.Client{Timeout: 30 * time.Second}

	switch kind {
	case "github":
		token := firstNonEmpty(getenv("GITHUB_TOKEN"), getenv("GH_TOKEN"))
		if token == "" {
			return nil, ErrNoToken{"GitHub", "set GITHUB_TOKEN"}
		}
		api := getenv("GITHUB_API_URL")
		if api == "" {
			if u.Host == "github.com" {
				api = "https://api.github.com"
			} else {
				api = u.Scheme + "://" + u.Host + "/api/v3" // GitHub Enterprise Server
			}
		}
		return &github{api: strings.TrimRight(api, "/"), repo: path, token: token, hc: hc}, nil
	case "gitlab":
		g := &gitlab{hc: hc}
		switch {
		case getenv("GITLAB_TOKEN") != "":
			g.header, g.token = "PRIVATE-TOKEN", getenv("GITLAB_TOKEN")
		case getenv("CI_JOB_TOKEN") != "":
			g.header, g.token = "JOB-TOKEN", getenv("CI_JOB_TOKEN")
		default:
			return nil, ErrNoToken{"GitLab", "set GITLAB_TOKEN, or run inside GitLab CI where CI_JOB_TOKEN is provided"}
		}
		g.api = getenv("CI_API_V4_URL")
		if g.api == "" {
			g.api = u.Scheme + "://" + u.Host + "/api/v4"
		}
		g.api = strings.TrimRight(g.api, "/")
		g.project = getenv("CI_PROJECT_ID")
		if g.project == "" {
			g.project = url.PathEscape(path)
		}
		return g, nil
	}
	return nil, fmt.Errorf("MONO_RELEASE_FORGE must be github or gitlab, got %q", kind)
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// FromEnv is Detect with the real environment.
func FromEnv(webURL string) (Client, error) { return Detect(webURL, os.Getenv) }

func do(hc *http.Client, req *http.Request, payload any) (status int, body string, err error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, "", err
	}
	req.Body = io.NopCloser(bytes.NewReader(data))
	req.ContentLength = int64(len(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "monorelease")
	resp, err := hc.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, string(b), nil
}

type github struct {
	api, repo, token string
	hc               *http.Client
}

func (g *github) Name() string { return "GitHub" }

func (g *github) CreateRelease(tag, title, body string, prerelease bool) (bool, error) {
	req, err := http.NewRequest(http.MethodPost, g.api+"/repos/"+g.repo+"/releases", nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	status, resp, err := do(g.hc, req, map[string]any{
		"tag_name": tag, "name": title, "body": body, "prerelease": prerelease,
	})
	if err != nil {
		return false, err
	}
	switch {
	case status == http.StatusCreated:
		return true, nil
	case status == http.StatusUnprocessableEntity && strings.Contains(resp, "already_exists"):
		return false, nil
	}
	return false, fmt.Errorf("GitHub: creating release %s: HTTP %d: %s", tag, status, strings.TrimSpace(resp))
}

type gitlab struct {
	api, project, header, token string
	hc                          *http.Client
}

func (g *gitlab) Name() string { return "GitLab" }

func (g *gitlab) CreateRelease(tag, title, body string, prerelease bool) (bool, error) {
	// GitLab has no "prerelease" flag; the tag name already shows it
	req, err := http.NewRequest(http.MethodPost, g.api+"/projects/"+g.project+"/releases", nil)
	if err != nil {
		return false, err
	}
	req.Header.Set(g.header, g.token)
	status, resp, err := do(g.hc, req, map[string]any{
		"tag_name": tag, "name": title, "description": body,
	})
	if err != nil {
		return false, err
	}
	switch status {
	case http.StatusCreated:
		return true, nil
	case http.StatusConflict:
		return false, nil
	}
	return false, fmt.Errorf("GitLab: creating release %s: HTTP %d: %s", tag, status, strings.TrimSpace(resp))
}
