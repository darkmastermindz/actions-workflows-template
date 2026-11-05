package main

// Pure functions: runner detection, billable minutes, rates, money formatting,
// cost computation. No I/O here (spec NF5).

import (
	"encoding/json"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Marker identifies the CI Ledger PR comment.
const Marker = "<!-- ci-ledger:cost-report -->"

// Runner describes the machine a job ran on (§5).
type Runner struct {
	OS         string
	Cores      int
	SelfHosted bool
}

// asciiLower lower-cases ASCII letters only (portable across implementations).
func asciiLower(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if b[j] >= 'A' && b[j] <= 'Z' {
					b[j] += 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}

func isAlnum(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// DetectRunner implements §5 on the job's labels.
func DetectRunner(labels []string) Runner {
	r := Runner{OS: "linux", Cores: 2}
	win, mac := false, false
	lower := make([]string, len(labels))
	for i, l := range labels {
		l = asciiLower(l)
		lower[i] = l
		if l == "self-hosted" {
			r.SelfHosted = true
		}
		// tokenize on runs of characters outside [a-z0-9]
		for j := 0; j < len(l); {
			if !isAlnum(l[j]) {
				j++
				continue
			}
			k := j
			for k < len(l) && isAlnum(l[k]) {
				k++
			}
			tok := l[j:k]
			j = k
			switch {
			case tok == "windows" || tok == "win" || tok == "win32" || tok == "win64" || strings.HasPrefix(tok, "windows"):
				win = true
			case tok == "macos" || tok == "mac" || tok == "osx" || tok == "darwin" || strings.HasPrefix(tok, "macos"):
				mac = true
			}
		}
	}
	if win {
		r.OS = "windows"
	} else if mac {
		r.OS = "macos"
	}
	if c, ok := matchCores(strings.Join(lower, " ")); ok {
		r.Cores = c
	}
	return r
}

// matchCores finds the first match of /([0-9]+)[-_ ]?cores?/ in s.
// Backtracking into a digit run can never succeed (the following char would be
// a digit), so it suffices to test each maximal digit run.
func matchCores(s string) (int, bool) {
	for i := 0; i < len(s); {
		if !isDigit(s[i]) {
			i++
			continue
		}
		j := i
		for j < len(s) && isDigit(s[j]) {
			j++
		}
		k := j
		if k < len(s) && (s[k] == '-' || s[k] == '_' || s[k] == ' ') {
			k++
		}
		if strings.HasPrefix(s[k:], "core") {
			n, err := strconv.Atoi(s[i:j])
			if err != nil {
				n = math.MaxInt32 // absurdly large core count; still "larger than any tier"
			}
			return n, true
		}
		i = j
	}
	return 0, false
}

// ParseTime parses an RFC 3339 timestamp (Z or ±hh:mm, optional fractions).
func ParseTime(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// DurationNanos returns completed-started in ns, or 0 when missing/invalid/negative (§6).
func DurationNanos(started, completed any) int64 {
	s, ok1 := started.(string)
	c, ok2 := completed.(string)
	if !ok1 || !ok2 {
		return 0
	}
	ts, ok1 := ParseTime(s)
	tc, ok2 := ParseTime(c)
	if !ok1 || !ok2 {
		return 0
	}
	d := tc.Sub(ts)
	if d <= 0 {
		return 0
	}
	return int64(d)
}

const nsPerMinute = int64(60 * time.Second)

// BillableMinutes = ceil(seconds/60), 0 when seconds == 0 (§6). Input in ns.
func BillableMinutes(ns int64) int64 {
	if ns <= 0 {
		return 0
	}
	return (ns-1)/nsPerMinute + 1
}

// ---- money -------------------------------------------------------------

var (
	ratMillion = new(big.Rat).SetInt64(1_000_000)
	ratHalf    = big.NewRat(1, 2)
)

// DecimalToMicro converts an exact decimal string (optionally with sign and
// exponent, as JSON numbers allow) to micro-units, rounding half-up.
func DecimalToMicro(s string) (int64, bool) {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return 0, false
	}
	r.Mul(r, ratMillion)
	neg := r.Sign() < 0
	if neg {
		r.Neg(r)
	}
	r.Add(r, ratHalf)
	q := new(big.Int).Quo(r.Num(), r.Denom()) // floor for non-negative
	var v int64
	if q.IsInt64() {
		v = q.Int64()
	} else {
		v = math.MaxInt64
	}
	if neg {
		v = -v
	}
	return v, true
}

func pad(n int64, width int) string {
	s := strconv.FormatInt(n, 10)
	for len(s) < width {
		s = "0" + s
	}
	return s
}

// Fmt4Plain formats micro-USD with 4 decimals, half-up, without "$".
func Fmt4Plain(micro int64) string {
	sign := ""
	if micro < 0 {
		sign, micro = "-", -micro
	}
	u := (micro + 50) / 100
	return sign + strconv.FormatInt(u/10000, 10) + "." + pad(u%10000, 4)
}

// Fmt4 formats micro-USD as "$d.dddd".
func Fmt4(micro int64) string {
	s := Fmt4Plain(micro)
	if s[0] == '-' {
		return "-$" + s[1:]
	}
	return "$" + s
}

// Fmt2 formats micro-USD as "$d.dd" (half-up).
func Fmt2(micro int64) string {
	sign := ""
	if micro < 0 {
		sign, micro = "-", -micro
	}
	u := (micro + 5000) / 10000
	return sign + "$" + strconv.FormatInt(u/100, 10) + "." + pad(u%100, 2)
}

// MicroToJSON renders micro/10^6 as a minimal exact JSON number.
func MicroToJSON(micro int64) string {
	sign := ""
	if micro < 0 {
		sign, micro = "-", -micro
	}
	ip := strconv.FormatInt(micro/1_000_000, 10)
	fp := micro % 1_000_000
	if fp == 0 {
		return sign + ip
	}
	f := strings.TrimRight(pad(fp, 6), "0")
	return sign + ip + "." + f
}

// ---- rates (§7) ---------------------------------------------------------

// Rates holds the rate table in micro-USD per minute.
type Rates struct {
	Standard   map[string]int64
	SelfHosted int64
	Larger     map[string]map[int]int64
}

// DefaultRates returns the built-in list prices (§7.1).
func DefaultRates() *Rates {
	return &Rates{
		Standard:   map[string]int64{"linux": 6000, "windows": 10000, "macos": 62000},
		SelfHosted: 0,
		Larger: map[string]map[int]int64{
			"linux":   {4: 12000, 8: 22000, 16: 42000, 32: 82000, 64: 162000},
			"windows": {2: 10000, 4: 22000, 8: 42000, 16: 82000, 32: 162000, 64: 322000},
		},
	}
}

// Rate implements rate(os, cores, self_hosted) in micro-USD.
func (r *Rates) Rate(os string, cores int, selfHosted bool) int64 {
	if selfHosted {
		return r.SelfHosted
	}
	if cores > 2 {
		if tiers := r.Larger[os]; len(tiers) > 0 {
			if v, ok := tiers[cores]; ok {
				return v
			}
			best, found := 0, false
			for c := range tiers {
				if c <= cores && (!found || c > best) {
					best, found = c, true
				}
			}
			if found {
				return tiers[best]
			}
		}
	}
	return r.Standard[os]
}

func numMicro(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	return DecimalToMicro(n.String())
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) {
			return false
		}
	}
	return true
}

// ApplyRatesJSON deep-merges a JSON override (§7.2) over r. Returns false
// when the JSON is invalid (r is left untouched in that case).
func ApplyRatesJSON(r *Rates, raw string) bool {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil || doc == nil {
		return false
	}
	if dec.More() { // trailing garbage
		return false
	}
	if std, ok := doc["standard"].(map[string]any); ok {
		for os, v := range std {
			if m, ok := numMicro(v); ok {
				r.Standard[os] = m
			}
		}
	}
	if m, ok := numMicro(doc["self_hosted"]); ok {
		r.SelfHosted = m
	}
	if larger, ok := doc["larger"].(map[string]any); ok {
		for os, tv := range larger {
			tiers, ok := tv.(map[string]any)
			if !ok {
				continue
			}
			for k, v := range tiers {
				if !allDigits(k) {
					continue
				}
				c, err := strconv.Atoi(k)
				if err != nil {
					continue
				}
				m, ok := numMicro(v)
				if !ok {
					continue
				}
				if r.Larger[os] == nil {
					r.Larger[os] = map[int]int64{}
				}
				r.Larger[os][c] = m
			}
		}
	}
	return true
}

// ---- budget (§8) --------------------------------------------------------

// ParseBudget validates ^[0-9]+(\.[0-9]+)?$ on the trimmed value.
// Returns (micro, set, valid).
func ParseBudget(raw string) (int64, bool, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false, true
	}
	ip, fp, hasDot := strings.Cut(s, ".")
	if !allDigits(ip) || (hasDot && !allDigits(fp)) {
		return 0, false, false
	}
	m, ok := DecimalToMicro(s)
	if !ok {
		return 0, false, false
	}
	return m, true, true
}

