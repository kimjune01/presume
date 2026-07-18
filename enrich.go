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

const enrichPrompt = `You classify a GitHub file that may or may not be one person's personal resume/CV.
Return ONLY minified JSON, no code fences, no prose:
{"is_resume":true|false,"kind":"prose|pdf-wrapper|tombstone|stub","primary_role":"<role or empty>","secondary_roles":["<role>"],"seniority":"intern|junior|mid|senior|staff|principal|lead|manager|unknown","note":"<=8 words"}

is_resume: true only if this is ONE person's resume/CV (their work history/skills). false for templates, tools, course/demo files, docs, session notes.
kind: "prose" if the resume text is IN this file; "pdf-wrapper" if it mostly links/embeds a PDF/DOC with little text; "tombstone" if it says the resume was removed / contact me; "stub" if near-empty.
primary_role: the SINGLE best fit from exactly this set, or "" if unclear:
  frontend backend fullstack mobile ml-ai data-engineer data-analyst devops-sre security systems qa-test game blockchain eng-manager
secondary_roles: 0-3 others from that same set.

FILE: %s/%s
CONTENT:
%s`

type judgment struct {
	IsResume       bool     `json:"is_resume"`
	Kind           string   `json:"kind"`
	PrimaryRole    string   `json:"primary_role"`
	SecondaryRoles []string `json:"secondary_roles"`
	Seniority      string   `json:"seniority"`
	Note           string   `json:"note"`
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
		if err := db.UpsertJudgment(r.Repo, r.Path, j.IsResume, j.Kind, j.PrimaryRole, j.Seniority, j.Note, now); err != nil {
			return err
		}
		// non-resume or non-prose (wrapper/tombstone/stub) → mask; else replace keyword tags
		// with Haiku's primary (strong) + secondary (weak) roles.
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
			if j.PrimaryRole != "" {
				db.UpsertRole(r.Repo, r.Path, j.PrimaryRole, 3)
			}
			for _, s := range j.SecondaryRoles {
				if s != "" && s != j.PrimaryRole {
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
