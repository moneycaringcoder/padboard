package main

import (
	"encoding/json"
	"net"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Public JSON API. Free, no keys; 60 requests per minute per IP.

const apiRate = 60

type apiToken struct {
	Address     string  `json:"address"`
	Platform    string  `json:"platform"`
	Symbol      string  `json:"symbol"`
	Name        string  `json:"name"`
	Image       string  `json:"image,omitempty"`
	Quote       string  `json:"quote"`
	QuoteSymbol string  `json:"quoteSymbol"`
	PoolID      string  `json:"poolId"`
	Creator     string  `json:"creator"`
	Created     int64   `json:"created"`
	Graduated   int64   `json:"graduated,omitempty"`
	Swaps       int     `json:"swaps"`
	VolQuote    float64 `json:"volumeQuote"`
	VolUSD      float64 `json:"volumeUsd"`
	PriceUSD    float64 `json:"priceUsd"`
	Supply      float64 `json:"supply"`
	FDV         float64 `json:"fdvUsd"`
}

func toAPIToken(r tokenRow) apiToken {
	img := r.Image
	if img == "-" {
		img = ""
	}
	return apiToken{Address: r.Address, Platform: r.Platform, Symbol: r.Symbol, Name: r.Name, Image: img, Quote: r.Quote, QuoteSymbol: r.QuoteSymbol,
		PoolID: r.PoolID, Creator: r.Creator, Created: r.Created, Graduated: r.Graduated, Swaps: r.Swaps, VolQuote: r.VolQuote, VolUSD: r.VolUSD,
		PriceUSD: r.PriceUSD, Supply: r.Supply, FDV: r.FDV}
}

type apiWindow struct {
	VolumeUSD   float64 `json:"volumeUsd"`
	FeesUSD     float64 `json:"feesUsd"`
	ProtocolUSD float64 `json:"protocolRevenueUsd"`
	CreatorsUSD float64 `json:"creatorFeesUsd"`
	Trades      int     `json:"trades"`
	Launches    int     `json:"launches"`
	Graduations int     `json:"graduations"`
}

func toWindow(r rangeStats) apiWindow {
	return apiWindow{r.Volume, r.Fees, r.Protocol, r.Creators, r.Trades, r.Launches, r.Graduations}
}

type apiPlatform struct {
	Key      string               `json:"key"`
	Name     string               `json:"name"`
	Site     string               `json:"site"`
	Tokens   int                  `json:"tokensLaunched"`
	SyncedTo uint64               `json:"syncedToBlock"`
	Indexed  float64              `json:"indexedPct"` // share of the platform's history indexed; 100 when caught up
	Token    *apiPlatformToken    `json:"platformToken,omitempty"`
	Today    apiWindow            `json:"today"`
	Ranges   map[string]apiWindow `json:"ranges"` // 24h, 7d, 30d, all
}

type apiPlatformToken struct {
	Symbol    string  `json:"symbol"`
	Address   string  `json:"address"`
	PriceUSD  float64 `json:"priceUsd"`
	Change24  float64 `json:"change24hPct"`
	Supply    float64 `json:"supply"`
	Burned    float64 `json:"burned"`
	FDV       float64 `json:"fdvUsd"`
	MarketCap float64 `json:"marketCapUsd"`
}

func (c *collector) apiSummary(w http.ResponseWriter, r *http.Request) {
	s := c.state
	s.mu.RLock()
	out := struct {
		UpdatedAt int64         `json:"updatedAt"`
		Head      uint64        `json:"headBlock"`
		HeadTime  int64         `json:"headBlockTime"`
		ETHUSD    float64       `json:"ethUsd"`
		Platforms []apiPlatform `json:"platforms"`
	}{UpdatedAt: s.SyncedAt, Head: s.Head, HeadTime: s.HeadTime}
	if q := s.Quotes[ethAddr]; q != nil {
		out.ETHUSD = q.USD
	}
	byPad, now := hoursByPad(s), time.Now().UTC()
	for _, p := range platforms {
		col := buildColumn(s, p, byPad[p.Key], now, false)
		ap := apiPlatform{Key: p.Key, Name: p.Name, Site: p.Site, Tokens: col.Tokens, SyncedTo: s.Heads[p.Key], Indexed: col.Progress,
			Today: toWindow(col.Today), Ranges: map[string]apiWindow{}}
		for _, rs := range col.Ranges {
			ap.Ranges[rs.Key] = toWindow(rs)
		}
		if t := col.Token; p.Token != "" {
			ap.Token = &apiPlatformToken{t.Symbol, t.Address, t.Price, t.Change24, t.Supply, t.Burned, t.FDV, t.Mcap}
		}
		out.Platforms = append(out.Platforms, ap)
	}
	s.mu.RUnlock()
	writeJSON(w, out)
}

func (c *collector) apiDaily(w http.ResponseWriter, r *http.Request) {
	s := c.state
	s.mu.RLock()
	defer s.mu.RUnlock()
	bucket := int64(86400)
	if r.URL.Query().Get("interval") == "hour" {
		bucket = 3600
	}
	byPad := hoursByPad(s)
	type row struct {
		T    int64           `json:"t"`
		Date string          `json:"date"`
		Per  map[string]*Agg `json:"platforms"`
	}
	rows := map[int64]*row{}
	for _, p := range platforms {
		for _, d := range dailyAggs(byPad[p.Key], bucket) {
			rw := rows[d.T]
			if rw == nil {
				rw = &row{T: d.T, Date: time.Unix(d.T, 0).UTC().Format(time.RFC3339), Per: map[string]*Agg{}}
				rows[d.T] = rw
			}
			a := d.Agg
			rw.Per[p.Key] = &a
		}
	}
	out := make([]*row, 0, len(rows))
	for _, rw := range rows {
		out = append(out, rw)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T < out[j].T })
	writeJSON(w, out)
}

