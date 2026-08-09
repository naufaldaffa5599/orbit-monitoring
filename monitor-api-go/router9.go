package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// 9router usage.
//
// 9router is an OpenAI-compatible router fronting many upstream providers, and
// its web dashboard keeps a request log of its own behind a JWT-cookie auth
// gate (a login POST returns a cookie that lives ~24h). The log is capped at
// 1000 entries — about five days here — so this collector pages through all of
// them once and lets the shared snapshot cache stop every dashboard poll from
// re-fetching the whole tunnel.
//
// The 9router password lives in .env, never in code or devices.json: it is a
// plain dashboard secret, not a device credential, so it stays out of the
// credential file that is read all over the place.

var router9TTL = 60 * time.Second

// router9Client is shared by every page fetch; the tunnel is a personal
// cloudflare link, so a per-request dial is just wasted time.
var router9Client = &http.Client{Timeout: 15 * time.Second}

// router9Session holds the auth cookie between refreshes. Cleared on any
// failure so the next collect re-logs-in instead of re-sending a dead cookie.
type router9Session struct {
	sync.Mutex
	cookie string
}

var router9Auth router9Session

func router9Login() (string, error) {
	router9Auth.Lock()
	defer router9Auth.Unlock()
	if router9Auth.cookie != "" {
		return router9Auth.cookie, nil
	}
	body := strings.NewReader(fmt.Sprintf(`{"password":%q}`, router9Pass))
	req, err := http.NewRequest(http.MethodPost, router9URL+"/api/auth/login", body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := router9Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("login 9router gagal: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("login 9router ditolak (%d) — cek ROUTER9_PASSWORD", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "auth_token" {
			router9Auth.cookie = c.Value
			return c.Value, nil
		}
	}
	return "", fmt.Errorf("login 9router tidak mengembalikan cookie auth")
}

// router9Clear forces a fresh login on the next collect, used after a 401.
func router9Clear() {
	router9Auth.Lock()
	router9Auth.cookie = ""
	router9Auth.Unlock()
}

// router9Req is one row of the request log; everything else the dashboard
// returns is dropped because nothing in the UI needs it.
type router9Req struct {
	Model     string `json:"model"`
	Timestamp string `json:"timestamp"`
	Status    string `json:"status"`
	Tokens    struct {
		Prompt     int `json:"prompt_tokens"`
		Completion int `json:"completion_tokens"`
	} `json:"tokens"`
}

type router9Page struct {
	Details    []router9Req `json:"details"`
	Pagination struct {
		HasNext bool `json:"hasNext"`
	} `json:"pagination"`
}

