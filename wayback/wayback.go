// Package wayback is presume's SECOND provenance backend: the Internet Archive's Wayback
// Machine. Where git provenance is content-addressed but its committer date is self-assertable
// (backdatable), a Wayback snapshot's timestamp is set by the Archive's crawler — a third party
// the candidate does not control — so it is an UN-BACKDATABLE pre-commitment anchor, and it
// covers the resumes that never touch git (personal sites, HTML, PDFs). Each snapshot is a
// resolvable pointer to that authority; presume stores nothing and vouches for nothing.
package wayback

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

var client = &http.Client{Timeout: 30 * time.Second}

// Snapshot is one archived capture. Timestamp is Wayback's (YYYYMMDDhhmmss), not the site's.
type Snapshot struct {
	Timestamp string
	Original  string
	Status    string
	Digest    string
}

// Time parses the Wayback timestamp to a time.Time.
func (s Snapshot) Time() time.Time {
	t, _ := time.Parse("20060102150405", s.Timestamp)
	return t
}

// URL is the resolvable pointer to this capture on the Archive.
func (s Snapshot) URL() string {
	return "https://web.archive.org/web/" + s.Timestamp + "/" + s.Original
}

// CDX returns the distinct-content capture history of a URL (collapsed on digest, so each entry
// is a real content change — the provenance chain), oldest first.
func CDX(rawURL string) ([]Snapshot, error) {
	api := "https://web.archive.org/cdx/search/cdx?output=json&collapse=digest&url=" + url.QueryEscape(rawURL)
	req, _ := http.NewRequest("GET", api, nil)
	req.Header.Set("User-Agent", "presume/0.1")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("wayback CDX: %s", resp.Status)
	}
	var rows [][]string
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, err
	}
	// rows[0] is the header: [urlkey timestamp original mimetype statuscode digest length]
	var out []Snapshot
	for _, r := range rows {
		if len(r) < 6 || r[1] == "timestamp" {
			continue
		}
		out = append(out, Snapshot{Timestamp: r[1], Original: r[2], Status: r[4], Digest: r[5]})
	}
	return out, nil
}
