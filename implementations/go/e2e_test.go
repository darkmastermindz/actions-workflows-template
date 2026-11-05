package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func init() { sleep = func(time.Duration) {} }

type recReq struct {
	Method, URI, Body string
	Header            http.Header
}

type mockAPI struct {
	t           *testing.T
	jobs        []json.RawMessage
	omitTotal   bool
	comments    []map[string]any
	jobsStatus  []int // statuses to return before success (per jobs request sequence)
	commentFail int   // status to return for comment listing (0 = ok)
	mu          sync.Mutex
	reqs        []recReq
	srv         *httptest.Server
}

func newMock(t *testing.T) *mockAPI {
	m := &mockAPI{t: t}
	m.srv = httptest.NewServer(http.HandlerFunc(m.handle))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockAPI) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	m.reqs = append(m.reqs, recReq{r.Method, r.URL.RequestURI(), string(body), r.Header.Clone()})
	m.mu.Unlock()
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	switch {
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/owner/repo/actions/runs/123/jobs"):
		m.mu.Lock()
		if len(m.jobsStatus) > 0 {
			st := m.jobsStatus[0]
			m.jobsStatus = m.jobsStatus[1:]
			m.mu.Unlock()
			w.WriteHeader(st)
			return
		}
		m.mu.Unlock()
		lo, hi := (page-1)*100, page*100
		if lo > len(m.jobs) {
			lo = len(m.jobs)
		}
		if hi > len(m.jobs) {
			hi = len(m.jobs)
		}
		var b bytes.Buffer
		if m.omitTotal {
			b.WriteString(`{"jobs":[`)
		} else {
			fmt.Fprintf(&b, `{"total_count":%d,"jobs":[`, len(m.jobs))
		}
		for i, j := range m.jobs[lo:hi] {
			if i > 0 {
				b.WriteByte(',')
			}
			b.Write(j)
		}
		b.WriteString("]}")
		w.Write(b.Bytes())
	case r.Method == "GET" && r.URL.Path == "/repos/owner/repo/issues/42/comments":
		if m.commentFail != 0 {
			w.WriteHeader(m.commentFail)
			return
		}
		lo, hi := (page-1)*100, page*100
		if lo > len(m.comments) {
			lo = len(m.comments)
		}
		if hi > len(m.comments) {
			hi = len(m.comments)
		}
		json.NewEncoder(w).Encode(m.comments[lo:hi])
	case r.Method == "POST" && r.URL.Path == "/repos/owner/repo/issues/42/comments":
		w.WriteHeader(201)
		w.Write([]byte(`{"id":1}`))
	case r.Method == "PATCH" && strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/comments/"):
		w.Write([]byte(`{"id":1}`))
	default:
		w.WriteHeader(404)
	}
}

func (m *mockAPI) requests() []recReq {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]recReq(nil), m.reqs...)
}

type result struct {
	code              int
	stdout, stderr    string
	summary, output   string
	reportPath, jsonF string
}

func goldenJobs(t *testing.T) []json.RawMessage {
	var p struct{ Jobs []json.RawMessage }
	if err := json.Unmarshal([]byte(goldenJobsJSON), &p); err != nil {
		t.Fatal(err)
	}
	return p.Jobs
}

// runWith executes run() in-process with a hermetic environment.
func runWith(t *testing.T, m *mockAPI, env map[string]string) result {
	t.Helper()
	dir := t.TempDir()
	evPath := filepath.Join(dir, "event.json")
	os.WriteFile(evPath, []byte(`{"workflow_run":{"id":123,"pull_requests":[{"number":42}]}}`), 0o644)
	base := map[string]string{
		"INPUT_GITHUB_TOKEN":  "s3cr3t-token",
		"GITHUB_REPOSITORY":   "owner/repo",
		"GITHUB_SERVER_URL":   "https://github.com",
		"GITHUB_EVENT_PATH":   evPath,
		"GITHUB_STEP_SUMMARY": filepath.Join(dir, "summary.md"),
		"GITHUB_OUTPUT":       filepath.Join(dir, "output.txt"),
		"RUNNER_TEMP":         dir,
	}
	if m != nil {
		base["GITHUB_API_URL"] = m.srv.URL + "/"
	}
	for k, v := range env {
		if v == "<unset>" {
			delete(base, k)
		} else {
			base[k] = v
		}
	}
	var so, se bytes.Buffer
	code := run(func(k string) string { return base[k] }, &so, &se)
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }
	return result{code, so.String(), se.String(), read(filepath.Join(dir, "summary.md")),
		read(filepath.Join(dir, "output.txt")), filepath.Join(dir, "ci-ledger-report.json"),
		read(filepath.Join(dir, "ci-ledger-report.json"))}
}