func router9FetchPage(cookie string, page int) (router9Page, error) {
	var p router9Page
	u := fmt.Sprintf("%s/api/usage/request-details?page=%d&pageSize=100", router9URL, page)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return p, err
	}
	req.Header.Set("Cookie", "auth_token="+cookie)
	resp, err := router9Client.Do(req)
	if err != nil {
		return p, fmt.Errorf("ambil log 9router gagal: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return p, errf(401, "sesi 9router kedaluwarsa")
	}
	if resp.StatusCode != http.StatusOK {
		return p, fmt.Errorf("log 9router menjawab %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return p, fmt.Errorf("jawaban 9router bukan JSON: %w", err)
	}
	return p, nil
}

// router9FetchAll walks the paginated log. The server caps pageSize at 100 and
// the log itself at 1000 entries, so this is at most ten round trips.
func router9FetchAll(cookie string) ([]router9Req, error) {
	var all []router9Req
	for page := 1; page <= 50; page++ {
		p, err := router9FetchPage(cookie, page)
		if err != nil {
			return nil, err
		}
		all = append(all, p.Details...)
		if !p.Pagination.HasNext || len(p.Details) == 0 {
			break
		}
	}
	return all, nil
}

// ── Aggregation ────────────────────────────────────────────────────────────

type Router9Day struct {
	Date       string `json:"date"`
	Requests   int    `json:"requests"`
	Prompt     int    `json:"prompt"`
	Completion int    `json:"completion"`
	Success    int    `json:"success"`
	Errors     int    `json:"errors"`
}

type Router9Model struct {
	Model      string `json:"model"`
	Requests   int    `json:"requests"`
	Prompt     int    `json:"prompt"`
	Completion int    `json:"completion"`
	Success    int    `json:"success"`
	Errors     int    `json:"errors"`
}

type Router9Usage struct {
	GeneratedAt      string         `json:"generated_at"`
	TotalRequests    int            `json:"total_requests"`
	Success          int            `json:"success"`
	Errors           int            `json:"errors"`
	PromptTokens     int            `json:"prompt_tokens"`
	CompletionTokens int            `json:"completion_tokens"`
	Days             []Router9Day   `json:"days"`
	Models           []Router9Model `json:"models"`
}

// aggregateRouter9 folds the raw log into per-day and per-model summaries. Days
// are bucketed in WIB, the timezone the rest of the dashboard speaks, so the
// chart labels line up with the clock the user actually sees.
func aggregateRouter9(reqs []router9Req) Router9Usage {
	// Non-nil slices so an empty result marshals as [] rather than null: the
	// frontend maps over these, and JSON null is not a list.
	u := Router9Usage{Days: []Router9Day{}, Models: []Router9Model{}}
	dayIdx := map[string]int{}
	modelIdx := map[string]int{}
	for _, r := range reqs {
		u.TotalRequests++
		if r.Status == "success" {
			u.Success++
		} else {
			u.Errors++
		}
		u.PromptTokens += r.Tokens.Prompt
		u.CompletionTokens += r.Tokens.Completion

		date := r.Timestamp
		if t, err := time.Parse(time.RFC3339, r.Timestamp); err == nil {
			date = t.In(wib).Format("2006-01-02")
		}
		if i, ok := dayIdx[date]; ok {
			d := &u.Days[i]
			d.Requests++
			d.Prompt += r.Tokens.Prompt
			d.Completion += r.Tokens.Completion
			if r.Status == "success" {
				d.Success++
			} else {
				d.Errors++
			}
		} else {
			dayIdx[date] = len(u.Days)
			u.Days = append(u.Days, Router9Day{
				Date: date, Requests: 1,
				Prompt: r.Tokens.Prompt, Completion: r.Tokens.Completion,
			})
			if r.Status == "success" {
				u.Days[len(u.Days)-1].Success = 1
			} else {
				u.Days[len(u.Days)-1].Errors = 1
			}
		}

		if i, ok := modelIdx[r.Model]; ok {
			m := &u.Models[i]
			m.Requests++
			m.Prompt += r.Tokens.Prompt
			m.Completion += r.Tokens.Completion
			if r.Status == "success" {
				m.Success++
			} else {
				m.Errors++
			}
		} else {
			modelIdx[r.Model] = len(u.Models)
			m := Router9Model{Model: r.Model, Requests: 1,
				Prompt: r.Tokens.Prompt, Completion: r.Tokens.Completion}
			if r.Status == "success" {
				m.Success = 1
			} else {
				m.Errors = 1
			}
			u.Models = append(u.Models, m)
		}
	}

	sort.Slice(u.Days, func(i, j int) bool { return u.Days[i].Date < u.Days[j].Date })
	sort.Slice(u.Models, func(i, j int) bool {
		if u.Models[i].Requests != u.Models[j].Requests {
			return u.Models[i].Requests > u.Models[j].Requests
		}
		return u.Models[i].Model < u.Models[j].Model
	})
	return u
}

func collectRouter9Usage() (Router9Usage, error) {
	cookie, err := router9Login()
	if err != nil {
		return Router9Usage{}, err
	}
	reqs, err := router9FetchAll(cookie)
	if err != nil {
		// The cookie carries a ~24h JWT; a 401 means it expired, so re-login
		// and give the fetch exactly one more chance before surfacing the error.
		router9Clear()
		cookie, err = router9Login()
		if err != nil {
			return Router9Usage{}, err
		}
		reqs, err = router9FetchAll(cookie)
		if err != nil {
			return Router9Usage{}, err
		}
	}
	u := aggregateRouter9(reqs)
	u.GeneratedAt = nowWIB()
	return u, nil
}

func handleRouter9Usage(w http.ResponseWriter, r *http.Request) error {
	usage, cacheErr := cached(cacheKey("router9", "usage"), router9TTL, collectRouter9Usage)
	// A login failure with no cached snapshot yields the zero value, whose nil
	// slices would marshal as null. Normalised here rather than at each error
	// return so the cache's own zero value is covered too.
	if usage.Days == nil {
		usage.Days = []Router9Day{}
	}
	if usage.Models == nil {
		usage.Models = []Router9Model{}
	}
	writeJSON(w, 200, map[string]any{"usage": usage, "error": cacheErr})
	return nil
}
