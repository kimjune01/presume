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
// Over-narrow first, then broaden. The ladder ranks by DISCOVERY PRECISION — the signal-to-
// noise of "is this hit even a personal resume" — because that is discover's only job. This is
// a different axis from CONTENT-DIFF cleanliness (how cleanly a real resume's versions diff),
// where structured .json is best and binary .pdf worst. Do not conflate them: resume.json has
// the cleanest content-diff but a NOISY discovery signal (the exact basename collides with
// Discord-bot i18n files, JSON-schema defs, and app data), so it ranks BELOW the .md queries
// here. Rank the ladder by discovery precision; let the content-diff axis matter later, when a
// found resume is actually analyzed.
//   1-4. exact resume/cv .md, Markdown-classified first — cleanest discovery signal.
//   5-8. resume/cv .json + .markdown — real resumes, noisier basenames.
//   9-12. .yaml/.yml + path: matches — broadest, noisiest long tail.
// Empirical note: EXPLICIT provenance markers (.ots / signed / build-SHA stamps) are a ~null
// set on GitHub, so we do NOT query for them — presume reads IMPLICIT git provenance instead.
var queries = []string{
	"filename:resume.md language:Markdown",
	"filename:cv.md language:Markdown",
	"filename:resume.md",
	"filename:cv.md",
	"filename:resume.markdown",
	"filename:cv.markdown",
	"filename:resume.json",
	"filename:cv.json",
	"filename:resume.yaml",
	"filename:resume.yml",
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
// Extensions restricted to the rendered/structured resume formats we query (md, markdown, json,
// yaml/yml) — not .html/.tex, whose histories are dominated by build churn, not claim edits.
var resumeBasename = regexp.MustCompile(`(?i)^(my)?(resume|cv|cv-resume)\.(md|markdown|json|ya?ml)$`)

// Reject course repos / templates / prompt libs / agent-command files — not personal resumes.
// The locale/bot patterns (/languages/, /i18n/, /music/, discord) exclude the biggest
// resume.json false positive: the "resume" (playback) command's i18n file in Discord bots.
var notPersonal = regexp.MustCompile(`(?i)template|example|sample|boilerplate|starter|tutorial|` +
	`course|homework|assign|awesome|prompt|snippet|lingo|cs[-_]?\d{2,}|dm-gy|topjava|ml[-_]?note|` +
	`\.claude/|\.codex/|/commands/|/skills/|/_posts/|reading-notes|-sources|/skills-|resolve-merge|` +
	`test-with-action|github-pages|/docs/|_sdk|` +
	`/languages/|/locales/|/lang/|/i18n/|/music/|discord|guvi|weekend-practice|` +
	`/schemas/|/trigger/|` + // resume.json is also used as a JSON-schema def / ETL trigger file
	`interview|capstone|bootcamp|curriculum|coursework`) // repos that ship a demo/practice resume

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

// cmdMask retroactively suppresses corpus entries that the current non-personal filter rejects
// — the false-positive classes ingested under looser filters (interview repos, coursework, JSON
// data files). It sets the masked flag rather than deleting: reversible, and it records why.
func cmdMask(db *platform.DB) error {
	resumes, err := db.DistinctResumes()
	if err != nil {
		return err
	}
	now := nowUTC()
	masked := 0
	for _, r := range resumes {
		if notPersonal.MatchString(r.Repo) || notPersonal.MatchString(r.Path) || !resumeBasename.MatchString(basename(r.Path)) {
			if err := db.Mask(r.Repo, r.Path, "non-personal (filter)", now); err != nil {
				return err
			}
			masked++
		}
	}
	total, _ := db.MaskedCount()
	fmt.Printf("masked %d non-resume entries this pass (%d masked total); omitted from search\n", masked, total)
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
