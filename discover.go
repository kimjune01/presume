package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/kimjune01/presume/forge"
	"github.com/kimjune01/presume/platform"
)

// Discovery indexes POINTERS to resumes already public on GitHub — never their content — so
// there is no rehosting and no copyright/copyleft surface (unlike pageleft, which ingests
// full text and must filter to copyleft-licensed sources). We index any public resume's
// pointer regardless of the repo's license.
//
// Two hard limits on code search: 1000 results/query and ~10 req/min. Beat the cap by sweeping
// each query in narrow size buckets (< 1000 hits each); beat the rate limit by pacing and
// checkpointing the cursor so a run resumes instead of restarting.
//
// Over-narrow first, then broaden: exact filename + Markdown-classified first (small, precise,
// real personal resumes), dropping qualifiers into the noisy long tail only later.
var queries = []string{
	"filename:resume.md language:Markdown",
	"filename:cv.md language:Markdown",
	"filename:resume.md",
	"filename:cv.md",
	"filename:resume.markdown",
	"filename:cv.markdown",
	"path:resume.md",
	"path:cv.md",
}

var sizeBuckets = [][2]int{
	{0, 1499}, {1500, 2999}, {3000, 4999}, {5000, 7999},
	{8000, 11999}, {12000, 19999}, {20000, 39999}, {40000, 200000},
}

const (
	maxPage = 10 // 10 pages * 100 = the 1000-result cap
	paceSec = 7  // ~10 req/min
)

// GitHub's filename:/path: qualifiers token-match, leaking vibe-resume.md, session-resume.md,
// _posts/2022-08-11-resume.md, README_RESUME.md. Enforce narrowness with an exact basename.
var resumeBasename = regexp.MustCompile(`(?i)^(my)?(resume|cv|cv-resume)\.(md|markdown)$`)

// Reject course repos / templates / prompt libs / agent-command files — not personal resumes.
var notPersonal = regexp.MustCompile(`(?i)template|example|sample|boilerplate|starter|tutorial|` +
	`course|homework|assign|awesome|prompt|snippet|lingo|cs[-_]?\d{2,}|dm-gy|topjava|ml[-_]?note|` +
	`\.claude/|\.codex/|/commands/|/skills/|/_posts/|reading-notes|-sources|/skills-|resolve-merge|` +
	`test-with-action|github-pages|/docs/|_sdk`)

type cursor struct {
	QI   int  `json:"qi"`
	BI   int  `json:"bi"`
	Page int  `json:"page"`
	Done bool `json:"done"`
}

func cursorPath(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), ".discover_cursor.json")
}

func loadCursor(path string) cursor {
	c := cursor{Page: 1}
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &c)
	}
	return c
}

func saveCursor(path string, c cursor) error {
	b, _ := json.Marshal(c)
	return os.WriteFile(path, b, 0o644)
}

func cmdDiscover(db *platform.DB, gh *forge.Client, dbPath string, pages int, reset bool) error {
	cpath := cursorPath(dbPath)
	if reset {
		os.Remove(cpath)
		fmt.Println("cursor reset")
	}
	c := loadCursor(cpath)
	if c.Done {
		fmt.Println("sweep complete; `discover --reset` to start over")
		return nil
	}
	before, _ := db.Count("discovered")
	skipped, fetched := 0, 0
	now := nowUTC()

	for fetched < pages {
		if c.QI >= len(queries) {
			c.Done = true
			break
		}
		b := sizeBuckets[c.BI]
		q := fmt.Sprintf("%s size:%d..%d", queries[c.QI], b[0], b[1])
		hits, err := gh.SearchCode(q, c.Page)
		if err != nil {
			return err
		}
		fetched++
		for _, h := range hits {
			base := basename(h.Path)
			if !resumeBasename.MatchString(base) || notPersonal.MatchString(h.Repo) || notPersonal.MatchString(h.Path) {
				skipped++
				continue
			}
			if err := db.UpsertDiscovered(h.URL, h.Repo, h.Path, now); err != nil {
				return err
			}
		}
		// advance: next page, or next bucket/query when a page is short or we hit the cap
		if len(hits) < 100 || c.Page >= maxPage {
			c.Page = 1
			c.BI++
			if c.BI >= len(sizeBuckets) {
				c.BI = 0
				c.QI++
			}
		} else {
			c.Page++
		}
		if err := saveCursor(cpath, c); err != nil {
			return err
		}
		if fetched < pages {
			time.Sleep(paceSec * time.Second)
		}
	}

	total, _ := db.Count("discovered")
	pos := "DONE"
	if !c.Done {
		pos = fmt.Sprintf("%q bucket %d page %d", queries[c.QI], c.BI, c.Page)
	}
	fmt.Printf("discover: +%d new candidates, %d non-personal filtered (%d pages this run). "+
		"registry now %d. next: %s\n", total-before, skipped, fetched, total, pos)
	recent, err := db.RecentDiscovered(12)
	if err != nil {
		return err
	}
	for _, r := range recent {
		fmt.Printf("  %s :: %s   (presume index %s --path %s)\n", r.Repo, r.Path, r.Repo, r.Path)
	}
	return nil
}

func basename(p string) string {
	if i := lastSlash(p); i >= 0 {
		return p[i+1:]
	}
	return p
}

func lastSlash(p string) int {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return i
		}
	}
	return -1
}
