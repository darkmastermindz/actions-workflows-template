package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// sleep is swapped out in tests to make retries instant.
var sleep = time.Sleep

var retryDelays = [...]time.Duration{1 * time.Second, 2 * time.Second}

// apiError describes a failed request: Status == 0 means a network error.
type apiError struct{ Status int }

func (e *apiError) Error() string { return e.Describe() }

// Describe renders "HTTP 404" or "network error" for the warning lines.
func (e *apiError) Describe() string {
	if e.Status == 0 {
		return "network error"
	}
	return "HTTP " + strconv.Itoa(e.Status)
}

type client struct {
	api   string // no trailing slash
	repo  string
	auth  string // "Bearer <token>"
	http  *http.Client
	debug io.Writer
}

func newClient(api, repo, token string, debug io.Writer) *client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = 16
	return &client{
		api:   api,
		repo:  repo,
		auth:  "Bearer " + token,
		http:  &http.Client{Transport: tr, Timeout: 60 * time.Second},
		debug: debug,
	}
}

// do performs one request with retries (§11) and returns the 2xx body.
func (c *client) do(method, url string, body []byte) ([]byte, error) {
	status := 0
	for attempt := 0; ; attempt++ {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequest(method, url, rd)
		if err != nil {
			return nil, &apiError{0}
		}
		req.Header.Set("Authorization", c.auth)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		req.Header.Set("User-Agent", "ci-ledger")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.http.Do(req)
		var data []byte
		if err == nil {
			data, err = io.ReadAll(resp.Body)
			resp.Body.Close()
		}
		if err != nil {
			status = 0
			if c.debug != nil {
				io.WriteString(c.debug, "ci-ledger: "+method+" request failed: "+err.Error()+"\n")
			}
		} else {
			status = resp.StatusCode
			if status >= 200 && status < 300 {
				return data, nil
			}
			if status != 429 && status < 500 {
				return nil, &apiError{status}
			}
		}
		if attempt >= len(retryDelays) {
			return nil, &apiError{status}
		}
		sleep(retryDelays[attempt])
	}
}

type jobsPage struct {
	TotalCount any   `json:"total_count"`
	Jobs       []Job `json:"jobs"`
}

func decode(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec.Decode(v)
}

func (c *client) jobsURL(runID string, page int) string {
	return c.api + "/repos/" + c.repo + "/actions/runs/" + runID + "/jobs?per_page=100&page=" + strconv.Itoa(page)
}

func (c *client) fetchJobsPage(runID string, page int) ([]Job, any, error) {
	data, err := c.do("GET", c.jobsURL(runID, page), nil)
	if err != nil {
		return nil, nil, err
	}
	var p jobsPage
	if err := decode(data, &p); err != nil {
		return nil, nil, &apiError{200}
	}
	return p.Jobs, p.TotalCount, nil
}

const maxPages = 100000 // safety net against a server that never returns a short page

// FetchJobs collects all jobs (§11): pages 1,2,… until a page has < 100 jobs.
// When page 1 reports total_count, the remaining expected pages are fetched
// concurrently; results are still consumed strictly in page order, so the
// outcome is identical to the sequential algorithm.
func (c *client) FetchJobs(runID string) ([]Job, error) {
	first, total, err := c.fetchJobsPage(runID, 1)
	if err != nil {
		return nil, err
	}
	all := first
	if len(first) < 100 {
		return all, nil
	}
	next := 2
	if n, ok := total.(json.Number); ok {
		if t, err := strconv.ParseInt(n.String(), 10, 64); err == nil && t > 100 {
			last := int((t + 99) / 100)
			if last > maxPages {
				last = maxPages
			}
			type result struct {
				jobs []Job
				err  error
			}
			count := last - 1
			results := make([]result, count)
			var wg sync.WaitGroup
			sem := make(chan struct{}, 8)
			for i := 0; i < count; i++ {
				wg.Add(1)
				sem <- struct{}{}
				go func(i int) {
					defer wg.Done()
					defer func() { <-sem }()
					j, _, e := c.fetchJobsPage(runID, i+2)
					results[i] = result{j, e}
				}(i)
			}
			wg.Wait()
			for i := range results {
				if results[i].err != nil {
					return nil, results[i].err
				}
				all = append(all, results[i].jobs...)
				if len(results[i].jobs) < 100 {
					return all, nil
				}
			}
			next = last + 1
		}
	}
	for page := next; page <= maxPages; page++ {
		jobs, _, err := c.fetchJobsPage(runID, page)
		if err != nil {
			return nil, err
		}
		all = append(all, jobs...)
		if len(jobs) < 100 {
			break
		}
	}
	return all, nil
}

type comment struct {
	ID   any `json:"id"`
	Body any `json:"body"`
}

// FindMarkerComment returns the id of the first comment containing Marker
// ("" if none). Paging stops early once found (the first match is the same).
func (c *client) FindMarkerComment(pr string) (string, error) {
	base := c.api + "/repos/" + c.repo + "/issues/" + pr + "/comments?per_page=100&page="
	for page := 1; page <= maxPages; page++ {
		data, err := c.do("GET", base+strconv.Itoa(page), nil)
		if err != nil {
			return "", err
		}
		var list []comment
		if err := decode(data, &list); err != nil {
			return "", &apiError{200}
		}
		for _, cm := range list {
			if s, ok := cm.Body.(string); ok && strings.Contains(s, Marker) {
				return idString(cm.ID), nil
			}
		}
		if len(list) < 100 {
			break
		}
	}
	return "", nil
}

func idString(v any) string {
	switch x := v.(type) {
	case json.Number:
		return x.String()
	case string:
		return x
	}
	return ""
}

// UpsertComment creates or updates the marker comment. Returns "created"/"updated".
func (c *client) UpsertComment(pr, report string) (string, error) {
	id, err := c.FindMarkerComment(pr)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]string{"body": report})
	body := bytes.TrimRight(buf.Bytes(), "\n")
	if id != "" {
		if _, err := c.do("PATCH", c.api+"/repos/"+c.repo+"/issues/comments/"+id, body); err != nil {
			return "", err
		}
		return "updated", nil
	}
	if _, err := c.do("POST", c.api+"/repos/"+c.repo+"/issues/"+pr+"/comments", body); err != nil {
		return "", err
	}
	return "created", nil
}
