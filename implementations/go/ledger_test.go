package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// §5 examples
func TestDetectRunner(t *testing.T) {
	cases := []struct {
		labels []string
		os     string
		cores  int
		self   bool
	}{
		{[]string{"ubuntu-latest"}, "linux", 2, false},
		{[]string{"windows-latest"}, "windows", 2, false},
		{[]string{"macos-14"}, "macos", 2, false},
		{[]string{"self-hosted", "linux", "x64"}, "linux", 2, true},
		{[]string{"self-hosted", "darwin", "arm64"}, "macos", 2, true},
		{[]string{"ubuntu-latest-8-cores"}, "linux", 8, false},
		{[]string{"windows-2022-16core"}, "windows", 16, false},
		{[]string{"build-machine"}, "linux", 2, false},
		{[]string{"twin-runner"}, "linux", 2, false},
		{[]string{}, "linux", 2, false},
		{nil, "linux", 2, false},
		// extra: case-insensitivity, prefix tokens, separators
		{[]string{"Self-Hosted", "Windows2022"}, "windows", 2, true},
		{[]string{"MacOS14-arm"}, "macos", 2, false},
		{[]string{"osx", "win"}, "windows", 2, false},
		{[]string{"linux_4_core"}, "linux", 4, false},
		{[]string{"x", "32", "cores"}, "linux", 32, false}, // joined with a space
		{[]string{"123-4core"}, "linux", 4, false},
	}
	for _, c := range cases {
		got := DetectRunner(c.labels)
		if got.OS != c.os || got.Cores != c.cores || got.SelfHosted != c.self {
			t.Errorf("DetectRunner(%q) = %+v, want %s/%d/%v", c.labels, got, c.os, c.cores, c.self)
		}
	}
}

func TestLabelsMissingOrNull(t *testing.T) {
	for _, raw := range []string{`{}`, `{"labels":null}`, `{"labels":[]}`} {
		var j Job
		if err := json.Unmarshal([]byte(raw), &j); err != nil {
			t.Fatal(err)
		}
		r := DetectRunner(labelStrings(j.Labels))
		if r != (Runner{"linux", 2, false}) {
			t.Errorf("%s → %+v", raw, r)
		}
	}
}

// §6 billable minutes examples: 0→0, 1→1, 60→1, 60.5→2, 61→2, 599→10
func TestBillableMinutes(t *testing.T) {
	const s = int64(1_000_000_000)
	cases := []struct{ ns, want int64 }{
		{0, 0}, {1 * s, 1}, {60 * s, 1}, {60*s + s/2, 2}, {61 * s, 2}, {599 * s, 10}, {-5 * s, 0},
	}
	for _, c := range cases {
		if got := BillableMinutes(c.ns); got != c.want {
			t.Errorf("BillableMinutes(%d) = %d, want %d", c.ns, got, c.want)
		}
	}
}

