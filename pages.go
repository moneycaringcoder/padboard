package main

import (
	"net/http"
	"sort"
	"strings"
	"time"
)

// Launchpad page (/p/{pad}) and token page (/t/{address}), plus the search
// endpoint behind the dock's search box.

// buildPad renders one launchpad: the same column the overview builds, its
// own stacked chart and its top tokens. Nil when the key is unknown.
func buildPad(s *State, key string) *pageView {
	p := platformByKey(key)
	if p == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	byPad := hoursByPad(s)
	pv := envelope(s, byPad)
	pv.Title, pv.Page = p.Name, "pad"
	pv.Col = column{platform: p} // the header needs the pad even before the first sync
	if pv.Empty {
		return &pv
	}
	now := time.Now().UTC()
	pv.Col = buildColumn(s, p, byPad[p.Key], now, true)
	pv.Col.TopCoins = topCoins(s, p.Key, 25)
	pv.Chart, pv.Metrics = comparison(byPad, []*platform{p}, now, "7d", "volume", false), chartMetrics
	return &pv
}

// buildToken renders one launched token with the launchpad's other top coins
// underneath. Nil when the address is unknown.
func buildToken(s *State, addr string) *pageView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tok := s.Tokens[toLower(addr)]
	if tok == nil {
		return nil
	}
	pv := envelope(s, hoursByPad(s))
	pv.Tok = newTokenRow(s, tok)
	pv.Title, pv.Page = pv.Tok.Symbol, "token"
	for _, r := range topCoins(s, tok.Platform, 11) {
		if r.Address != tok.Address && len(pv.More) < 10 {
			pv.More = append(pv.More, r)
		}
	}
	for i := range pv.More {
		pv.More[i].Rank = i + 1
	}
	return &pv
}

type searchHit struct {
	Kind    string `json:"kind"` // "pad" or "token"
	Href    string `json:"href"`
	Title   string `json:"title"`
	Sub     string `json:"sub"`
	Image   string `json:"image"`
	PadIdx  int    `json:"padIdx"`
	PadName string `json:"padName,omitempty"`
}

// search ranks launchpads by name prefix and tokens by symbol prefix, then
// name prefix, then substring anywhere (symbol, name, address); ties by volume.
// Eight hits at most. Caller holds s.mu.
func search(s *State, q string) []searchHit {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return nil
	}
	var hits []searchHit
	for _, p := range platforms {
		if strings.HasPrefix(strings.ToLower(p.Name), q) || strings.HasPrefix(p.Key, q) {
			hits = append(hits, searchHit{Kind: "pad", Href: "/p/" + p.Key, Title: p.Name, Sub: "launchpad", Image: p.Logo, PadIdx: p.Idx})
		}
	}
	type scored struct {
		t    *Token
		rank int
	}
	var toks []scored
	for _, t := range s.Tokens {
		sym, name := strings.ToLower(t.Symbol), strings.ToLower(t.Name)
		rank := 0
		switch {
		case strings.HasPrefix(sym, q):
			rank = 1
		case strings.HasPrefix(name, q):
			rank = 2
		case strings.Contains(sym, q) || strings.Contains(name, q) || strings.HasPrefix(t.Address, q):
			rank = 3
		}
		if rank > 0 {
			toks = append(toks, scored{t, rank})
		}
	}
	sort.Slice(toks, func(i, j int) bool {
		if toks[i].rank != toks[j].rank {
			return toks[i].rank < toks[j].rank
		}
		return toks[i].t.VolUSD > toks[j].t.VolUSD
	})
	for _, x := range toks {
		if len(hits) >= 8 {
			break
		}
		p := platformByKey(x.t.Platform)
		img := x.t.Image
		if img == "-" {
			img = ""
		}
		hits = append(hits, searchHit{Kind: "token", Href: "/t/" + x.t.Address, Title: x.t.Symbol, Sub: x.t.Name + " · " + fmtUSD(x.t.VolUSD) + " volume", Image: img, PadIdx: p.Idx, PadName: p.Name})
	}
	return hits
}

func (c *collector) searchHandler(w http.ResponseWriter, r *http.Request) {
	c.state.mu.RLock()
	hits := search(c.state, r.URL.Query().Get("q"))
	c.state.mu.RUnlock()
	if hits == nil {
		hits = []searchHit{}
	}
	writeJSON(w, hits)
}