func TestE2EGolden(t *testing.T) {
	m := newMock(t)
	m.jobs = goldenJobs(t)
	r := runWith(t, m, map[string]string{"INPUT_BUDGET_USD": "0.50"})
	if r.code != 0 {
		t.Errorf("exit %d", r.code)
	}
	if r.summary != goldenBody+"\n" {
		t.Errorf("summary mismatch:\n%s", r.summary)
	}
	wantOut := "CI Ledger comment created on PR #42\n" +
		"::notice::CI Ledger run total $0.5620 (26 min, 6 jobs)\n" +
		"::warning::Run cost $0.5620 exceeds budget $0.50\n"
	if r.stdout != wantOut {
		t.Errorf("stdout mismatch:\n%q\nwant\n%q", r.stdout, wantOut)
	}
	wantGO := "total_cost=0.5620\ntotal_minutes=26\njob_count=6\nover_budget=true\nreport_path=" + r.reportPath + "\n"
	if r.output != wantGO {
		t.Errorf("GITHUB_OUTPUT mismatch:\n%q\nwant\n%q", r.output, wantGO)
	}
	var js map[string]any
	if err := json.Unmarshal([]byte(r.jsonF), &js); err != nil || js["total_cost_usd"] != 0.562 {
		t.Errorf("json report: %v %s", err, r.jsonF)
	}
	if strings.Contains(r.stdout+r.stderr, "s3cr3t-token") {
		t.Error("token leaked")
	}

	reqs := m.requests()
	if len(reqs) != 3 {
		t.Fatalf("expected 3 requests, got %d: %+v", len(reqs), reqs)
	}
	if reqs[0].Method != "GET" || reqs[0].URI != "/repos/owner/repo/actions/runs/123/jobs?per_page=100&page=1" {
		t.Errorf("jobs request: %s %s", reqs[0].Method, reqs[0].URI)
	}
	if reqs[1].URI != "/repos/owner/repo/issues/42/comments?per_page=100&page=1" {
		t.Errorf("comments request: %s", reqs[1].URI)
	}
	if reqs[2].Method != "POST" || reqs[2].URI != "/repos/owner/repo/issues/42/comments" {
		t.Errorf("post request: %s %s", reqs[2].Method, reqs[2].URI)
	}
	var posted map[string]string
	if err := json.Unmarshal([]byte(reqs[2].Body), &posted); err != nil || posted["body"] != goldenBody {
		t.Errorf("posted body mismatch: %v %q", err, reqs[2].Body)
	}
	for i, q := range reqs {
		h := q.Header
		if h.Get("Authorization") != "Bearer s3cr3t-token" || h.Get("Accept") != "application/vnd.github+json" ||
			h.Get("X-GitHub-Api-Version") != "2022-11-28" || h.Get("User-Agent") != "ci-ledger" {
			t.Errorf("request %d headers: %v", i, h)
		}
		wantCT := ""
		if q.Method == "POST" {
			wantCT = "application/json"
		}
		if h.Get("Content-Type") != wantCT {
			t.Errorf("request %d Content-Type %q", i, h.Get("Content-Type"))
		}
	}
}

func TestE2EFailOnBudgetAndTopN(t *testing.T) {
	m := newMock(t)
	m.jobs = goldenJobs(t)
	r := runWith(t, m, map[string]string{"INPUT_BUDGET_USD": "0.50", "INPUT_FAIL_ON_BUDGET": "true"})
	if r.code != 1 {
		t.Errorf("fail_on_budget exit %d", r.code)
	}
	r = runWith(t, m, map[string]string{"INPUT_BUDGET_USD": "0.50", "INPUT_TOP_N": "4"})
	want := strings.Replace(goldenBody, "| lint | linux | 1 | $0.0060 |\n| deploy | self-hosted linux | 5 | $0.0000 |\n", "| … +2 more | | | |\n", 1)
	if r.code != 0 || r.summary != want+"\n" {
		t.Errorf("top_n=4 summary:\n%s", r.summary)
	}
}

