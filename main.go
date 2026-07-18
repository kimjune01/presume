// presume — a searchable provenance index for talent. NOT a certificate authority.
//
// A resume is written after the candidate reads the posting, so it's optimized to match it, and
// nothing stops one story to employer A and another to B. presume removes that freedom: a
// candidate keeps a few resume versions in git; each commit is a content-addressed, timestamped,
// publicly-replicated version. You APPLY BY REFERENCE to a committed version, and the gap between
// its commit time and the application is the anti-tailoring signal.
//
// Trust model — a non-custodial transparency index, not a CA. presume mints nothing, signs
// nothing, hosts no content. Every record is a RESOLVABLE POINTER to an authority the recruiter
// verifies independently: the git host's replicated, timestamped commit record. You never have
// to trust presume — if it is wrong, down, or hostile, every pointer still resolves and the lie
// is caught. Prior art: Certificate Transparency, Sigstore/Rekor, DNS, DOI.
//
// Copyright (C) 2026 June Kim. GNU AGPL-3.0-or-later. See LICENSE.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/kimjune01/presume/forge"
	"github.com/kimjune01/presume/platform"
)

const tailoringDays = 3

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

func dbPath() string {
	if p := os.Getenv("PRESUME_DB"); p != "" {
		return p
	}
	return "bank.db"
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	path := dbPath()
	db, err := platform.Open(path)
	check(err)
	defer db.Close()
	gh := forge.New()

	switch os.Args[1] {
	case "discover":
		fs := flag.NewFlagSet("discover", flag.ExitOnError)
		pages := fs.Int("pages", 6, "search pages to fetch this run")
		reset := fs.Bool("reset", false, "restart the sweep from the beginning")
		fs.Parse(os.Args[2:])
		check(cmdDiscover(db, gh, path, *pages, *reset))
	case "index":
		repo, rest := arg(os.Args[2:])
		fs := flag.NewFlagSet("index", flag.ExitOnError)
		fp := fs.String("path", "", "file path within the repo")
		fs.Parse(rest)
		require(repo != "" && *fp != "", "usage: presume index OWNER/REPO --path FILE")
		check(cmdIndex(db, gh, repo, *fp))
	case "seed":
		require(len(os.Args) >= 3, "usage: presume seed HANDLE")
		check(cmdSeed(db, gh, os.Args[2]))
	case "ingest":
		check(cmdIngest(db, gh))
	case "classify":
		check(cmdClassify(db, gh))
	case "search":
		fs := flag.NewFlagSet("search", flag.ExitOnError)
		mv := fs.Int("min-versions", 1, "minimum version count")
		ms := fs.Int("min-span-days", 0, "minimum days between first and last version")
		cb := fs.String("committed-before", "", "require a version committed before YYYY-MM-DD")
		h := fs.String("handle", "", "substring filter on OWNER/REPO")
		role := fs.String("role", "", "filter by derived role tag (e.g. backend, ml-ai, devops-sre)")
		limit := fs.Int("limit", 0, "return at most N candidates (0 = all)")
		asJSON := fs.Bool("json", false, "emit JSON pointers (agent-first)")
		fs.Parse(os.Args[2:])
		check(cmdSearch(db, *mv, *ms, *cb, *h, *role, *limit, *asJSON))
	case "verify":
		repo, sha, rest := arg2(os.Args[2:])
		fs := flag.NewFlagSet("verify", flag.ExitOnError)
		fp := fs.String("path", "", "file path within the repo")
		fs.Parse(rest)
		require(repo != "" && sha != "" && *fp != "", "usage: presume verify OWNER/REPO SHA --path FILE")
		check(cmdVerify(gh, repo, sha, *fp))
	case "apply":
		repo, sha, rest := arg2(os.Args[2:])
		fs := flag.NewFlagSet("apply", flag.ExitOnError)
		fp := fs.String("path", "", "file path within the repo")
		job := fs.String("job", "", "job reference applied to")
		fs.Parse(rest)
		require(repo != "" && sha != "" && *fp != "" && *job != "", "usage: presume apply OWNER/REPO SHA --path FILE --job REF")
		check(cmdApply(db, gh, repo, sha, *fp, *job))
	case "log":
		h := ""
		if len(os.Args) >= 3 {
			h = os.Args[2]
		}
		check(cmdLog(db, h))
	default:
		usage()
	}
}

