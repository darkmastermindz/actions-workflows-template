// Command ci-ledger attributes a USD cost to every job of a GitHub Actions
// workflow run. Configured only through environment variables (see
// REQUIREMENTS.md §2.1); takes no arguments.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	os.Exit(run(os.Getenv, os.Stdout, os.Stderr))
}

// loadEvent parses GITHUB_EVENT_PATH; missing/invalid → empty object (§4).
func loadEvent(path string) map[string]any {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var ev map[string]any
	if decode(data, &ev) != nil {
		return nil
	}
	return ev
}

// scalarString renders a JSON scalar as the string used in URLs/text.
func scalarString(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case json.Number:
		return x.String()
	}
	return ""
}

func dig(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func appendFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// run executes the action and returns the process exit code.
func run(getenv func(string) string, stdout, stderr io.Writer) (code int) {
	out := bufio.NewWriterSize(stdout, 4096)
	defer out.Flush()
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(stderr, "ci-ledger: internal error: %v\n", r)
			out.WriteString("::warning::CI Ledger internal error; skipping\n")
			code = 0
		}
	}()

	// §4 configuration.
	token := firstNonEmpty(getenv("INPUT_GITHUB_TOKEN"), getenv("GITHUB_TOKEN"))
	repo := getenv("GITHUB_REPOSITORY")
	if token == "" || repo == "" {
		out.WriteString("::error::GITHUB_TOKEN and GITHUB_REPOSITORY are required\n")
		return 1
	}
	var event any = loadEvent(getenv("GITHUB_EVENT_PATH"))
	runID := firstNonEmpty(
		strings.TrimSpace(getenv("INPUT_RUN_ID")),
		scalarString(dig(event, "workflow_run", "id")),
		strings.TrimSpace(getenv("GITHUB_RUN_ID")),
	)
	if runID == "" {
		out.WriteString("::error::could not resolve a run id to analyze\n")
		return 1
	}
	var prFromList any
	if prs, ok := dig(event, "workflow_run", "pull_requests").([]any); ok && len(prs) > 0 {
		prFromList = dig(prs[0], "number")
	}
	pr := firstNonEmpty(
		strings.TrimSpace(getenv("INPUT_PR_NUMBER")),
		scalarString(dig(event, "pull_request", "number")),
		scalarString(prFromList),
	)

	api := strings.TrimRight(firstNonEmpty(getenv("GITHUB_API_URL"), "https://api.github.com"), "/")
	server := strings.TrimRight(firstNonEmpty(getenv("GITHUB_SERVER_URL"), "https://github.com"), "/")
	topN := ParseTopN(getenv("INPUT_TOP_N"))
	commentOn := ParseBool(getenv("INPUT_COMMENT"), true)
	failOnBudget := ParseBool(getenv("INPUT_FAIL_ON_BUDGET"), false)

	// §7.2 then §8 warnings, before anything else.
	rates := DefaultRates()
	if raw := getenv("INPUT_RATES_JSON"); strings.TrimSpace(raw) != "" {
		if !ApplyRatesJSON(rates, raw) {
			rates = DefaultRates()
			out.WriteString("::warning::CI Ledger ignored invalid rates_json\n")
		}
	}
	rawBudget := getenv("INPUT_BUDGET_USD")
	budgetMicro, hasBudget, valid := ParseBudget(rawBudget)
	if !valid {
		out.WriteString("::warning::CI Ledger ignored invalid budget_usd '" + rawBudget + "'\n")
	}

	c := newClient(api, repo, token, stderr)
	jobs, err := c.FetchJobs(runID)
	if err != nil {
		out.WriteString("::warning::CI Ledger could not fetch jobs (" + describe(err) + "); skipping\n")
		return 0
	}

	rows, total, minutes, inProgress := Compute(jobs, rates)
	rep := &Report{
		Repo: repo, RunID: runID, Server: server,
		Rows: rows, TotalMicro: total, TotalMinutes: minutes, InProgress: inProgress,
		HasBudget: hasBudget, BudgetMicro: budgetMicro,
		Over: hasBudget && total > budgetMicro, TopN: topN,
	}
	body := RenderMarkdown(rep)

	// §12 side effects, in order.
	if p := getenv("GITHUB_STEP_SUMMARY"); p != "" {
		if err := appendFile(p, append([]byte(body), '\n')); err != nil {
			fmt.Fprintf(stderr, "ci-ledger: could not write job summary: %v\n", err)
		}
	}
	dir := getenv("RUNNER_TEMP")
	if dir == "" {
		dir = os.TempDir()
	}
	reportPath := strings.TrimRight(dir, `/\`) + string(os.PathSeparator) + "ci-ledger-report.json"
	if strings.TrimRight(dir, `/\`) == "" { // dir was "/" itself
		reportPath = dir[:1] + "ci-ledger-report.json"
	}
	if err := os.WriteFile(reportPath, RenderJSON(rep), 0o644); err != nil {
		fmt.Fprintf(stderr, "ci-ledger: could not write JSON report: %v\n", err)
	}
	if p := getenv("GITHUB_OUTPUT"); p != "" {
		over := "false"
		if rep.Over {
			over = "true"
		}
		var sb strings.Builder
		sb.WriteString("total_cost=" + Fmt4Plain(total) + "\n")
		sb.WriteString(fmt.Sprintf("total_minutes=%d\n", minutes))
		sb.WriteString(fmt.Sprintf("job_count=%d\n", len(rows)))
		sb.WriteString("over_budget=" + over + "\n")
		sb.WriteString("report_path=" + reportPath + "\n")
		if err := appendFile(p, []byte(sb.String())); err != nil {
			fmt.Fprintf(stderr, "ci-ledger: could not write step outputs: %v\n", err)
		}
	}

	switch {
	case pr == "":
		out.WriteString("CI Ledger: no PR context; wrote job summary only\n")
	case !commentOn:
		out.WriteString("CI Ledger: comment disabled; wrote job summary only\n")
	default:
		if action, err := c.UpsertComment(pr, body); err != nil {
			out.WriteString("::warning::CI Ledger could not post comment (" + describe(err) + ")\n")
		} else {
			out.WriteString("CI Ledger comment " + action + " on PR #" + pr + "\n")
		}
	}

	out.WriteString(fmt.Sprintf("::notice::CI Ledger run total %s (%d min, %d jobs)\n", Fmt4(total), minutes, len(rows)))
	if rep.Over {
		out.WriteString("::warning::Run cost " + Fmt4(total) + " exceeds budget " + Fmt2(budgetMicro) + "\n")
		if failOnBudget {
			return 1
		}
	}
	return 0
}

func describe(err error) string {
	if e, ok := err.(*apiError); ok {
		return e.Describe()
	}
	return "network error"
}
