// Package platform is presume's local store: a non-custodial index of pointers, not a store
// of truth. Nothing here is authoritative — every row points back to the git host, which is.
// Pure-Go SQLite (modernc.org/sqlite), WAL, single file — pageleft's storage convention.
package platform

import (
	"database/sql"
	"strings"

	_ "modernc.org/sqlite"
)

type DB struct{ conn *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS versions(
  repo TEXT, path TEXT, sha TEXT, committed_at TEXT, subject TEXT, author TEXT,
  PRIMARY KEY(repo, path, sha));
CREATE TABLE IF NOT EXISTS applications(
  id INTEGER PRIMARY KEY, repo TEXT, path TEXT, sha TEXT, job TEXT,
  applied_at TEXT, committed_at TEXT, latency_days REAL);
CREATE TABLE IF NOT EXISTS discovered(
  url TEXT PRIMARY KEY, repo TEXT, path TEXT, found_at TEXT);
CREATE TABLE IF NOT EXISTS roles(
  repo TEXT, path TEXT, role TEXT, score INTEGER,
  PRIMARY KEY(repo, path, role));`

func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		return nil, err
	}
	if _, err := conn.Exec(schema); err != nil {
		return nil, err
	}
	return &DB{conn: conn}, nil
}

func (d *DB) Close() error { return d.conn.Close() }

// UpsertVersion registers one immutable version; idempotent on (repo,path,sha). Reports whether
// the row was new so index can report newly-registered vs already-known.
func (d *DB) UpsertVersion(repo, path, sha, committedAt, subject, author string) (bool, error) {
	res, err := d.conn.Exec(`INSERT OR IGNORE INTO versions VALUES (?,?,?,?,?,?)`,
		repo, path, sha, committedAt, subject, author)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// UpsertDiscovered keys on the file's canonical URL; re-discovery refreshes last-seen only.
func (d *DB) UpsertDiscovered(url, repo, path, found string) error {
	_, err := d.conn.Exec(
		`INSERT INTO discovered(url,repo,path,found_at) VALUES (?,?,?,?)
		 ON CONFLICT(url) DO UPDATE SET found_at=excluded.found_at`,
		url, repo, path, found)
	return err
}

func (d *DB) InsertApplication(repo, path, sha, job, appliedAt, committedAt string, latency float64) error {
	_, err := d.conn.Exec(
		`INSERT INTO applications(repo,path,sha,job,applied_at,committed_at,latency_days)
		 VALUES (?,?,?,?,?,?,?)`, repo, path, sha, job, appliedAt, committedAt, latency)
	return err
}

func (d *DB) Count(table string) (int, error) {
	var n int
	err := d.conn.QueryRow("SELECT count(*) FROM " + table).Scan(&n)
	return n, err
}

// Candidate is a talent match: a provenance shape plus the earliest (hardest-to-backdate)
// version to point at.
type Candidate struct {
	Repo, Path, First, Latest, EarliestSHA string
	Versions, SpanDays                     int
	Roles                                  []string
}

// SearchCandidates queries talent by provenance SHAPE and (optionally) role. It filters, never
// ranks or adjudicates — each match carries the pointer the recruiter resolves against the git
// host, plus its derived role tags. An empty role matches everything.
func (d *DB) SearchCandidates(minVersions, minSpanDays int, committedBefore, handle, role string) ([]Candidate, error) {
	rows, err := d.conn.Query(`
	  SELECT repo, path, count(*) n, min(committed_at) first, max(committed_at) latest,
	         CAST(julianday(max(committed_at)) - julianday(min(committed_at)) AS INT) span
	  FROM versions GROUP BY repo, path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.Repo, &c.Path, &c.Versions, &c.First, &c.Latest, &c.SpanDays); err != nil {
			return nil, err
		}
		if c.Versions < minVersions || c.SpanDays < minSpanDays {
			continue
		}
		if handle != "" && !strings.Contains(strings.ToLower(c.Repo), strings.ToLower(handle)) {
			continue
		}
		if committedBefore != "" && ymd(c.First) >= committedBefore {
			continue
		}
		roles, err := d.RolesFor(c.Repo, c.Path)
		if err != nil {
			return nil, err
		}
		if role != "" && !contains(roles, strings.ToLower(role)) {
			continue
		}
		c.Roles = roles
		d.conn.QueryRow(`SELECT sha FROM versions WHERE repo=? AND path=? ORDER BY committed_at LIMIT 1`,
			c.Repo, c.Path).Scan(&c.EarliestSHA)
		out = append(out, c)
	}
	return out, rows.Err()
}