func cmdIndex(db *platform.DB, gh *forge.Client, repo, path string) error {
	commits, err := gh.Commits(repo, path)
	if err != nil {
		return err
	}
	added := 0
	for _, c := range commits {
		isNew, err := db.UpsertVersion(repo, path, c.SHA, c.Date.Format(time.RFC3339), c.Subject, c.Author)
		if err != nil {
			return err
		}
		if isNew {
			added++
		}
	}
	fmt.Printf("indexed %s:%s — %d versions in git, %d newly registered\n", repo, path, len(commits), added)
	for i, c := range commits {
		if i >= 8 {
			break
		}
		fmt.Printf("  %s  %s  %s\n", c.SHA[:8], c.Date.Format("2006-01-02T15:04:05Z"), c.Subject)
	}
	return nil
}

func cmdSeed(db *platform.DB, gh *forge.Client, handle string) error {
	type cand struct{ repo, path string }
	cands := []cand{{handle + "/" + handle, "README.md"}}
	repos, err := gh.Repos(handle)
	if err != nil {
		return err
	}
	for _, r := range repos {
		for _, p := range []string{"resume.md", "cv.md", "RESUME.md", "public/assets/resume.md"} {
			cands = append(cands, cand{handle + "/" + r, p})
		}
	}
	seeded := 0
	for _, c := range cands {
		commits, err := gh.Commits(c.repo, c.path)
		if err != nil || len(commits) == 0 {
			continue
		}
		if err := cmdIndex(db, gh, c.repo, c.path); err == nil {
			seeded++
		}
	}
	fmt.Printf("seeded %d resume file(s) for %s\n", seeded, handle)
	return nil
}

// cmdIngest pulls the commit-history provenance for every discovered candidate into the
// versions table — turning the pointer list from `discover` into a searchable provenance index.
func cmdIngest(db *platform.DB, gh *forge.Client) error {
	cands, err := db.AllDiscovered()
	if err != nil {
		return err
	}
	indexed, versions, unreachable := 0, 0, 0
	for _, c := range cands {
		commits, err := gh.Commits(c.Repo, c.Path)
		if err != nil || len(commits) == 0 {
			unreachable++
			continue
		}
		for _, cm := range commits {
			isNew, err := db.UpsertVersion(c.Repo, c.Path, cm.SHA, cm.Date.Format(time.RFC3339), cm.Subject, cm.Author)
			if err != nil {
				return err
			}
			if isNew {
				versions++
			}
		}
		indexed++
		if indexed%25 == 0 {
			fmt.Printf("  ingested %d/%d…\n", indexed, len(cands))
		}
	}
	fmt.Printf("ingested %d/%d discovered resumes — %d new versions (%d unreachable/empty)\n",
		indexed, len(cands), versions, unreachable)
	return nil
}

// pointer is the agent-first result shape: a resolvable link plus the provenance and role tags
// an agent needs to rank, with nothing to trust — every field re-verifies against the authority.
type pointer struct {
	Repo        string   `json:"repo"`
	Path        string   `json:"path"`
	Roles       []string `json:"roles"`
	Versions    int      `json:"versions"`
	SpanDays    int      `json:"span_days"`
	FirstCommit string   `json:"first_committed"`
	EarliestSHA string   `json:"earliest_sha"`
	Authority   string   `json:"authority"`
}