func TestE2EPatchOnLaterPage(t *testing.T) {
	m := newMock(t)
	m.jobs = goldenJobs(t)
	for i := 0; i < 300; i++ {
		body := fmt.Sprintf("comment %d", i)
		if i == 250 || i == 280 {
			body = "old report\n" + Marker + "\nstuff"
		}
		m.comments = append(m.comments, map[string]any{"id": 1000 + i, "body": body})
	}
	r := runWith(t, m, nil)
	if r.code != 0 {
		t.Errorf("exit %d", r.code)
	}
	if !strings.HasPrefix(r.stdout, "CI Ledger comment updated on PR #42\n") {
		t.Errorf("stdout: %q", r.stdout)
	}
	reqs := m.requests()
	last := reqs[len(reqs)-1]
	if last.Method != "PATCH" || last.URI != "/repos/owner/repo/issues/comments/1250" {
		t.Errorf("last request %s %s", last.Method, last.URI)
	}
	if last.Header.Get("Content-Type") != "application/json" {
		t.Error("PATCH content-type")
	}
	var posted map[string]string
	json.Unmarshal([]byte(last.Body), &posted)
	if !strings.HasPrefix(posted["body"], Marker+"\n") || strings.Contains(posted["body"], "**Budget") {
		t.Errorf("patched body: %q", posted["body"])
	}
	var pages []string
	for _, q := range reqs {
		if strings.Contains(q.URI, "/issues/42/comments?") {
			pages = append(pages, q.URI[strings.LastIndex(q.URI, "=")+1:])
		}
	}
	if strings.Join(pages, ",") != "1,2,3" {
		t.Errorf("comment pages fetched: %v", pages)
	}
}

func TestE2ECommentPagination300NoMarker(t *testing.T) {
	m := newMock(t)
	m.jobs = goldenJobs(t)
	for i := 0; i < 300; i++ {
		m.comments = append(m.comments, map[string]any{"id": i, "body": "x"})
	}
	r := runWith(t, m, nil)
	if !strings.HasPrefix(r.stdout, "CI Ledger comment created on PR #42\n") {
		t.Errorf("stdout %q", r.stdout)
	}
	n := 0
	for _, q := range m.requests() {
		if strings.Contains(q.URI, "/issues/42/comments?") {
			n++
		}
	}
	if n != 4 { // 100,100,100,0
		t.Errorf("comment page requests = %d, want 4", n)
	}
}

func makeJobs(n int) []json.RawMessage {
	labels := []string{`["ubuntu-latest"]`, `["windows-latest"]`, `["macos-14"]`, `["ubuntu-latest-8-cores"]`, `["self-hosted","linux"]`}
	out := make([]json.RawMessage, n)
	for i := 0; i < n; i++ {
		secs := 30 + (i*37)%900
		end := time.Date(2026, 6, 14, 1, 0, 0, 0, time.UTC).Add(time.Duration(secs) * time.Second)
		out[i] = json.RawMessage(fmt.Sprintf(
			`{"id":%d,"name":"job %d","labels":%s,"status":"completed","started_at":"2026-06-14T01:00:00Z","completed_at":"%s","steps":[{"name":"s","status":"completed"}]}`,
			i, i, labels[i%len(labels)], end.Format(time.RFC3339)))
	}
	return out
}

func check1000(t *testing.T, omitTotal bool) {
	m := newMock(t)
	m.jobs = makeJobs(1000)
	m.omitTotal = omitTotal
	r := runWith(t, m, map[string]string{"INPUT_PR_NUMBER": "", "GITHUB_EVENT_PATH": "<unset>", "GITHUB_RUN_ID": "123"})
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stdout)
	}
	if !strings.Contains(r.output, "job_count=1000\n") {
		t.Errorf("output: %s", r.output)
	}
	if !strings.HasPrefix(r.stdout, "CI Ledger: no PR context; wrote job summary only\n::notice::") {
		t.Errorf("stdout: %q", r.stdout)
	}
	if !strings.Contains(r.summary, "| … +985 more | | | |\n") {
		t.Error("expected truncation row")
	}
	// cross-check totals with the pure functions
	var jobs []Job
	for _, raw := range m.jobs {
		var j Job
		decode(raw, &j)
		jobs = append(jobs, j)
	}
	rows, total, mins, _ := Compute(jobs, DefaultRates())
	want := fmt.Sprintf("total_cost=%s\ntotal_minutes=%d\njob_count=%d\n", Fmt4Plain(total), mins, len(rows))
	if !strings.HasPrefix(r.output, want) {
		t.Errorf("output %q want prefix %q", r.output, want)
	}
	pages := map[string]int{}
	for _, q := range m.requests() {
		pages[q.URI[strings.LastIndex(q.URI, "=")+1:]]++
	}
	if len(pages) != 11 {
		t.Errorf("job pages requested: %v (want 1..11 once each)", pages)
	}
	for p, n := range pages {
		if n != 1 {
			t.Errorf("page %s requested %d times", p, n)
		}
	}
}

