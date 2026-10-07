package main

import (
	"fmt"
	"html/template"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

// dailyAggs buckets a platform's hourly aggregates into `bucket`-second bins
// (86400 for days, 3600 for hours), sorted ascending. Caller holds s.mu.
func dailyAggs(s *State, p *platform, hours []int64, bucket int64) []dayAgg {
	var out []dayAgg
	idx := map[int64]int{}
	for _, h := range hours {
		a := s.Hours[hourKey(p.Key, h)]
		if a == nil {
			continue
		}
		d := h - h%bucket
		i, ok := idx[d]
		if !ok {
			i = len(out)
			idx[d] = i
			out = append(out, dayAgg{T: d})
		}
		out[i].Agg.add(a)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].T < out[b].T })
	return out
}

// ---- Charts page: both platforms overlaid in every chart ----

type wallChart struct {
	Title, Sub string
	Icon       string   // optional image for the title
	Totals     []string // one per platform, same order as `platforms`
	SVG        template.HTML
	Wide       bool
}

func buildCharts(s *State) pageView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pv := envelope(s)
	pv.Title, pv.Page = "Charts", "charts"
	tokenImg := make([]string, len(platforms))
	for i, p := range platforms {
		pv.Columns = append(pv.Columns, column{platform: p, Idx: i + 1})
		if t := s.Tokens[p.Token]; t != nil && t.Image != "-" {
			tokenImg[i] = t.Image
		}
	}
	hours := s.hours()
	if len(hours) == 0 {
		pv.Empty = true
		return pv
	}
	now := time.Now().Unix()

	// Align both platforms on one shared day axis (and one hour axis for 7d).
	type axis struct {
		keys   []int64
		labels []string
		per    [][]Agg // [platform][bucket]
	}
	build := func(bucket, from int64, layout string) axis {
		seen := map[int64]bool{}
		perPlat := make([]map[int64]Agg, len(platforms))
		for i, p := range platforms {
			perPlat[i] = map[int64]Agg{}
			for _, d := range dailyAggs(s, p, hours, bucket) {
				if d.T < from {
					continue
				}
				perPlat[i][d.T] = d.Agg
				seen[d.T] = true
			}
		}
		var ax axis
		for k := range seen {
			ax.keys = append(ax.keys, k)
		}
		sort.Slice(ax.keys, func(a, b int) bool { return ax.keys[a] < ax.keys[b] })
		ax.per = make([][]Agg, len(platforms))
		for i := range platforms {
			ax.per[i] = make([]Agg, len(ax.keys))
			for j, k := range ax.keys {
				ax.per[i][j] = perPlat[i][k]
			}
		}
		for _, k := range ax.keys {
			ax.labels = append(ax.labels, time.Unix(k, 0).UTC().Format(layout))
		}
		return ax
	}
	days := build(86400, 0, "Jan 2")
	hrs := build(3600, now-7*86400, "Jan 2 15h")

	pick := func(ax axis, f func(Agg) float64) [][]float64 {
		out := make([][]float64, len(platforms))
		for i := range platforms {
			out[i] = make([]float64, len(ax.keys))
			for j := range ax.keys {
				out[i][j] = f(ax.per[i][j])
			}
		}
		return out
	}
	sumEach := func(series [][]float64, f func(float64) string) []string {
		out := make([]string, len(series))
		for i, s := range series {
			t := 0.0
			for _, v := range s {
				t += v
			}
			out[i] = f(t)
		}
		return out
	}
	lastEach := func(series [][]float64, f func(float64) string) []string {
		out := make([]string, len(series))
		for i, s := range series {
			last := 0.0
			for _, v := range s {
				if v != 0 {
					last = v
				}
			}
			out[i] = f(last)
		}
		return out
	}
	vol := pick(days, func(a Agg) float64 { return a.VolUSD })
	fee := pick(days, fees)
	lau := pick(days, func(a Agg) float64 { return float64(a.Launches) })
	trd := pick(days, func(a Agg) float64 { return float64(a.Swaps) })
	rev := pick(days, func(a Agg) float64 { return a.PlatformUSD + a.LaunchFees })
	hv := pick(hrs, func(a Agg) float64 { return a.VolUSD })
	prices := pick(days, func(a Agg) float64 { return a.TokenPrice })

	// Platform-token FDV and FDV ÷ trailing-7-day fees.
	fdv := make([][]float64, len(platforms))
	ratio := make([][]float64, len(platforms))
	for i, p := range platforms {
		supply := 0.0
		if t := s.Tokens[p.Token]; t != nil {
			supply = t.Supply
		}
		fdv[i] = make([]float64, len(days.keys))
		ratio[i] = make([]float64, len(days.keys))
		last := 0.0
		for j := range days.keys {
			if prices[i][j] != 0 {
				last = prices[i][j]
			}
			fdv[i][j] = last * supply
			if j < 6 {
				continue
			}
			f := 0.0
			for k := j - 6; k <= j; k++ {
				f += fee[i][k]
			}
			if f > 0 {
				ratio[i][j] = fdv[i][j] / f
			}
		}
	}

	count := func(v float64) string { return fmtK(v) }
	n, w := narrowTileW, wideW
	pv.Charts = []wallChart{
		{Title: "Volume", Sub: "daily, USD", Totals: sumEach(vol, fmtUSD), SVG: dualBars(vol, days.labels, fmtUSD, w), Wide: true},
		{Title: "Hourly volume", Sub: "last 7 days", Totals: sumEach(hv, fmtUSD), SVG: dualBars(hv, hrs.labels, fmtUSD, n)},
		{Title: "Volume dominance", Sub: "share of daily volume", Totals: shareTotals(vol), SVG: band(vol[0], vol[1], days.labels, w), Wide: true},
		{Title: "Launches", Sub: "daily", Totals: sumEach(lau, count), SVG: dualBars(lau, days.labels, count, n)},
		{Title: "Trades", Sub: "daily swaps", Totals: sumEach(trd, count), SVG: dualBars(trd, days.labels, count, n)},
		{Title: "Fees", Sub: "daily, all destinations", Totals: sumEach(fee, fmtUSD), SVG: dualBars(fee, days.labels, fmtUSD, n)},
		{Title: "Protocol revenue", Sub: "platform share + launch fees", Totals: sumEach(rev, fmtUSD), SVG: dualBars(rev, days.labels, fmtUSD, n)},
		{Title: "Fee dominance", Sub: "share of daily fees", Totals: shareTotals(fee), SVG: band(fee[0], fee[1], days.labels, n)},
		{Title: "Launch dominance", Sub: "share of daily launches", Totals: shareTotals(lau), SVG: band(lau[0], lau[1], days.labels, n)},
		{Title: "Platform token FDV", Sub: "$" + platforms[0].TokenSymbol + " vs $" + platforms[1].TokenSymbol, Totals: lastEach(fdv, fmtUSD), SVG: dualLines(fdv, days.labels, fmtUSD, n)},
		{Title: "FDV ÷ 7d fees", Sub: "valuation per dollar of weekly fees", Totals: lastEach(ratio, fmtRatio), SVG: dualLines(ratio, days.labels, fmtRatio, n)},
		{Title: "$" + platforms[0].TokenSymbol + " price", Sub: "daily last swap", Icon: tokenImg[0], Totals: []string{lastEach(prices, fmtPrice)[0], ""}, SVG: dualLines([][]float64{prices[0], nil}, days.labels, fmtPrice, n)},
		{Title: "$" + platforms[1].TokenSymbol + " price", Sub: "daily last swap", Icon: tokenImg[1], Totals: []string{"", lastEach(prices, fmtPrice)[1]}, SVG: dualLines([][]float64{nil, prices[1]}, days.labels, fmtPrice, n)},
	}
	return pv
}

