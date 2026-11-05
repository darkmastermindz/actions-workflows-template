package main

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

const footer = "<sub>GitHub list prices, billed per started minute, gross of included free minutes. Self-hosted shown free unless overridden. Copilot code review also consumes Actions minutes from this pool.</sub>"

// EscapeName makes a job name safe for a markdown table cell (§9).
func EscapeName(s string) string {
	if !strings.ContainsAny(s, "|\r\n") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '|':
			b.WriteString(`\|`)
		case '\r', '\n':
			b.WriteByte(' ')
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// RunnerLabel renders the Runner column (§9).
func RunnerLabel(r Runner) string {
	s := r.OS
	if r.SelfHosted {
		s = "self-hosted " + s
	}
	if r.Cores != 2 {
		s += " " + strconv.Itoa(r.Cores) + "c"
	}
	return s
}

// RenderMarkdown renders the report body (§9), without a trailing newline.
func RenderMarkdown(r *Report) string {
	var b strings.Builder
	shown := len(r.Rows)
	if r.TopN > 0 && shown > r.TopN {
		shown = r.TopN
	}
	b.Grow(600 + shown*64)
	b.WriteString(Marker)
	b.WriteString("\n### 💸 CI Ledger — GitHub Actions cost\n\n**Run total: ")
	b.WriteString(Fmt4(r.TotalMicro))
	b.WriteString("** (")
	b.WriteString(strconv.FormatInt(r.TotalMinutes, 10))
	b.WriteString(" billable min, ")
	b.WriteString(strconv.Itoa(len(r.Rows)))
	b.WriteString(" jobs) · [run ")
	b.WriteString(r.RunID)
	b.WriteString("](")
	b.WriteString(r.Server)
	b.WriteByte('/')
	b.WriteString(r.Repo)
	b.WriteString("/actions/runs/")
	b.WriteString(r.RunID)
	b.WriteString(")\n\n")
	if r.HasBudget {
		b.WriteString("**Budget ")
		b.WriteString(Fmt2(r.BudgetMicro))
		if r.Over {
			b.WriteString("/run: 🔴 OVER**\n\n")
		} else {
			b.WriteString("/run: 🟢 ok**\n\n")
		}
	}
	b.WriteString("| Job | Runner | Min | Cost |\n|---|---|--:|--:|\n")
	for i := 0; i < shown; i++ {
		row := &r.Rows[i]
		b.WriteString("| ")
		b.WriteString(EscapeName(row.Name))
		b.WriteString(" | ")
		b.WriteString(RunnerLabel(row.Runner))
		b.WriteString(" | ")
		b.WriteString(strconv.FormatInt(row.Minutes, 10))
		b.WriteString(" | ")
		b.WriteString(Fmt4(row.CostMicro))
		b.WriteString(" |\n")
	}
	if rest := len(r.Rows) - shown; rest > 0 {
		b.WriteString("| … +")
		b.WriteString(strconv.Itoa(rest))
		b.WriteString(" more | | | |\n")
	}
	b.WriteByte('\n')
	if r.InProgress > 0 {
		b.WriteByte('_')
		b.WriteString(strconv.Itoa(r.InProgress))
		b.WriteString(" job(s) still in progress were not counted._\n\n")
	}
	b.WriteString(footer)
	return b.String()
}

// RenderJSON renders the machine-readable report (§12).
func RenderJSON(r *Report) []byte {
	b := make([]byte, 0, 256+len(r.Rows)*160)
	b = append(b, "{\n  \"repository\": "...)
	b = appendJSONString(b, r.Repo)
	b = append(b, ",\n  \"run_id\": "...)
	b = appendJSONString(b, r.RunID)
	b = append(b, ",\n  \"total_cost_usd\": "...)
	b = append(b, MicroToJSON(r.TotalMicro)...)
	b = append(b, ",\n  \"total_minutes\": "...)
	b = strconv.AppendInt(b, r.TotalMinutes, 10)
	b = append(b, ",\n  \"job_count\": "...)
	b = strconv.AppendInt(b, int64(len(r.Rows)), 10)
	b = append(b, ",\n  \"in_progress_jobs\": "...)
	b = strconv.AppendInt(b, int64(r.InProgress), 10)
	if r.HasBudget {
		b = append(b, ",\n  \"budget_usd\": "...)
		b = append(b, MicroToJSON(r.BudgetMicro)...)
		b = append(b, ",\n  \"over_budget\": "...)
		b = strconv.AppendBool(b, r.Over)
	} else {
		b = append(b, ",\n  \"budget_usd\": null,\n  \"over_budget\": null"...)
	}
	b = append(b, ",\n  \"jobs\": ["...)
	for i := range r.Rows {
		row := &r.Rows[i]
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, "\n    {\"name\": "...)
		b = appendJSONString(b, row.Name)
		b = append(b, ", \"os\": "...)
		b = appendJSONString(b, row.Runner.OS)
		b = append(b, ", \"cores\": "...)
		b = strconv.AppendInt(b, int64(row.Runner.Cores), 10)
		b = append(b, ", \"self_hosted\": "...)
		b = strconv.AppendBool(b, row.Runner.SelfHosted)
		b = append(b, ", \"minutes\": "...)
		b = strconv.AppendInt(b, row.Minutes, 10)
		b = append(b, ", \"rate_usd\": "...)
		b = append(b, MicroToJSON(row.RateMicro)...)
		b = append(b, ", \"cost_usd\": "...)
		b = append(b, MicroToJSON(row.CostMicro)...)
		b = append(b, '}')
	}
	if len(r.Rows) > 0 {
		b = append(b, "\n  "...)
	}
	b = append(b, "]\n}\n"...)
	return b
}

const hexDigits = "0123456789abcdef"

// appendJSONString appends s as a JSON string literal. Valid UTF-8 is passed
// through; invalid bytes become U+FFFD so the file stays valid JSON.
func appendJSONString(b []byte, s string) []byte {
	b = append(b, '"')
	for _, r := range s {
		switch {
		case r == '"':
			b = append(b, '\\', '"')
		case r == '\\':
			b = append(b, '\\', '\\')
		case r == '\n':
			b = append(b, '\\', 'n')
		case r == '\r':
			b = append(b, '\\', 'r')
		case r == '\t':
			b = append(b, '\\', 't')
		case r < 0x20:
			b = append(b, '\\', 'u', '0', '0', hexDigits[r>>4], hexDigits[r&0xf])
		default:
			b = utf8.AppendRune(b, r)
		}
	}
	return append(b, '"')
}