func TestE2E1000Jobs(t *testing.T)             { check1000(t, false) }
func TestE2E1000JobsNoTotalCount(t *testing.T) { check1000(t, true) }

func TestE2ERetriesAndFailures(t *testing.T) {
	m := newMock(t)
	m.jobs = goldenJobs(t)
	m.jobsStatus = []int{502, 429}
	r := runWith(t, m, map[string]string{"INPUT_COMMENT": "false"})
	if r.code != 0 || !strings.HasPrefix(r.stdout, "CI Ledger: comment disabled; wrote job summary only\n") {
		t.Errorf("retry success: %d %q", r.code, r.stdout)
	}
	if len(m.requests()) != 3 {
		t.Errorf("want 3 attempts, got %d", len(m.requests()))
	}

	m2 := newMock(t)
	m2.jobsStatus = []int{500, 503, 500, 200}
	r = runWith(t, m2, nil)
	if r.code != 0 || r.stdout != "::warning::CI Ledger could not fetch jobs (HTTP 500); skipping\n" || r.summary != "" || r.output != "" || r.jsonF != "" {
		t.Errorf("exhausted retries: %d %q", r.code, r.stdout)
	}
	if len(m2.requests()) != 3 {
		t.Errorf("want 3 attempts, got %d", len(m2.requests()))
	}

	m3 := newMock(t)
	m3.jobsStatus = []int{403}
	r = runWith(t, m3, nil)
	if r.stdout != "::warning::CI Ledger could not fetch jobs (HTTP 403); skipping\n" || len(m3.requests()) != 1 {
		t.Errorf("4xx no retry: %q %d", r.stdout, len(m3.requests()))
	}

	r = runWith(t, nil, map[string]string{"GITHUB_API_URL": "http://127.0.0.1:1"})
	if r.code != 0 || r.stdout != "::warning::CI Ledger could not fetch jobs (network error); skipping\n" {
		t.Errorf("network error: %d %q", r.code, r.stdout)
	}

	m4 := newMock(t)
	m4.jobs = goldenJobs(t)
	m4.commentFail = 403
	r = runWith(t, m4, nil)
	if r.code != 0 || !strings.HasPrefix(r.stdout, "::warning::CI Ledger could not post comment (HTTP 403)\n::notice::") {
		t.Errorf("comment failure: %q", r.stdout)
	}
}