func shareTotals(series [][]float64) []string {
	t := make([]float64, len(series))
	all := 0.0
	for i, s := range series {
		for _, v := range s {
			t[i] += v
		}
		all += t[i]
	}
	out := make([]string, len(series))
	for i := range series {
		if all > 0 {
			out[i] = fmtPct(t[i] / all * 100)
		}
	}
	return out
}

// ---- overlay chart renderers ----

// Wide tiles span two grid cells and render at 600 units; narrow tiles use
// 300 so both end up the same pixel height in a row.
const (
	wideW, narrowTileW = 600.0, 300.0
	oh                 = 220.0
	oPadR, oPadB       = 56.0, 22.0
	oPadT              = 8.0
)

func overlayAxes(b *strings.Builder, ow, maxV float64, fy func(float64) string) {
	plotH := oh - oPadT - oPadB
	for _, f := range []float64{1.0 / 3, 2.0 / 3, 1} {
		y := oPadT + plotH*(1-f)
		fmt.Fprintf(b, `<line x1="0" y1="%.1f" x2="%g" y2="%.1f" class="grid"/><text x="%g" y="%.1f" class="tick" text-anchor="end">%s</text>`, y, ow-oPadR, y, ow, y+4, fy(maxV*f))
	}
}

func overlayXLabels(b *strings.Builder, labels []string, xAt func(int) float64) {
	n := len(labels)
	if n == 0 {
		return
	}
	for _, j := range []int{0, n / 2, n - 1} {
		anchor := "start"
		if j == n-1 {
			anchor = "end"
		} else if j == n/2 {
			anchor = "middle"
		}
		fmt.Fprintf(b, `<text x="%.1f" y="%g" class="tick" text-anchor="%s">%s</text>`, xAt(j), oh-6, anchor, labels[j])
	}
}