func TestDurationNanos(t *testing.T) {
	cases := []struct {
		a, b any
		want int64
	}{
		{"2026-06-14T01:00:00Z", "2026-06-14T01:07:31Z", 451e9},
		{"2026-06-14T03:00:00+02:00", "2026-06-14T01:03:00Z", 180e9},
		{"2026-06-14T01:00:00.250Z", "2026-06-14T01:01:00.750Z", 60.5e9},
		{"2026-06-14T01:00:00-05:30", "2026-06-14T06:31:00Z", 60e9},
		{"2026-06-14T01:00:00Z", nil, 0},
		{nil, "2026-06-14T01:00:00Z", 0},
		{"garbage", "2026-06-14T01:00:00Z", 0},
		{"2026-06-14T01:05:00Z", "2026-06-14T01:00:00Z", 0}, // negative
		{"2026-06-14T01:00:00Z", 12, 0},
	}
	for _, c := range cases {
		if got := DurationNanos(c.a, c.b); got != c.want {
			t.Errorf("DurationNanos(%v,%v) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// §6 money formatting examples
func TestFmt(t *testing.T) {
	f4 := map[int64]string{562000: "$0.5620", 1234567: "$1.2346", 0: "$0.0000", 49: "$0.0000", 50: "$0.0001", 12_345_678_900: "$12345.6789"}
	for in, want := range f4 {
		if got := Fmt4(in); got != want {
			t.Errorf("Fmt4(%d) = %s, want %s", in, got, want)
		}
	}
	if Fmt4Plain(562000) != "0.5620" {
		t.Error("Fmt4Plain")
	}
	f2 := map[int64]string{500000: "$0.50", 0: "$0.00", 4999: "$0.00", 5000: "$0.01", 12_345_000: "$12.35"}
	for in, want := range f2 {
		if got := Fmt2(in); got != want {
			t.Errorf("Fmt2(%d) = %s, want %s", in, got, want)
		}
	}
	js := map[int64]string{562000: "0.562", 0: "0", 1_000_000: "1", 434000: "0.434", 1: "0.000001", 2_500_000: "2.5"}
	for in, want := range js {
		if got := MicroToJSON(in); got != want {
			t.Errorf("MicroToJSON(%d) = %s, want %s", in, got, want)
		}
	}
}

func TestDecimalToMicro(t *testing.T) {
	cases := map[string]int64{"0.006": 6000, "0.0000005": 1, "0.00000049": 0, "1e-3": 1000, "0.25": 250000, "3": 3_000_000, "0.0625": 62500}
	for in, want := range cases {
		got, ok := DecimalToMicro(in)
		if !ok || got != want {
			t.Errorf("DecimalToMicro(%s) = %d,%v want %d", in, got, ok, want)
		}
	}
}

// §7.1 examples
func TestRate(t *testing.T) {
	r := DefaultRates()
	cases := []struct {
		os    string
		cores int
		self  bool
		want  int64
	}{
		{"linux", 8, false, 22000},
		{"linux", 12, false, 22000},
		{"linux", 3, false, 6000},
		{"macos", 12, false, 62000},
		{"windows", 2, false, 10000},
		{"windows", 4, false, 22000},
		{"linux", 2, false, 6000},
		{"macos", 2, false, 62000},
		{"linux", 128, false, 162000},
		{"macos", 4, true, 0},
	}
	for _, c := range cases {
		if got := r.Rate(c.os, c.cores, c.self); got != c.want {
			t.Errorf("Rate(%s,%d,%v) = %d, want %d", c.os, c.cores, c.self, got, c.want)
		}
	}
}

// §7.2 example override
func TestRatesJSON(t *testing.T) {
	r := DefaultRates()
	ok := ApplyRatesJSON(r, `{"standard": {"linux": 0.005}, "self_hosted": 0.002, "larger": {"linux": {"4": 0.011, "96": 0.25}}, "bogus": 1}`)
	if !ok {
		t.Fatal("valid json rejected")
	}
	checks := []struct {
		os    string
		cores int
		self  bool
		want  int64
	}{
		{"linux", 2, false, 5000},
		{"linux", 4, true, 2000},
		{"linux", 4, false, 11000},
		{"linux", 8, false, 22000}, // untouched default tier
		{"linux", 100, false, 250000},
		{"windows", 2, false, 10000},
	}
	for _, c := range checks {
		if got := r.Rate(c.os, c.cores, c.self); got != c.want {
			t.Errorf("Rate(%s,%d,%v) = %d, want %d", c.os, c.cores, c.self, got, c.want)
		}
	}
	for _, bad := range []string{`{`, `[1]`, `nope`, `null`, `{"a":1} x`} {
		if ApplyRatesJSON(DefaultRates(), bad) {
			t.Errorf("invalid rates_json %q accepted", bad)
		}
	}
}

func TestParseBudget(t *testing.T) {
	cases := []struct {
		in         string
		micro      int64
		set, valid bool
	}{
		{"", 0, false, true},
		{"   ", 0, false, true},
		{"0.50", 500000, true, true},
		{" 2 ", 2_000_000, true, true},
		{"0.0000005", 1, true, true},
		{"0.00000049", 0, true, true},
		{"1.", 0, false, false},
		{".5", 0, false, false},
		{"-1", 0, false, false},
		{"abc", 0, false, false},
		{"1e3", 0, false, false},
	}
	for _, c := range cases {
		m, set, valid := ParseBudget(c.in)
		if m != c.micro || set != c.set || valid != c.valid {
			t.Errorf("ParseBudget(%q) = %d,%v,%v", c.in, m, set, valid)
		}
	}
}

func TestParseTopNAndBool(t *testing.T) {
	for in, want := range map[string]int{"": 15, "4": 4, "0": 15, "-3": 15, "x": 15, " 7 ": 7, "1": 1} {
		if got := ParseTopN(in); got != want {
			t.Errorf("ParseTopN(%q) = %d want %d", in, got, want)
		}
	}
	for in, want := range map[string]bool{"true": true, "TRUE": true, "1": true, "Yes": true, "false": false, "no": false, "on": false} {
		if got := ParseBool(in, false); got != want {
			t.Errorf("ParseBool(%q) = %v", in, got)
		}
	}
	if !ParseBool("", true) || ParseBool("", false) {
		t.Error("ParseBool default")
	}
}

func TestEscapeAndRunnerLabel(t *testing.T) {
	if got := EscapeName("a|b\r\nc"); got != `a\|b  c` {
		t.Errorf("EscapeName = %q", got)
	}
	labels := map[Runner]string{
		{"linux", 2, false}: "linux",
		{"linux", 2, true}:  "self-hosted linux",
		{"linux", 8, false}: "linux 8c",
		{"macos", 4, true}:  "self-hosted macos 4c",
	}
	for r, want := range labels {
		if got := RunnerLabel(r); got != want {
			t.Errorf("RunnerLabel(%+v) = %q want %q", r, got, want)
		}
	}
}

func TestStatusCounting(t *testing.T) {
	jobs := []Job{
		{Name: "a", Status: "completed", StartedAt: "2026-01-01T00:00:00Z", CompletedAt: "2026-01-01T00:01:00Z"},
		{Name: "b", StartedAt: "2026-01-01T00:00:00Z", CompletedAt: "2026-01-01T00:01:00Z"}, // missing status
	}
	for _, s := range []string{"queued", "in_progress", "waiting", "pending", "requested"} {
		jobs = append(jobs, Job{Name: s, Status: s})
	}
	rows, total, min, ip := Compute(jobs, DefaultRates())
	if len(rows) != 2 || ip != 5 || min != 2 || total != 12000 {
		t.Errorf("rows=%d ip=%d min=%d total=%d", len(rows), ip, min, total)
	}
}

// ---- §13 golden example (pure part) ----

const goldenJobsJSON = `{"total_count":7,"jobs":[
{"name":"build","labels":["ubuntu-latest"],"status":"completed","started_at":"2026-06-14T01:00:00Z","completed_at":"2026-06-14T01:07:31Z"},
{"name":"integration (macos)","labels":["macos-latest"],"status":"completed","started_at":"2026-06-14T01:00:00Z","completed_at":"2026-06-14T01:06:01Z"},
{"name":"lint","labels":["ubuntu-latest"],"status":"completed","started_at":"2026-06-14T01:00:00Z","completed_at":"2026-06-14T01:00:40Z"},
{"name":"deploy","labels":["self-hosted","linux","x64"],"status":"completed","started_at":"2026-06-14T01:00:00Z","completed_at":"2026-06-14T01:04:30Z"},
{"name":"big | test","labels":["ubuntu-latest-8-cores"],"status":"completed","started_at":"2026-06-14T01:00:00Z","completed_at":"2026-06-14T01:02:00Z"},
{"name":"e2e","labels":["ubuntu-latest"],"status":"in_progress","started_at":"2026-06-14T01:00:00Z","completed_at":null},
{"name":"win","labels":["windows-latest"],"status":"completed","started_at":"2026-06-14T03:00:00+02:00","completed_at":"2026-06-14T01:03:00Z"}
]}`

const goldenBody = "<!-- ci-ledger:cost-report -->\n" +
	"### 💸 CI Ledger — GitHub Actions cost\n" +
	"\n" +
	"**Run total: $0.5620** (26 billable min, 6 jobs) · [run 123](https://github.com/owner/repo/actions/runs/123)\n" +
	"\n" +
	"**Budget $0.50/run: 🔴 OVER**\n" +
	"\n" +
	"| Job | Runner | Min | Cost |\n" +
	"|---|---|--:|--:|\n" +
	"| integration (macos) | macos | 7 | $0.4340 |\n" +
	"| build | linux | 8 | $0.0480 |\n" +
	"| big \\| test | linux 8c | 2 | $0.0440 |\n" +
	"| win | windows | 3 | $0.0300 |\n" +
	"| lint | linux | 1 | $0.0060 |\n" +
	"| deploy | self-hosted linux | 5 | $0.0000 |\n" +
	"\n" +
	"_1 job(s) still in progress were not counted._\n" +
	"\n" +
	"<sub>GitHub list prices, billed per started minute, gross of included free minutes. Self-hosted shown free unless overridden. Copilot code review also consumes Actions minutes from this pool.</sub>"

func goldenReport(t *testing.T, topN int) *Report {
	t.Helper()
	var p jobsPage
	if err := decode([]byte(goldenJobsJSON), &p); err != nil {
		t.Fatal(err)
	}
	rows, total, min, ip := Compute(p.Jobs, DefaultRates())
	budget, _, _ := ParseBudget("0.50")
	return &Report{Repo: "owner/repo", RunID: "123", Server: "https://github.com", Rows: rows,
		TotalMicro: total, TotalMinutes: min, InProgress: ip, HasBudget: true, BudgetMicro: budget,
		Over: total > budget, TopN: topN}
}

func TestGoldenRender(t *testing.T) {
	if got := RenderMarkdown(goldenReport(t, 15)); got != goldenBody {
		t.Errorf("golden body mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, goldenBody)
	}
}

func TestGoldenTopN4(t *testing.T) {
	want := strings.Replace(goldenBody,
		"| lint | linux | 1 | $0.0060 |\n| deploy | self-hosted linux | 5 | $0.0000 |\n",
		"| … +2 more | | | |\n", 1)
	if got := RenderMarkdown(goldenReport(t, 4)); got != want {
		t.Errorf("top_n=4 mismatch:\n%s", got)
	}
}

func TestRenderNoBudgetNoInProgressNoJobs(t *testing.T) {
	r := &Report{Repo: "o/r", RunID: "9", Server: "https://ghe.example", TopN: 15}
	want := "<!-- ci-ledger:cost-report -->\n### 💸 CI Ledger — GitHub Actions cost\n\n" +
		"**Run total: $0.0000** (0 billable min, 0 jobs) · [run 9](https://ghe.example/o/r/actions/runs/9)\n\n" +
		"| Job | Runner | Min | Cost |\n|---|---|--:|--:|\n\n" + footer
	if got := RenderMarkdown(r); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
	r.HasBudget, r.BudgetMicro = true, 1_000_000
	if !strings.Contains(RenderMarkdown(r), "**Budget $1.00/run: 🟢 ok**\n\n| Job") {
		t.Error("ok budget line")
	}
}

func TestGoldenJSON(t *testing.T) {
	var got map[string]any
	if err := json.Unmarshal(RenderJSON(goldenReport(t, 15)), &got); err != nil {
		t.Fatal(err)
	}
	if got["total_cost_usd"] != 0.562 || got["total_minutes"] != 26.0 || got["job_count"] != 6.0 ||
		got["in_progress_jobs"] != 1.0 || got["budget_usd"] != 0.5 || got["over_budget"] != true ||
		got["run_id"] != "123" || got["repository"] != "owner/repo" {
		t.Errorf("json header: %v", got)
	}
	jobs := got["jobs"].([]any)
	first := jobs[0].(map[string]any)
	if first["name"] != "integration (macos)" || first["os"] != "macos" || first["cores"] != 2.0 ||
		first["self_hosted"] != false || first["minutes"] != 7.0 || first["rate_usd"] != 0.062 || first["cost_usd"] != 0.434 {
		t.Errorf("first job: %v", first)
	}
	if jobs[2].(map[string]any)["name"] != "big | test" {
		t.Error("raw name expected in JSON")
	}
	r := &Report{Repo: "o/r", RunID: "1"}
	if err := json.Unmarshal(RenderJSON(r), &got); err != nil || got["budget_usd"] != nil || got["over_budget"] != nil {
		t.Errorf("null budget: %v %v", err, got)
	}
}