func cmdSearch(db *platform.DB, minVersions, minSpanDays int, committedBefore, handle, role string, limit int, asJSON bool) error {
	cands, err := db.SearchCandidates(minVersions, minSpanDays, committedBefore, handle, role)
	if err != nil {
		return err
	}
	// deepest provenance first — the strongest, hardest-to-fake candidates lead.
	sort.Slice(cands, func(i, j int) bool { return cands[i].SpanDays > cands[j].SpanDays })
	if limit > 0 && len(cands) > limit {
		cands = cands[:limit]
	}
	if asJSON {
		out := make([]pointer, len(cands))
		for i, c := range cands {
			out[i] = pointer{c.Repo, c.Path, orEmpty(c.Roles), c.Versions, c.SpanDays,
				c.First[:10], c.EarliestSHA, forge.RawURL(c.Repo, c.EarliestSHA, c.Path)}
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	for _, c := range cands {
		roles := "—"
		if len(c.Roles) > 0 {
			roles = strings.Join(c.Roles, ", ")
		}
		fmt.Printf("\n%s :: %s\n", c.Repo, c.Path)
		fmt.Printf("  roles        : %s\n", roles)
		fmt.Printf("  provenance   : %d versions over %d days (%s → %s)\n",
			c.Versions, c.SpanDays, c.First[:10], c.Latest[:10])
		fmt.Printf("  earliest ref : %s  committed %s  (pre-commitment anchor)\n", short(c.EarliestSHA), c.First[:10])
		fmt.Printf("  authority    : %s\n", forge.RawURL(c.Repo, c.EarliestSHA, c.Path))
	}
	crit := fmt.Sprintf("versions>=%d, span>=%dd", minVersions, minSpanDays)
	if role != "" {
		crit += ", role=" + role
	}
	if committedBefore != "" {
		crit += ", a version before " + committedBefore
	}
	if handle != "" {
		crit += ", handle~" + handle
	}
	fmt.Printf("\n%d candidate(s) matching [%s] — verify each pointer against the git host.\n", len(cands), crit)
	return nil
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func cmdVerify(gh *forge.Client, repo, sha, path string) error {
	content, err := gh.Content(repo, path, sha)
	if err != nil {
		return err
	}
	commit, err := gh.Commit(repo, sha)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(content.Bytes)
	fmt.Printf("verified %s:%s @ %s\n", repo, path, short(sha))
	fmt.Printf("  committed_at  : %s\n", commit.Date.Format(time.RFC3339))
	fmt.Printf("  content sha256: %s\n", hex.EncodeToString(sum[:]))
	fmt.Printf("  git blob sha  : %s  (%d bytes) — frozen, content-addressed\n", content.BlobSHA, content.Size)
	fmt.Printf("  authority     : %s\n", forge.RawURL(repo, sha, path))
	return nil
}

func cmdApply(db *platform.DB, gh *forge.Client, repo, sha, path, job string) error {
	commit, err := gh.Commit(repo, sha)
	if err != nil {
		return err
	}
	latency := time.Since(commit.Date).Hours() / 24
	signal := fmt.Sprintf("pre-committed %.0f days before applying", latency)
	if latency < tailoringDays {
		signal = "TAILORED (committed just before applying)"
	}
	if err := db.InsertApplication(repo, path, sha, job, nowUTC(), commit.Date.Format(time.RFC3339), round1(latency)); err != nil {
		return err
	}
	fmt.Printf("applied to %s by reference to %s:%s@%s\n", job, repo, path, short(sha))
	fmt.Printf("  version committed %s\n", commit.Date.Format(time.RFC3339))
	fmt.Printf("  anti-tailoring signal: %s\n", signal)
	return nil
}

func cmdLog(db *platform.DB, handle string) error {
	versions, err := db.Versions(handle)
	if err != nil {
		return err
	}
	fmt.Println("=== versions ===")
	for _, v := range versions {
		fmt.Printf("  %s  %s  %s:%s  %s\n", short(v.SHA), v.CommittedAt, v.Repo, v.Path, v.Subject)
	}
	apps, err := db.Applications(handle)
	if err != nil {
		return err
	}
	fmt.Println("=== applications ===")
	for _, a := range apps {
		fmt.Printf("  %s -> %s  (precommit %.1fd)\n", short(a.SHA), a.Job, a.Latency)
	}
	return nil
}

// helpers

func short(sha string) string {
	if len(sha) >= 8 {
		return sha[:8]
	}
	return sha
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

// arg splits a leading positional from the remaining flag args.
func arg(a []string) (string, []string) {
	if len(a) == 0 || len(a[0]) == 0 || a[0][0] == '-' {
		return "", a
	}
	return a[0], a[1:]
}

// arg2 splits two leading positionals from the remaining flag args.
func arg2(a []string) (string, string, []string) {
	first, rest := arg(a)
	second, rest := arg(rest)
	return first, second, rest
}

func require(ok bool, msg string) {
	if !ok {
		fmt.Fprintln(os.Stderr, msg)
		os.Exit(2)
	}
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `presume — searchable provenance index for talent (not a CA)

  presume discover [--pages N] [--reset]
  presume index    OWNER/REPO --path FILE
  presume seed     HANDLE
  presume ingest
  presume classify
  presume search   [--role R] [--min-versions N] [--min-span-days N] [--committed-before DATE] [--handle S] [--limit N] [--json]
  presume verify   OWNER/REPO SHA --path FILE
  presume apply    OWNER/REPO SHA --path FILE --job REF
  presume log      [HANDLE]

Roles: frontend backend fullstack mobile ml-ai data-engineer data-analyst devops-sre security systems qa-test game blockchain
DB path: $PRESUME_DB (default ./bank.db). Token: $GITHUB_PUBLIC_API_KEY or gh auth.`)
	os.Exit(2)
}
