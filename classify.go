package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kimjune01/presume/forge"
	"github.com/kimjune01/presume/platform"
)

// Role taxonomy for the most common technical-recruiter queries. The classifier is a
// deterministic keyword matcher — transparent and re-runnable, not an opaque model — so a
// recruiter can see exactly why a resume carries a tag. Content is fetched ephemerally to
// derive tags (like `verify`) and never stored: the pointer-only model holds.
//
// A role is assigned when >= roleThreshold distinct keywords hit; if none clear the bar, the
// single best hit is kept as a low-confidence tag. Keywords are lowercase; multiword allowed.
var roleKeywords = map[string][]string{
	"frontend":      {"frontend", "front-end", "front end", "react", "vue", "angular", "svelte", "next.js", "tailwind", "css", "web developer", "ui engineer"},
	"backend":       {"backend", "back-end", "back end", "rest api", "graphql", "django", "flask", "rails", "spring boot", "express", "node.js", "microservice", "grpc"},
	"fullstack":     {"full-stack", "fullstack", "full stack", "mern", "mean stack"},
	"mobile":        {"ios", "android", "swift", "kotlin", "objective-c", "react native", "flutter", "mobile developer", "mobile engineer"},
	"ml-ai":         {"machine learning", "deep learning", "pytorch", "tensorflow", "nlp", "computer vision", "data scientist", "ml engineer", "neural network", "llm", "ai engineer", "reinforcement learning"},
	"data-engineer": {"data engineer", "etl", "apache spark", "airflow", "kafka", "data pipeline", "data warehouse", "dbt", "snowflake", "databricks"},
	"data-analyst":  {"data analyst", "analytics", "tableau", "power bi", "looker", "dashboard", "business intelligence"},
	"devops-sre":    {"devops", "sre", "site reliability", "kubernetes", "docker", "terraform", "ansible", "ci/cd", "infrastructure", "platform engineer", "cloud engineer"},
	"security":      {"security engineer", "appsec", "infosec", "penetration", "pentest", "vulnerability", "cryptography", "reverse engineering", "malware", "ctf"},
	"systems":       {"compiler", "operating system", "embedded", "firmware", "kernel", "systems programming", "low-level", "distributed systems", "high-performance"},
	"qa-test":       {"quality assurance", "test automation", "sdet", "selenium", "cypress", "manual testing", "qa engineer"},
	"game":          {"unity", "unreal engine", "game developer", "gameplay", "godot", "game engine"},
	"blockchain":    {"blockchain", "solidity", "web3", "smart contract", "ethereum", "defi"},
}

const roleThreshold = 2

type roleHit struct {
	role  string
	score int
}

// classifyText returns role tags, strongest first: all roles with >= roleThreshold keyword
// hits, or the single best if none clear the bar.
func classifyText(text string) []roleHit {
	low := strings.ToLower(text)
	var scored []roleHit
	for role, kws := range roleKeywords {
		n := 0
		for _, k := range kws {
			if strings.Contains(low, k) {
				n++
			}
		}
		if n > 0 {
			scored = append(scored, roleHit{role, n})
		}
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	var out []roleHit
	for _, s := range scored {
		if s.score >= roleThreshold {
			out = append(out, s)
		}
	}
	if len(out) == 0 && len(scored) > 0 {
		out = append(out, scored[0]) // best-effort low-confidence tag
	}
	return out
}

// cmdClassify derives role tags for every ingested resume from its latest version's content.
func cmdClassify(db *platform.DB, gh *forge.Client) error {
	resumes, err := db.DistinctResumes()
	if err != nil {
		return err
	}
	done, tagged := 0, 0
	counts := map[string]int{}
	for _, r := range resumes {
		done++
		sha, err := db.LatestSHA(r.Repo, r.Path)
		if err != nil || sha == "" {
			continue
		}
		content, err := gh.Content(r.Repo, r.Path, sha)
		if err != nil {
			continue
		}
		hits := classifyText(string(content.Bytes))
		if err := db.ClearRoles(r.Repo, r.Path); err != nil {
			return err
		}
		for _, h := range hits {
			if err := db.UpsertRole(r.Repo, r.Path, h.role, h.score); err != nil {
				return err
			}
			counts[h.role]++
		}
		if len(hits) > 0 {
			tagged++
		}
		if done%25 == 0 {
			fmt.Printf("  classified %d/%d…\n", done, len(resumes))
		}
	}
	fmt.Printf("classified %d resumes, %d tagged. role distribution:\n", done, tagged)
	type rc struct {
		role string
		n    int
	}
	var dist []rc
	for role, n := range counts {
		dist = append(dist, rc{role, n})
	}
	sort.Slice(dist, func(i, j int) bool { return dist[i].n > dist[j].n })
	for _, d := range dist {
		fmt.Printf("  %-14s %d\n", d.role, d.n)
	}
	return nil
}