// ---- jobs and report model ---------------------------------------------

// Job is the subset of a GitHub job object CI Ledger needs.
type Job struct {
	Name        any `json:"name"`
	Labels      any `json:"labels"`
	Status      any `json:"status"`
	StartedAt   any `json:"started_at"`
	CompletedAt any `json:"completed_at"`
}

// Row is one counted job.
type Row struct {
	Name      string
	Runner    Runner
	Minutes   int64
	RateMicro int64
	CostMicro int64
}

// Report is the computed run summary.
type Report struct {
	Repo, RunID, Server string
	Rows                []Row
	TotalMicro          int64
	TotalMinutes        int64
	InProgress          int
	HasBudget           bool
	BudgetMicro         int64
	Over                bool
	TopN                int
}

func labelStrings(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// Compute bills the jobs and builds the report model (§6, §8).
func Compute(jobs []Job, rates *Rates) (rows []Row, totalMicro, totalMinutes int64, inProgress int) {
	rows = make([]Row, 0, len(jobs))
	for i := range jobs {
		j := &jobs[i]
		if j.Status != nil {
			if s, ok := j.Status.(string); !ok || s != "completed" {
				inProgress++
				continue
			}
		}
		name, ok := j.Name.(string)
		if !ok {
			name = "?"
		}
		rn := DetectRunner(labelStrings(j.Labels))
		min := BillableMinutes(DurationNanos(j.StartedAt, j.CompletedAt))
		rate := rates.Rate(rn.OS, rn.Cores, rn.SelfHosted)
		cost := min * rate
		rows = append(rows, Row{Name: name, Runner: rn, Minutes: min, RateMicro: rate, CostMicro: cost})
		totalMicro += cost
		totalMinutes += min
	}
	sort.SliceStable(rows, func(a, b int) bool {
		if rows[a].CostMicro != rows[b].CostMicro {
			return rows[a].CostMicro > rows[b].CostMicro
		}
		return rows[a].Minutes > rows[b].Minutes
	})
	return
}

// ParseTopN returns the row limit (invalid / < 1 → 15).
func ParseTopN(raw string) int {
	s := strings.TrimSpace(raw)
	if !allDigits(s) {
		return 15
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		if len(strings.TrimLeft(s, "0")) > 0 {
			return math.MaxInt32 // huge but valid: show everything
		}
		return 15
	}
	if n < 1 {
		return 15
	}
	return n
}

// ParseBool: case-insensitive true/1/yes → true; empty → def.
func ParseBool(raw string, def bool) bool {
	s := strings.TrimSpace(raw)
	if s == "" {
		return def
	}
	switch asciiLower(s) {
	case "true", "1", "yes":
		return true
	}
	return false
}
