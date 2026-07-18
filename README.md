# presume

A provenance resume index for technical recruiters worried about **resume stuffing**.

A resume is written *after* the candidate reads the posting, so every line is optimized to
match it — and nothing stops them telling one employer "10y Rust" and another "10y Go."
`presume` removes that freedom. A candidate keeps a small number of resume versions **in
git**; each commit is a content-addressed, timestamped, publicly-replicated version
(relatively tamper-proof once pushed — GitHub records receipt time server-side). You
**apply by reference** to a specific committed version, and the gap between when it was
committed and when you applied is the signal:

- committed **long before** the posting → revealed pre-commitment (couldn't be stuffed for this job)
- committed **the same day** → tailored; flag it

The provenance chain already exists in the git history of resumes people keep on GitHub —
`presume` just **indexes it**, so it seeds straight from what's already public. No
blockchain: git's Merkle history plus public replication is the tamper-evidence.

## Trust model — a non-custodial index, not a CA

`presume` is **not a certificate authority**. It mints nothing, signs nothing, vouches for
nothing, and **hosts no content** — only *pointers*. Every record it returns is a resolvable
pointer to an authority the recruiter verifies independently: the git host's replicated,
timestamped commit record (`raw.githubusercontent.com/OWNER/REPO/SHA/PATH`). You **never have
to trust `presume`** — if the index is wrong, down, or hostile, every pointer still resolves
against the git host and the lie is caught. The index points to the authority; it is not the
authority. (Prior art for the shape: Certificate Transparency logs, Sigstore/Rekor, DNS, DOI —
an index of resolvable pointers, never the trust root.)

Because it stores pointers and never rehosts a byte of resume content, there is **no copyright
surface** and no license filter on what it indexes (unlike a content/RAG index such as
[pageleft](https://github.com/kimjune01/pageleft), which ingests full text and must restrict
itself to copyleft-licensed sources). `verify` fetches content at request time only to hash
it — ephemeral, like a browser, never stored.

## What it catches (that a PDF can't)

- **Tailoring / stuffing** — a version committed hours before the application, versus one
  that's been standing for months.
- **Cross-employer inconsistency** — all versions are public in git history; you can't tell
  A and B contradictory stories, and you can't quietly delete a past claim.
- **Non-repudiation** — the SHA freezes the exact bytes; "the version I applied with" is a
  cryptographic reference, not a re-uploadable file.

## Use

```
presume discover [--pages N] [--reset]                   # sweep GitHub for resumes (resumable), to seed from
presume index    OWNER/REPO --path FILE                  # build the version index from git history
presume seed     HANDLE                                  # find resume-like files in a user's repos, index them
presume ingest                                           # pull provenance for every discovered candidate
presume classify                                         # derive role tags from each resume's latest content
presume categories                                       # the curated job-category menu, with corpus counts
presume mask                                             # flag non-resume false positives; omit them from search
presume search   [--role R] [--min-versions N] [--min-span-days N] [--committed-before DATE] [--handle S] [--limit N] [--json]
                                                         # query talent by role + provenance shape; returns pointers
presume verify   OWNER/REPO SHA --path FILE              # resolve a pointer: frozen content + authority URL
presume apply    OWNER/REPO SHA --path FILE --job REF    # apply by reference; emit the anti-tailoring signal
presume log      [HANDLE]                                # indexed versions and applications
```

**Agent-first.** `search --json` returns an array of pointer objects (`repo`, `path`, `roles`,
`versions`, `span_days`, `earliest_sha`, `authority`), deepest-provenance first — a few
resolvable links an agent can rank without trusting the index. Roles come from a transparent
keyword classifier (frontend / backend / fullstack / mobile / ml-ai / data-engineer /
data-analyst / devops-sre / security / systems / qa-test / game / blockchain), derived from
content fetched ephemerally at `classify` time and stored as tags — never the resume body.
Pipeline: `discover` → `ingest` → `classify` → `search --role … --json`.

Go, on the [pageleft](https://github.com/kimjune01/pageleft) skeleton: pure-Go SQLite
(`modernc.org/sqlite`, no CGo), a `net/http` GitHub client in `forge/`, a pointer store in
`platform/`. Build: `go build -o presume .`. Token: `$GITHUB_PUBLIC_API_KEY`, falling back to
`gh auth token` (works unauthenticated at 60 req/hr). DB: `$PRESUME_DB` (default `./bank.db`,
gitignored). `discover` sweeps GitHub code search — narrow filenames first, then broadening —
checkpointing a gitignored cursor so it resumes under the rate limit instead of restarting.

## Composes with `resume-audit`

Two orthogonal defenses:

- **presume** answers *which version, committed when, and was it stuffed for this posting* —
  it defeats **tailoring**.
- **resume-audit** (sibling skill) answers *are the claims in that version staked and true*
  (self / peer / world attestation, cost-and-stake filter) — it defeats **fabrication**.

Point `resume-audit` at the exact `presume`-referenced version and you have both: a frozen,
timestamped, un-tailored resume whose claims are verified against the contribution graph.

## Status

MVP, working end-to-end on live GitHub data: `discover` / `index` / `seed` / `search` /
`verify` / `apply` / `log`. Full roadmap in [`docs/ROADMAP.md`](docs/ROADMAP.md). Nearest
open items: index on **job titles / roles** so recruiters can query by role (not just
provenance shape); a `consistency` command (diff claims across a candidate's versions); a
content-vs-presentation diff so cosmetic same-day commits don't false-flag as tailoring; and
a stronger timestamp anchor (push-time / OpenTimestamps / PageLeft) beyond the self-assertable
commit date.
