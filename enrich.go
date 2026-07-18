package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/kimjune01/presume/forge"
	"github.com/kimjune01/presume/platform"
)

// Enrichment is the Haiku quality pass — the judgment a keyword matcher can't make: is this a
// real personal resume, what is its PRIMARY role, and is the content actually in the file (prose)
// vs a PDF-wrapper / tombstone / stub. It reads content ephemerally (like verify) and stores only
// the derived verdict. A model classifier is a pragmatic bootstrap, not the re-runnable ideal —
// so we keep the output structured and inspectable (primary role + a short note), and it only
// ever refines tags and masks, never touches the provenance record itself.

// Roles are NOT mutually exclusive — a profile legitimately holds several. Enrich asks for all
// applicable roles, split only by confidence (strong = clearly is; present = has touched), never
// collapsed to one primary. A full-stack engineer is strong in BOTH frontend and backend.
const enrichPrompt = `You classify a GitHub file that may or may not be one person's personal resume/CV.
Return ONLY minified JSON, no code fences, no prose:
{"is_resume":true|false,"kind":"prose|pdf-wrapper|tombstone|stub","strong_roles":["<role>"],"present_roles":["<role>"],"seniority":"intern|junior|mid|senior|staff|principal|lead|manager|unknown","note":"<=8 words"}

is_resume: true only if this is ONE person's resume/CV (their work history/skills). false for templates, tools, course/demo files, docs, session notes.
kind: "prose" if the resume text is IN this file; "pdf-wrapper" if it mostly links/embeds a PDF/DOC with little text; "tombstone" if it says the resume was removed / contact me; "stub" if near-empty.
Roles are NOT mutually exclusive — assign ALL that fit, from exactly this set:
  frontend backend fullstack mobile ml-ai data-engineer data-analyst devops-sre security systems qa-test game blockchain eng-manager
strong_roles: roles the person CLEARLY is (may be several — a full-stack dev is strong in both frontend and backend).
present_roles: roles they've touched but aren't central. Both lists may be empty.

FILE: %s/%s
CONTENT:
%s`

type judgment struct {
	IsResume     bool     `json:"is_resume"`
	Kind         string   `json:"kind"`
	StrongRoles  []string `json:"strong_roles"`
	PresentRoles []string `json:"present_roles"`
	Seniority    string   `json:"seniority"`
	Note         string   `json:"note"`
}

// haiku runs one Claude Haiku classification via the CLI and returns the raw stdout.
func haiku(prompt string) (string, error) {
	cmd := exec.Command("claude", "-p", "--model", "claude-haiku-4-5")
	cmd.Stdin = strings.NewReader(prompt)
	out, err := cmd.Output()
	return string(out), err
}

// parseJudgment extracts the JSON object from Haiku's reply (tolerating ```json fences / prose).
func parseJudgment(s string) (judgment, error) {
	var j judgment
	a, b := strings.IndexByte(s, '{'), strings.LastIndexByte(s, '}')
	if a < 0 || b <= a {
		return j, fmt.Errorf("no JSON in reply")
	}
	return j, json.Unmarshal([]byte(s[a:b+1]), &j)
}

func cmdEnrich(db *platform.DB, gh *forge.Client, limit int, force bool) error {
	resumes, err := db.ResumesToEnrich(force, limit)
	if err != nil {
		return err
	}
	now := nowUTC()
	done, masked, retagged, failed := 0, 0, 0, 0
	for _, r := range resumes {
		done++
		if force {
			db.Unmask(r.Repo, r.Path) // clear any prior enrich mask so the re-run can reconsider
		}
		sha, err := db.LatestSHA(r.Repo, r.Path)
		if err != nil || sha == "" {
			failed++
			continue
		}
		content, err := gh.Content(r.Repo, r.Path, sha)
		if err != nil {
			failed++
			continue
		}
		body := string(content.Bytes)
		if len(body) > 3500 {
			body = body[:3500]
		}
		reply, err := haiku(fmt.Sprintf(enrichPrompt, r.Repo, r.Path, body))
		if err != nil {
			failed++
			continue
		}
		j, err := parseJudgment(reply)
		if err != nil {
			failed++
			continue
		}
		// judgments.primary_role holds the strong-role SET (comma-joined) — a headline, not an
		// exclusive category. Roles are multi-membership.
		if err := db.UpsertJudgment(r.Repo, r.Path, j.IsResume, j.Kind, strings.Join(j.StrongRoles, ","), j.Seniority, j.Note, now); err != nil {
			return err
		}
		// non-resume or non-prose (wrapper/tombstone/stub) → mask; else replace keyword tags with
		// Haiku's roles: every strong role at full weight (no forced primary), present ones weak.
		if !j.IsResume || j.Kind != "prose" {
			reason := "enrich: not a resume"
			if j.IsResume {
				reason = "enrich: " + j.Kind
			}
			if err := db.Mask(r.Repo, r.Path, reason, now); err != nil {
				return err
			}
			masked++
		} else {
			db.ClearRoles(r.Repo, r.Path)
			for _, s := range j.StrongRoles {
				if s != "" {
					db.UpsertRole(r.Repo, r.Path, s, 3)
				}
			}
			for _, s := range j.PresentRoles {
				if s != "" {
					db.UpsertRole(r.Repo, r.Path, s, 1)
				}
			}
			retagged++
		}
		if done%10 == 0 {
			fmt.Printf("  enriched %d/%d… (masked %d, retagged %d)\n", done, len(resumes), masked, retagged)
		}
	}
	fmt.Printf("enriched %d resumes — %d masked (non-resume/wrapper), %d retagged, %d failed\n",
		done, masked, retagged, failed)
	return nil
}
