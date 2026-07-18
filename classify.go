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
// SCAFFOLD, not the long-run design. A hand-maintained keyword list per category doesn't scale
// — long-term the categories and their signals should emerge from the corpus (and from the
// demand side, real job postings) rather than be curated. But while the corpus is thin, a
// curated list we can afford to keep gives coverage and a stable menu that sparse data can't.
// Languages/tools are included as signals precisely to lift coverage on generic resumes.
//
// Categories are NOT mutually exclusive: a profile holds every role it fits (a full-stack dev
// is both frontend and backend). This classifier already multi-tags; enrich does likewise with
// multiple strong roles. Never collapse a profile to one category.
//
// A role is assigned when >= roleThreshold distinct keywords hit; if none clear the bar, the
// single best hit is kept as a low-confidence tag. Keywords are lowercase; multiword allowed.
var roleKeywords = map[string][]string{
	"frontend":      {"frontend", "front-end", "front end", "react", "vue", "angular", "svelte", "next.js", "tailwind", "css", "web developer", "ui engineer", "javascript", "typescript", "html", "sass", "redux", "webpack"},
	"backend":       {"backend", "back-end", "back end", "rest api", "graphql", "django", "flask", "rails", "spring boot", "express", "node.js", "microservice", "grpc", "python", "java", "golang", "c#", ".net", "php", "laravel", "postgresql", "mysql", "mongodb", "redis"},
	"fullstack":     {"full-stack", "fullstack", "full stack", "mern", "mean stack"},
	"mobile":        {"ios", "android", "swift", "kotlin", "objective-c", "react native", "flutter", "mobile developer", "mobile engineer", "xcode", "swiftui", "jetpack compose"},
	"ml-ai":         {"machine learning", "deep learning", "pytorch", "tensorflow", "nlp", "computer vision", "data scientist", "ml engineer", "neural network", "llm", "ai engineer", "reinforcement learning", "scikit", "keras", "pandas", "numpy", "data science", "opencv"},
	"data-engineer": {"data engineer", "etl", "apache spark", "airflow", "kafka", "data pipeline", "data warehouse", "dbt", "snowflake", "databricks", "hadoop", "bigquery", "redshift"},
	"data-analyst":  {"data analyst", "analytics", "tableau", "power bi", "looker", "dashboard", "business intelligence", "sql", "excel", "statistics"},
	"devops-sre":    {"devops", "sre", "site reliability", "kubernetes", "docker", "terraform", "ansible", "ci/cd", "infrastructure", "platform engineer", "cloud engineer", "aws", "gcp", "azure", "jenkins", "prometheus", "grafana", "linux"},
	"security":      {"security engineer", "appsec", "infosec", "penetration", "pentest", "vulnerability", "cryptography", "reverse engineering", "malware", "ctf", "owasp", "siem", "threat"},
	"systems":       {"compiler", "operating system", "embedded", "firmware", "kernel", "systems programming", "low-level", "distributed systems", "high-performance", "rust", "c++", "assembly", "concurrency"},
	"qa-test":       {"quality assurance", "test automation", "sdet", "selenium", "cypress", "manual testing", "qa engineer", "junit", "pytest", "jest"},
	"game":          {"unity", "unreal engine", "game developer", "gameplay", "godot", "game engine", "shader"},
	"blockchain":    {"blockchain", "solidity", "web3", "smart contract", "ethereum", "defi", "nft"},
	"eng-manager":   {"engineering manager", "tech lead", "team lead", "people manager", "led a team", "managed a team", "director of engineering"},
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

// cmdCategories prints the curated job-category taxonomy with per-category corpus counts. The
// list is a first-class, browsable menu — useful precisely when the corpus is too thin for
// categories to emerge on their own. It is a scaffold: long-term, categories should be derived
// from the corpus and the demand side, not hand-kept.
func cmdCategories(db *platform.DB) error {
	counts, err := db.RoleCounts()
	if err != nil {
		return err
	}
	roles := make([]string, 0, len(roleKeywords))
	for r := range roleKeywords {
		roles = append(roles, r)
	}
	sort.Slice(roles, func(i, j int) bool { return counts[roles[i]] > counts[roles[j]] })
	fmt.Println("job categories (curated scaffold — thin-corpus bootstrap):")
	for _, r := range roles {
		fmt.Printf("  %-14s %3d resumes   %d keywords\n", r, counts[r], len(roleKeywords[r]))
	}
	return nil
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
