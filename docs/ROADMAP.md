# presume roadmap

A searchable provenance index for talent — **not** a certificate authority. The index holds
resolvable pointers to the git host's timestamped commit record; it never becomes the
authority and never hosts content. Every item below preserves that invariant: *don't trust
the index, verify against the authority.*

Move items to **Done** as they land. If work reveals new open problems, add them.

## Done

- **Go port on the pageleft skeleton** — pure-Go SQLite (`modernc.org/sqlite`), `forge/`
  GitHub client over `net/http`, `platform/` pointer store. `go build ./...` clean, `go vet`
  clean.
- **Core verbs** — `index` (version history from git), `seed` (a handle's resume-like files),
  `verify` (frozen content + `sha256` + git blob SHA + authority URL), `apply` (apply-by-
  reference, emits the commit-vs-application anti-tailoring latency), `log`.
- **`search` — talent by provenance shape.** Filter on version count, span, and a pre-commit
  cutoff; each match returns the earliest (hardest-to-backdate) version as a pointer that
  resolves at `raw.githubusercontent.com`. Filters, never ranks or adjudicates.
- **`discover` — resumable GitHub sweep.** Beats the 1000-results/query cap by bucketing each
  query on file size, beats the ~10 req/min limit by pacing + honoring `Retry-After`, and
  checkpoints a gitignored cursor so a run resumes instead of restarting. Over-narrow first
  (exact filename + Markdown-classified), broadening into the long tail later. Upserts on the
  file's canonical URL. Precision gate (exact basename + course/template/agent-file rejects)
  took survivor precision from ~50% to ~90%+ on live results.
- **Non-custodial / pointer-only trust model** documented and enforced in code — no rehosting,
  no copyright surface, no license filter on indexed resumes (contrast: pageleft is a
  content/RAG index and must restrict to copyleft sources).
- **`ingest` → `classify` → agent-first search.** `ingest` pulls provenance for every
  discovered candidate; `classify` derives role tags (curated keyword scaffold, 13 recruiter
  categories) from content fetched ephemerally, stored as pointer-adjacent tags; `search`
  takes `--role` / `--limit` / `--json` and returns resolvable pointer objects, deepest
  provenance first. `categories` lists the curated menu with counts.
- **Masking.** A per-resume `masked` flag suppresses non-resume false positives (interview
  repos, coursework, JSON data files) from search and browse without deleting them — reversible
  and it records the reason. `mask` re-applies the current non-personal filter to the corpus.

## Next — highest leverage

- **Emergent categories.** The current role taxonomy (`classify` / `categories`) is a curated
  keyword scaffold — affordable while the corpus is thin, but hand-maintained and not
  sustainable. Long-term the categories and their signals should be *derived* from the corpus
  (clustering real resumes) and the demand side (real job postings), not kept by hand. Also:
  coverage is ~30% at threshold 2 — lift it without wrecking precision (weak language signals,
  confidence tiers), and add seniority (staff / senior / …) as a second axis recruiters query.
- **Content-vs-presentation diff for the anti-tailoring signal.** The raw commit-time latency
  over-fires on cosmetic churn (observed: a candidate with 16 commits in one day that were all
  CSS/layout, not claim edits — would false-flag as "tailored"). Fix: strip frontmatter /
  HTML / CSS, hash only the prose claims, and compare *that* across versions. A same-day commit
  that leaves claim-text identical is not tailoring.
- **`consistency` command.** Diff factual claims across a candidate's versions (and across the
  versions they've applied-by-reference with) — catch a story told to employer A that
  contradicts the one told to B, and silent deletions of past claims.
- **Resume classifier / species tags.** Discovery surfaces distinct shapes worth labeling:
  *tombstone* (stable body, plumbing-only commits — real provenance, zero resume content),
  *cosmetic churner* (many commits, no claim movement), *dormant-then-sprint* (long flat line,
  one datable revision event), *single-commit* (no history, no anti-tailoring signal). Only the
  last is useless to presume; flag it as such rather than indexing it as if it carried signal.

## Next — trust hardening

- **Stronger timestamp anchor (partly done via `wayback`).** The git committer date is self-
  assertable (`GIT_COMMITTER_DATE` is backdatable). The `wayback` backend already supplies an
  un-backdatable third-party anchor (Internet Archive crawl time) for any URL. Remaining: fold
  Wayback captures into the index alongside git versions (a `snapshots` table), cross-check the
  two backends against each other, and add push-time / OpenTimestamps for git-only resumes.
- **Codeberg / GitLab as secondary discovery.** Neither offers convenient unauthenticated
  *code* search (GitLab blob search needs auth + Elasticsearch; Gitea code search is per-repo).
  Both do offer *repo-name* search — a coarser channel: find repos named resume/cv, then probe
  for the file. Lower yield than GitHub code search; worth it only once GitHub is exhausted.
- **Per-candidate version scarcity.** The signal assumes a small, deliberate set of versions;
  an auto-committing resume (hourly stats churn) defeats the timestamp signal the same way
  tailoring does. Surface a scarcity/entropy score, or cap how many versions read as "real."
- **Contributor-identity resolution.** Commits authored under an unlinked email are missed;
  resolve identity (commit emails, co-author trailers) before asserting a firm negative.

## Composition (already true, worth keeping straight)

- **presume** answers *which version, committed when, tailored for this posting?* — defeats
  **tailoring**.
- **resume-audit** (sibling skill) answers *are the claims in that version staked and true?*
  (self / peer / world attestation, cost-and-stake filter) — defeats **fabrication**.
- Point `resume-audit` at the exact `presume` pointer: a frozen, timestamped, un-tailored
  resume whose claims are then verified against the contribution graph.

## Demand-side thesis (why candidates opt in)

The index makes a cheap-but-credible signal actually get *emitted*. Aged git identity is
costly to reboot — you can buy an account but not a history entangled with merged PRs, arXiv
records, and years of incidental commits; money compresses most costs but not elapsed calendar
time. The honest type already paid that cost, incidentally, by using git normally — and
presume surfaces it with **zero candidate work** (indexed from public history; the only action
is `apply --job`, pointing at a version that already exists). Two consequences to protect:
*both sides have a unilateral reason to show up* (recruiters audit non-participants; candidates
are pre-indexed), which breaks the two-sided cold start; and *willingness to apply-by-reference
is itself a separating signal* (only clean-provenance candidates want the check). **Design rule:
never let emission cost rise above zero** — no required signup, badge, or installed tool, or
only the desperate pay it and selection inverts.