func (c *collector) apiTokens(w http.ResponseWriter, r *http.Request) {
	s := c.state
	s.mu.RLock()
	t := queryTokens(s, r.URL.Query())
	s.mu.RUnlock()
	rows := make([]apiToken, 0, len(t.Rows))
	for _, rw := range t.Rows {
		rows = append(rows, toAPIToken(rw))
	}
	writeJSON(w, struct {
		Total   int        `json:"total"`
		Page    int        `json:"page"`
		Pages   int        `json:"pages"`
		PerPage int        `json:"perPage"`
		Sort    string     `json:"sort"`
		Tokens  []apiToken `json:"tokens"`
	}{t.Total, t.Page, t.Last, tokensPerPage, t.Sort, rows})
}

func (c *collector) apiToken(w http.ResponseWriter, r *http.Request) {
	s := c.state
	s.mu.RLock()
	defer s.mu.RUnlock()
	tok := s.Tokens[toLower(r.PathValue("address"))]
	if tok == nil {
		http.Error(w, `{"error":"unknown token"}`, 404)
		return
	}
	row := newTokenRow(s, tok)
	writeJSON(w, toAPIToken(row))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=60")
	enc := json.NewEncoder(w)
	enc.SetIndent("", " ")
	enc.Encode(v)
}

// rateLimit is a per-IP fixed-window limiter: apiRate requests per minute.
// ponytail: in-memory, single process; move to a shared store if replicated.
type rateLimit struct {
	mu   sync.Mutex
	hits map[string]*rateHit
}

type rateHit struct {
	n     int
	reset time.Time
}

func newRateLimit() *rateLimit {
	rl := &rateLimit{hits: map[string]*rateHit{}}
	go func() {
		for range time.Tick(5 * time.Minute) {
			rl.mu.Lock()
			for k, h := range rl.hits {
				if time.Now().After(h.reset) {
					delete(rl.hits, k)
				}
			}
			rl.mu.Unlock()
		}
	}()
	return rl
}

func (rl *rateLimit) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := r.Header.Get("X-Forwarded-For")
		if ip == "" {
			ip, _, _ = net.SplitHostPort(r.RemoteAddr)
		}
		now := time.Now()
		rl.mu.Lock()
		h := rl.hits[ip]
		if h == nil || now.After(h.reset) {
			h = &rateHit{reset: now.Add(time.Minute)}
			rl.hits[ip] = h
		}
		h.n++
		n, reset := h.n, h.reset
		rl.mu.Unlock()
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(apiRate))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(max(0, apiRate-n)))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		if n > apiRate {
			w.Header().Set("Retry-After", strconv.Itoa(int(time.Until(reset).Seconds())+1))
			http.Error(w, `{"error":"rate limited: 60 requests per minute"}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// apiHeaders adds CORS and the snapshot time to every API response, errors included.
func (c *collector) apiHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.state.mu.RLock()
		synced := c.state.SyncedAt
		c.state.mu.RUnlock()
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Last-Modified", time.Unix(synced, 0).UTC().Format(http.TimeFormat))
		next.ServeHTTP(w, r)
	})
}