func TestE2EConfigErrorsAndWarnings(t *testing.T) {
	r := runWith(t, nil, map[string]string{"INPUT_GITHUB_TOKEN": "", "GITHUB_TOKEN": ""})
	if r.code != 1 || r.stdout != "::error::GITHUB_TOKEN and GITHUB_REPOSITORY are required\n" {
		t.Errorf("token: %d %q", r.code, r.stdout)
	}
	r = runWith(t, nil, map[string]string{"GITHUB_REPOSITORY": "", "GITHUB_EVENT_PATH": "/nonexistent"})
	if r.code != 1 || r.stdout != "::error::GITHUB_TOKEN and GITHUB_REPOSITORY are required\n" {
		t.Errorf("repo: %d %q", r.code, r.stdout)
	}
	r = runWith(t, nil, map[string]string{"GITHUB_EVENT_PATH": "/nonexistent"})
	if r.code != 1 || r.stdout != "::error::could not resolve a run id to analyze\n" {
		t.Errorf("run id: %d %q", r.code, r.stdout)
	}

	m := newMock(t)
	m.jobs = goldenJobs(t)
	r = runWith(t, m, map[string]string{
		"INPUT_GITHUB_TOKEN": "", "GITHUB_TOKEN": "s3cr3t-token",
		"INPUT_RATES_JSON": "{bad", "INPUT_BUDGET_USD": " 1.2.3 ",
	})
	want := "::warning::CI Ledger ignored invalid rates_json\n::warning::CI Ledger ignored invalid budget_usd ' 1.2.3 '\nCI Ledger comment created on PR #42\n::notice::CI Ledger run total $0.5620 (26 min, 6 jobs)\n"
	if r.code != 0 || r.stdout != want {
		t.Errorf("warnings: %q", r.stdout)
	}
	if !strings.Contains(r.output, "over_budget=false\n") || strings.Contains(r.summary, "**Budget") {
		t.Error("invalid budget should mean no budget")
	}

	// rates override + PR from pull_request event + under budget
	dir := t.TempDir()
	ev := filepath.Join(dir, "ev.json")
	os.WriteFile(ev, []byte(`{"pull_request":{"number":42}}`), 0o644)
	r = runWith(t, m, map[string]string{
		"GITHUB_EVENT_PATH": ev, "GITHUB_RUN_ID": "123", "INPUT_BUDGET_USD": "5",
		"INPUT_RATES_JSON": `{"self_hosted": 0.002}`,
	})
	if !strings.Contains(r.summary, "| deploy | self-hosted linux | 5 | $0.0100 |") ||
		!strings.Contains(r.summary, "**Budget $5.00/run: 🟢 ok**") ||
		!strings.Contains(r.stdout, "::notice::CI Ledger run total $0.5720 (26 min, 6 jobs)\n") {
		t.Errorf("override run:\n%s\n%s", r.summary, r.stdout)
	}
}

func TestE2EStableSortTies(t *testing.T) {
	m := newMock(t)
	m.jobs = []json.RawMessage{
		json.RawMessage(`{"name":"z","labels":["ubuntu-latest"],"status":"completed","started_at":"2026-06-14T01:00:00Z","completed_at":"2026-06-14T01:01:00Z"}`),
		json.RawMessage(`{"name":"a","labels":["ubuntu-latest"],"status":"completed","started_at":"2026-06-14T01:00:00Z","completed_at":"2026-06-14T01:01:00Z"}`),
		json.RawMessage(`{"name":"sh-long","labels":["self-hosted"],"status":"completed","started_at":"2026-06-14T01:00:00Z","completed_at":"2026-06-14T01:09:00Z"}`),
		json.RawMessage(`{"labels":null,"status":null,"started_at":"2026-06-14T01:00:00Z","completed_at":"2026-06-14T01:00:00.5Z"}`),
		json.RawMessage(`{"name":"sh-short","labels":["self-hosted"],"started_at":null}`),
	}
	r := runWith(t, m, map[string]string{"INPUT_COMMENT": "no"})
	want := "| z | linux | 1 | $0.0060 |\n| a | linux | 1 | $0.0060 |\n| ? | linux | 1 | $0.0060 |\n| sh-long | self-hosted linux | 9 | $0.0000 |\n| sh-short | self-hosted linux | 0 | $0.0000 |\n\n<sub>"
	if !strings.Contains(r.summary, want) {
		t.Errorf("sort order:\n%s", r.summary)
	}
}

func BenchmarkE2E1000Jobs300Comments(b *testing.B) {
	t := &testing.T{}
	m := &mockAPI{t: t}
	m.srv = httptest.NewServer(http.HandlerFunc(m.handle))
	defer m.srv.Close()
	m.jobs = makeJobs(1000)
	for i := 0; i < 300; i++ {
		m.comments = append(m.comments, map[string]any{"id": i, "body": strings.Repeat("lorem ipsum ", 40)})
	}
	dir := b.TempDir()
	ev := filepath.Join(dir, "ev.json")
	os.WriteFile(ev, []byte(`{"workflow_run":{"id":123,"pull_requests":[{"number":42}]}}`), 0o644)
	env := map[string]string{"INPUT_GITHUB_TOKEN": "t", "GITHUB_REPOSITORY": "owner/repo", "GITHUB_API_URL": m.srv.URL,
		"GITHUB_EVENT_PATH": ev, "RUNNER_TEMP": dir}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		run(func(k string) string { return env[k] }, io.Discard, io.Discard)
	}
}