func seriesMax(series [][]float64) float64 {
	m := 0.0
	for _, s := range series {
		for _, v := range s {
			if !math.IsNaN(v) && !math.IsInf(v, 0) {
				m = math.Max(m, v)
			}
		}
	}
	if m == 0 {
		return 1
	}
	return m
}

// dualBars draws side-by-side bars per bucket, one per platform.
func dualBars(series [][]float64, labels []string, fy func(float64) string, ow float64) template.HTML {
	n := len(labels)
	if n == 0 {
		return ""
	}
	maxV := seriesMax(series)
	var b strings.Builder
	d := newDither()
	fmt.Fprintf(&b, `<svg viewBox="0 0 %g %g" class="bars" role="img">%s`, ow, oh, d.defs())
	overlayAxes(&b, ow, maxV, fy)
	plotW, plotH := ow-oPadR, oh-oPadT-oPadB
	group := plotW / float64(n)
	bw := math.Max(0.8, group*0.8/float64(len(series)))
	for j := range n {
		for i, s := range series {
			if j >= len(s) {
				continue
			}
			bh := plotH * s[j] / maxV
			x := float64(j)*group + group*0.1 + bw*float64(i)
			d.bar(&b, x, oPadT+plotH-bh, bw, bh, i+1, fmt.Sprintf("%s · %s: %s", labels[j], platforms[i].Name, fy(s[j])), "")
		}
	}
	overlayXLabels(&b, labels, func(j int) float64 { return float64(j)*group + group/2 })
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// dualLines draws one line + soft area per platform; zero values are gaps.
func dualLines(series [][]float64, labels []string, fy func(float64) string, ow float64) template.HTML {
	n := len(labels)
	if n < 2 {
		return ""
	}
	maxV := seriesMax(series)
	var b strings.Builder
	d := newDither()
	fmt.Fprintf(&b, `<svg viewBox="0 0 %g %g" class="bars lines" role="img">%s`, ow, oh, d.defs())
	overlayAxes(&b, ow, maxV, fy)
	plotW, plotH := ow-oPadR, oh-oPadT-oPadB
	xAt := func(j int) float64 { return plotW * float64(j) / float64(n-1) }
	yAt := func(v float64) float64 { return oPadT + plotH*(1-v/maxV) }
	for i, s := range series {
		var path strings.Builder
		first, last := -1, -1
		for j, v := range s {
			if v == 0 {
				continue
			}
			cmd := "L"
			if first < 0 {
				cmd, first = "M", j
			}
			last = j
			fmt.Fprintf(&path, "%s%.1f %.1f ", cmd, xAt(j), yAt(v))
		}
		if first < 0 {
			continue
		}
		fmt.Fprintf(&b, `<path d="%sL%.1f %.1f L%.1f %.1f Z" class="area s%d" %s/>`, path.String(), xAt(last), oPadT+plotH, xAt(first), oPadT+plotH, i+1, d.fill(i+1))
		fmt.Fprintf(&b, `<path d="%s" class="line glowline s%d" %s/><path d="%s" class="line s%d"/>`, path.String(), i+1, d.glow(), path.String(), i+1)
		for j, v := range s {
			if v != 0 {
				fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="5" class="pt s%d" data-tip="%s · %s: %s"/>`, xAt(j), yAt(v), i+1, labels[j], platforms[i].Name, fy(v))
			}
		}
	}
	overlayXLabels(&b, labels, xAt)
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// band is a 100%-stacked share chart: platform 1 from the bottom, 2 on top.
func band(a, c []float64, labels []string, ow float64) template.HTML {
	n := len(labels)
	if n == 0 {
		return ""
	}
	var b strings.Builder
	d := newDither()
	fmt.Fprintf(&b, `<svg viewBox="0 0 %g %g" class="bars band" role="img">%s`, ow, oh, d.defs())
	plotW, plotH := ow-oPadR, oh-oPadT-oPadB
	group := plotW / float64(n)
	w := math.Max(0.8, group*0.8)
	for j := range n {
		tot := a[j] + c[j]
		if tot == 0 {
			continue
		}
		sa := a[j] / tot
		x := float64(j)*group + group*0.1
		fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" class="bar s1" %s data-tip="%s · %s %.0f%%"/>`, x, oPadT+plotH*(1-sa), w, plotH*sa, d.flat(1), labels[j], platforms[0].Name, sa*100)
		fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" class="bar s2" %s data-tip="%s · %s %.0f%%"/>`, x, oPadT, w, plotH*(1-sa), d.flat(2), labels[j], platforms[1].Name, (1-sa)*100)
	}
	for _, f := range []float64{0.25, 0.5, 0.75} {
		y := oPadT + plotH*(1-f)
		fmt.Fprintf(&b, `<line x1="0" y1="%.1f" x2="%g" y2="%.1f" class="mid"/><text x="%g" y="%.1f" class="tick" text-anchor="end">%s</text>`, y, plotW, y, ow, y+4, fmtPct(f*100))
	}
	overlayXLabels(&b, labels, func(j int) float64 { return float64(j)*group + group/2 })
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// ---- Tokens page ----

const tokensPerPage = 100

type tokenTable struct {
	Rows       []tokenRow
	Sort, Pad  string
	Query      string
	Total      int
	Page, Last int
	Pages      []int // page numbers to render
	PlatformOf map[string]*platform
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
	t := tokenTable{Sort: get("sort"), Pad: get("pad"), Query: strings.TrimSpace(get("q")), Page: 1, PlatformOf: map[string]*platform{}}
	if tokenSorts[t.Sort] == nil {
		t.Sort = "volume"
	}
	fmt.Sscan(get("page"), &t.Page)
	for _, p := range platforms {
		t.PlatformOf[p.Key] = p
	}
	needle := strings.ToLower(t.Query)
	for _, tok := range s.Tokens {
		if t.Pad != "" && tok.Platform != t.Pad {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(tok.Symbol+" "+tok.Name+" "+tok.Address), needle) {
			continue
		}
		row := tokenRow{Token: tok, QuoteSymbol: "?", QuoteLogo: logoOf(s.Quotes[tok.Quote]), FDV: tok.PriceUSD * tok.Supply}
		if qq := s.Quotes[tok.Quote]; qq != nil && qq.Symbol != "" {
			row.QuoteSymbol = qq.Symbol
		}
		t.Rows = append(t.Rows, row)
	}
	less := tokenSorts[t.Sort]
	sort.Slice(t.Rows, func(i, j int) bool { return less(&t.Rows[i], &t.Rows[j]) })
	t.Total = len(t.Rows)
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
	pv := envelope(s)
	pv.Title, pv.Page = "Tokens", "tokens"
	pv.Table = queryTokens(s, r.URL.Query())
	for i, p := range platforms {
		pv.Columns = append(pv.Columns, column{platform: p, Idx: i + 1})
	}
	return pv
}
