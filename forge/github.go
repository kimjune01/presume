// Package forge is presume's read-only GitHub client. It builds the index but is never the
// authority: every record ultimately resolves to the git host's own replicated commit record.
// Inherits pageleft's forge conventions — net/http, token from GITHUB_PUBLIC_API_KEY, pure Go.
package forge

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const apiBase = "https://api.github.com/"

// RawURL is the resolvable pointer-to-authority for a file at a commit: it resolves against
// the git host with no dependency on presume or even gh. This is what recruiters verify.
func RawURL(repo, sha, path string) string {
	return "https://raw.githubusercontent.com/" + repo + "/" + sha + "/" + escapePath(path)
}

type Client struct {
	http  *http.Client
	token string
}

// New reads GITHUB_PUBLIC_API_KEY (pageleft's convention), falling back to `gh auth token`
// so it runs anywhere gh is already authed. Unauthenticated it still works at 60 req/hr.
func New() *Client {
	tok := os.Getenv("GITHUB_PUBLIC_API_KEY")
	if tok == "" {
		if b, err := exec.Command("gh", "auth", "token").Output(); err == nil {
			tok = strings.TrimSpace(string(b))
		}
	}
	return &Client{http: &http.Client{Timeout: 20 * time.Second}, token: tok}
}

// get issues an authenticated GET, honoring GitHub's secondary rate limit (403/429 +
// Retry-After) with bounded backoff — the same limit the discovery sweep is paced against.
func (c *Client) get(path string, v any) error {
	for attempt := 0; attempt < 4; attempt++ {
		req, _ := http.NewRequest("GET", apiBase+path, nil)
		req.Header.Set("Accept", "application/vnd.github+json")
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		switch {
		case resp.StatusCode == 200:
			return json.Unmarshal(body, v)
		case resp.StatusCode == 403 || resp.StatusCode == 429:
			wait := 60 * (attempt + 1)
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if n, e := strconv.Atoi(ra); e == nil {
					wait = n
				}
			}
			time.Sleep(time.Duration(wait) * time.Second)
		default:
			return fmt.Errorf("GET %s: %s: %s", path, resp.Status, trunc(body))
		}
	}
	return fmt.Errorf("GET %s: gave up after repeated rate limiting", path)
}

// Commit is one immutable resume version: SHA content-addresses it, Date timestamps it.
type Commit struct {
	SHA     string
	Date    time.Time
	Subject string
	Author  string
}

type ghCommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Name string `json:"name"`
		} `json:"author"`
		Committer struct {
			Date string `json:"date"`
		} `json:"committer"`
	} `json:"commit"`
}

func (g ghCommit) toCommit() Commit {
	d, _ := time.Parse(time.RFC3339, g.Commit.Committer.Date)
	subj := g.Commit.Message
	if i := strings.IndexByte(subj, '\n'); i >= 0 {
		subj = subj[:i]
	}
	return Commit{SHA: g.SHA, Date: d, Subject: subj, Author: g.Commit.Author.Name}
}

// Commits returns a file's full version history (the provenance chain), newest first.
func (c *Client) Commits(repo, path string) ([]Commit, error) {
	q := url.Values{"path": {path}, "per_page": {"100"}}
	var raw []ghCommit
	if err := c.get("repos/"+repo+"/commits?"+q.Encode(), &raw); err != nil {
		return nil, err
	}
	out := make([]Commit, len(raw))
	for i, g := range raw {
		out[i] = g.toCommit()
	}
	return out, nil
}

// Commit resolves one version by SHA (for verify/apply).
func (c *Client) Commit(repo, sha string) (Commit, error) {
	var g ghCommit
	if err := c.get("repos/"+repo+"/commits/"+sha, &g); err != nil {
		return Commit{}, err
	}
	return g.toCommit(), nil
}

// Repos lists a user's public repo names (for seed).
func (c *Client) Repos(user string) ([]string, error) {
	var raw []struct {
		Name string `json:"name"`
	}
	if err := c.get("users/"+user+"/repos?per_page=100", &raw); err != nil {
		return nil, err
	}
	out := make([]string, len(raw))
	for i, r := range raw {
		out[i] = r.Name
	}
	return out, nil
}

// Content is the frozen bytes at a version: BlobSHA is git's content address.
type Content struct {
	BlobSHA string
	Size    int
	Bytes   []byte
}

func (c *Client) Content(repo, path, ref string) (Content, error) {
	var m struct {
		SHA     string `json:"sha"`
		Size    int    `json:"size"`
		Content string `json:"content"`
	}
	q := url.Values{"ref": {ref}}
	if err := c.get("repos/"+repo+"/contents/"+escapePath(path)+"?"+q.Encode(), &m); err != nil {
		return Content{}, err
	}
	b, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(m.Content, "\n", ""))
	if err != nil {
		return Content{}, err
	}
	return Content{BlobSHA: m.SHA, Size: m.Size, Bytes: b}, nil
}

// CodeHit is a discovery candidate. URL is the file's canonical GitHub URL — the upsert key.
type CodeHit struct{ Repo, Path, URL string }

// SearchCode runs one page of code search. Caller paces + buckets to beat the 1000/query cap.
func (c *Client) SearchCode(q string, page int) ([]CodeHit, error) {
	var r struct {
		Items []struct {
			Path       string `json:"path"`
			HTMLURL    string `json:"html_url"`
			Repository struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
		} `json:"items"`
	}
	vals := url.Values{"q": {q}, "per_page": {"100"}, "page": {strconv.Itoa(page)}}
	if err := c.get("search/code?"+vals.Encode(), &r); err != nil {
		return nil, err
	}
	out := make([]CodeHit, len(r.Items))
	for i, it := range r.Items {
		out[i] = CodeHit{Repo: it.Repository.FullName, Path: it.Path, URL: it.HTMLURL}
	}
	return out, nil
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

func trunc(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
