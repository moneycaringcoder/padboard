package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// Tokens page: every launch, filtered, sorted and paged server-side.

const tokensPerPage = 100

type tokenTable struct {
	Rows       []tokenRow
	Sort, Pad  string
	Query      string
	Total      int
	Page, Last int
	Pages      []int // page numbers to render
	// Sums over every row the filter selects, not just this page.
	VolUSD    float64
	Swaps     int
	Graduated int
	Label     string // "all launchpads" or the pad's name
}

var tokenSorts = map[string]func(a, b *tokenRow) bool{
	"volume": func(a, b *tokenRow) bool { return a.VolUSD > b.VolUSD },
	// Thin pools print absurd marks; rank them after anything with real activity.
	"fdv": func(a, b *tokenRow) bool {
		ra, rb := a.Swaps >= 20, b.Swaps >= 20
		if ra != rb {
			return ra
		}
		return a.FDV > b.FDV
	},
	"trades": func(a, b *tokenRow) bool { return a.Swaps > b.Swaps },
	"price": func(a, b *tokenRow) bool {
		ra, rb := a.Swaps >= 20, b.Swaps >= 20
		if ra != rb {
			return ra
		}
		return a.PriceUSD > b.PriceUSD
	},
	"new": func(a, b *tokenRow) bool { return a.Created > b.Created },
}

// queryTokens filters, sorts and pages tokens from URL parameters
// (pad, q, sort, page). Caller holds s.mu (read).
func queryTokens(s *State, q map[string][]string) tokenTable {
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	t := tokenTable{Sort: get("sort"), Pad: get("pad"), Query: strings.TrimSpace(get("q")), Page: 1}
	if tokenSorts[t.Sort] == nil {
		t.Sort = "volume"
	}
	fmt.Sscan(get("page"), &t.Page)
	needle := strings.ToLower(t.Query)
	for _, tok := range s.Tokens {
		if t.Pad != "" && tok.Platform != t.Pad {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(tok.Symbol+" "+tok.Name+" "+tok.Address), needle) {
			continue
		}
		t.Rows = append(t.Rows, newTokenRow(s, tok))
	}
	less := tokenSorts[t.Sort]
	sort.Slice(t.Rows, func(i, j int) bool { return less(&t.Rows[i], &t.Rows[j]) })
	t.Total = len(t.Rows)
	t.Label = "all launchpads"
	if p := platformByKey(t.Pad); p != nil {
		t.Label = p.Name
	}
	for i := range t.Rows {
		t.VolUSD += t.Rows[i].VolUSD
		t.Swaps += t.Rows[i].Swaps
		if t.Rows[i].Graduated != 0 {
			t.Graduated++
		}
	}
	t.Last = max(1, (t.Total+tokensPerPage-1)/tokensPerPage)
	t.Page = min(max(1, t.Page), t.Last)
	start := (t.Page - 1) * tokensPerPage
	t.Rows = t.Rows[start:min(start+tokensPerPage, t.Total)]
	for i := range t.Rows {
		t.Rows[i].Rank = start + i + 1
	}
	for p := max(1, t.Page-3); p <= min(t.Last, t.Page+3); p++ {
		t.Pages = append(t.Pages, p)
	}
	return t
}

func buildTokens(s *State, r *http.Request) pageView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pv := envelope(s, hoursByPad(s))
	pv.Title, pv.Page = "Tokens", "tokens"
	pv.Table = queryTokens(s, r.URL.Query())
	return pv
}