func contains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

// role-tag storage — derived facts about a resume, never its content.

func (d *DB) DistinctResumes() ([]Discovered, error) {
	rows, err := d.conn.Query(`SELECT DISTINCT repo, path FROM versions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Discovered
	for rows.Next() {
		var v Discovered
		if err := rows.Scan(&v.Repo, &v.Path); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (d *DB) LatestSHA(repo, path string) (string, error) {
	var sha string
	err := d.conn.QueryRow(
		`SELECT sha FROM versions WHERE repo=? AND path=? ORDER BY committed_at DESC LIMIT 1`,
		repo, path).Scan(&sha)
	return sha, err
}

func (d *DB) ClearRoles(repo, path string) error {
	_, err := d.conn.Exec(`DELETE FROM roles WHERE repo=? AND path=?`, repo, path)
	return err
}

func (d *DB) UpsertRole(repo, path, role string, score int) error {
	_, err := d.conn.Exec(
		`INSERT INTO roles(repo,path,role,score) VALUES (?,?,?,?)
		 ON CONFLICT(repo,path,role) DO UPDATE SET score=excluded.score`,
		repo, path, role, score)
	return err
}

func (d *DB) RolesFor(repo, path string) ([]string, error) {
	rows, err := d.conn.Query(
		`SELECT role FROM roles WHERE repo=? AND path=? ORDER BY score DESC`, repo, path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type VersionRow struct{ SHA, CommittedAt, Repo, Path, Subject string }

func (d *DB) Versions(handle string) ([]VersionRow, error) {
	like := "%"
	if handle != "" {
		like = handle + "/%"
	}
	rows, err := d.conn.Query(
		`SELECT sha,committed_at,repo,path,subject FROM versions WHERE repo LIKE ? ORDER BY committed_at DESC`, like)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VersionRow
	for rows.Next() {
		var v VersionRow
		if err := rows.Scan(&v.SHA, &v.CommittedAt, &v.Repo, &v.Path, &v.Subject); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

type AppRow struct {
	SHA, Job string
	Latency  float64
}

func (d *DB) Applications(handle string) ([]AppRow, error) {
	like := "%"
	if handle != "" {
		like = handle + "/%"
	}
	rows, err := d.conn.Query(
		`SELECT sha,job,latency_days FROM applications WHERE repo LIKE ?`, like)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppRow
	for rows.Next() {
		var a AppRow
		if err := rows.Scan(&a.SHA, &a.Job, &a.Latency); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

type Discovered struct{ Repo, Path string }

// AllDiscovered lists every discovered candidate (for ingest — pull each one's provenance).
func (d *DB) AllDiscovered() ([]Discovered, error) {
	rows, err := d.conn.Query(`SELECT repo,path FROM discovered ORDER BY found_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Discovered
	for rows.Next() {
		var v Discovered
		if err := rows.Scan(&v.Repo, &v.Path); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (d *DB) RecentDiscovered(limit int) ([]Discovered, error) {
	rows, err := d.conn.Query(`SELECT repo,path FROM discovered ORDER BY found_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Discovered
	for rows.Next() {
		var v Discovered
		if err := rows.Scan(&v.Repo, &v.Path); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func ymd(s string) string {
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}
