package main

import (
	"embed"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

//go:embed templates/*.html static/*
var assets embed.FS

var tmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"usd": fmtUSD, "num": fmtNum, "k": fmtKAny, "pct": fmtPct, "pct2": fmtPct2, "ago": fmtAgo, "eth": fmtETH,
	"short": shortAddr, "delta": fmtDelta, "price": fmtPrice, "ratio": fmtRatio, "lower": strings.ToLower,
	"inc":  func(i int) int { return i + 1 },
	"add":  func(a, b int) int { return a + b },
	"sub":  func(a, b int) int { return a - b },
	"icon": iconHTML,
	"int":  func(v uint64) int { return int(v) },
}).ParseFS(assets, "templates/*.html"))

// rangeStats sums activity over a window; D* are percentage deltas vs the
// preceding window of equal length (0 when there is no prior data).
type rangeStats struct {
	Key, Label                       string
	Volume, Fees, Protocol, Creators float64
	Trades, Launches, Graduations    int
	AvgTrade                         float64
	DVolume, DFees, DProtocol        float64
	DLaunches, DTrades               float64
	HasDelta                         bool
}

type segment struct {
	Label string
	Value float64
	Pct   float64
	Text  string // formatted value
	Class string
}

type monthRow struct {
	Label                             string
	Fees, Creators, Holders, Protocol float64
	LaunchFees                        float64
}

type tokenRow struct {
	*Token
	Rank        int
	QuoteSymbol string
	QuoteLogo   string
	FDV         float64
}

type tokenCard struct {
	Symbol, Address, Image    string
	Price, Change24           float64
	Supply, Burned, BurnedPct float64
	BurnedValue, FDV, Mcap    float64
	HasPrice                  bool
}

type chartView struct {
	Key, Label, Total string
	SVG               template.HTML
}

type column struct {
	*platform
	Idx          int
	Today        rangeStats
	Ranges       []rangeStats // 24h, 7d, 30d, all
	Charts       []chartView
	Days         int
	Week         rangeStats
	QuoteShares  []segment
	LaunchShares []segment
	TopCoins     []tokenRow
	FeeSplit     []segment
	Months       []monthRow
	Token        tokenCard
	All          rangeStats
	PaidSplit    []segment
	Milestones   []string
	Tokens       int
	UnpricedPct  float64
	SpotPct      float64
	Quotes       int
}

// pageView is the common envelope for every page; page-specific payloads hang
// off it so the shared head/dock/foot partials work everywhere.
type pageView struct {
	Title, Page string
	Columns     []column
	Head        uint64
	HeadTime    time.Time
	SyncedAt    time.Time
	ETHUSD      float64
	Empty       bool
	Charts      []wallChart // charts page
	Table       tokenTable  // tokens page
	Host        string      // api page
}

type dayAgg struct {
	T   int64
	Agg Agg
}

// envelope fills the shared header/footer fields. Caller holds s.mu (read).
func envelope(s *State) pageView {
	pv := pageView{Head: s.Head, HeadTime: time.Unix(s.HeadTime, 0).UTC(), SyncedAt: time.Unix(s.SyncedAt, 0).UTC()}
	if q := s.Quotes[ethAddr]; q != nil {
		pv.ETHUSD = q.USD
	}
	return pv
}

func buildView(s *State) pageView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pv := envelope(s)
	pv.Title, pv.Page = "Overview", "overview"
	hours := s.hours()
	if len(hours) == 0 {
		pv.Empty = true
		return pv
	}
	now := time.Now().UTC()
	nowU := now.Unix()
	midnight := now.Truncate(24 * time.Hour).Unix()

	for i, p := range platforms {
		col := column{platform: p, Idx: i + 1}
		sum := func(from, to int64) rangeStats {
			var r rangeStats
			for _, h := range hours {
				if h < from || h >= to {
					continue
				}
				a := s.Hours[hourKey(p.Key, h)]
				if a == nil {
					continue
				}
				r.Volume += a.VolUSD
				r.Fees += a.CreatorUSD + a.HolderUSD + a.PlatformUSD
				r.Protocol += a.PlatformUSD + a.LaunchFees
				r.Creators += a.CreatorUSD
				r.Trades += a.Swaps
				r.Launches += a.Launches
				r.Graduations += a.Graduations
			}
			return r
		}
		window := func(key, label string, d int64) rangeStats {
			r := sum(nowU-d, nowU+1)
			prev := sum(nowU-2*d, nowU-d)
			r.Key, r.Label = key, label
			// Only compare against a prior window that is fully inside the data.
			if first := firstHour(s, p, hours); first != 0 && nowU-2*d >= first && (prev.Volume > 0 || prev.Launches > 0) {
				r.HasDelta = true
				r.DVolume = pctChange(r.Volume, prev.Volume)
				r.DFees = pctChange(r.Fees, prev.Fees)
				r.DProtocol = pctChange(r.Protocol, prev.Protocol)
				r.DLaunches = pctChange(float64(r.Launches), float64(prev.Launches))
				r.DTrades = pctChange(float64(r.Trades), float64(prev.Trades))
			}
			return r
		}
		col.Today = sum(midnight, nowU+1)
		col.All = sum(0, math.MaxInt64)
		col.All.Key, col.All.Label = "all", "All time"
		col.Ranges = []rangeStats{window("24h", "24 hours", 86400), window("7d", "7 days", 7*86400), window("30d", "30 days", 30*86400), col.All}
		for i := range col.Ranges {
			if r := &col.Ranges[i]; r.Trades > 0 {
				r.AvgTrade = r.Volume / float64(r.Trades)
			}
		}
		col.Week = col.Ranges[1]

		days := dailyAggs(s, p, hours, 86400)
		col.Days = len(days)
		labels := make([]string, len(days))
		for j, d := range days {
			labels[j] = time.Unix(d.T, 0).UTC().Format("Jan 2")
		}
		series := func(f func(Agg) float64) []float64 {
			out := make([]float64, len(days))
			for j, d := range days {
				out[j] = f(d.Agg)
			}
			return out
		}
		mkChart := func(key, label string, vals []float64, total string, fy func(float64) string) chartView {
			return chartView{Key: key, Label: label, Total: total, SVG: bars(vals, labels, fy, i+1)}
		}
		col.Charts = []chartView{
			mkChart("volume", "Volume", series(func(a Agg) float64 { return a.VolUSD }), fmtUSD(col.All.Volume), fmtUSD),
			mkChart("launches", "Launches", series(func(a Agg) float64 { return float64(a.Launches) }), fmtK(float64(col.All.Launches)), fmtKf),
			mkChart("fees", "Fees", series(fees), fmtUSD(col.All.Fees), fmtUSD),
			mkChart("revenue", "Revenue", series(func(a Agg) float64 { return a.PlatformUSD + a.LaunchFees }), fmtUSD(col.All.Protocol), fmtUSD),
			mkChart("trades", "Trades", series(func(a Agg) float64 { return float64(a.Swaps) }), fmtK(float64(col.All.Trades)), fmtKf),
		}
		if t := s.Tokens[p.Token]; t != nil {
			col.Charts = append(col.Charts, mkChart("price", "$"+p.TokenSymbol, series(func(a Agg) float64 { return a.TokenPrice }), fmtPrice(t.PriceUSD), fmtPrice))
		}

		// Milestones: days from first activity to cumulative volume marks.
		cum := 0.0
		marks := []float64{10e6, 25e6, 50e6, 100e6, 250e6, 500e6, 1e9}
		mi := 0
		for j, d := range days {
			cum += d.Agg.VolUSD
			for mi < len(marks) && cum >= marks[mi] {
				col.Milestones = append(col.Milestones, fmt.Sprintf("%d days to %s", j+1, fmtUSD(marks[mi])))
				mi++
			}
		}

		// Months.
		months := map[string]*monthRow{}
		var mkeys []string
		for _, d := range days {
			k := time.Unix(d.T, 0).UTC().Format("2006-01")
			m := months[k]
			if m == nil {
				m = &monthRow{Label: time.Unix(d.T, 0).UTC().Format("Jan 2006")}
				months[k] = m
				mkeys = append(mkeys, k)
			}
			m.Fees += fees(d.Agg)
			m.Creators += d.Agg.CreatorUSD
			m.Holders += d.Agg.HolderUSD
			m.Protocol += d.Agg.PlatformUSD
			m.LaunchFees += d.Agg.LaunchFees
		}
		sort.Sort(sort.Reverse(sort.StringSlice(mkeys)))
		for _, k := range mkeys {
			col.Months = append(col.Months, *months[k])
		}

		// Tokens: quote classification, launches split, top coins.
		var qv, lc [4]float64
		var toks []tokenRow
		for _, t := range s.Tokens {
			if t.Platform != p.Key {
				continue
			}
			col.Tokens++
			sym := "?"
			if q := s.Quotes[t.Quote]; q != nil && q.Symbol != "" {
				sym = q.Symbol
			}
			c := quoteClass(t.Quote, sym)
			qv[c] += t.VolUSD
			lc[c]++
			toks = append(toks, tokenRow{Token: t, QuoteSymbol: sym, QuoteLogo: logoOf(s.Quotes[t.Quote]), FDV: t.PriceUSD * t.Supply})
		}
		names := [4]string{"ETH", "Stocks and ETFs", "Stablecoins", "Other"}
		classes := [4]string{"c-eth", "c-stock", "c-stable", "c-other"}
		col.QuoteShares = segments(names[:], classes[:], qv[:], fmtUSD)
		col.LaunchShares = segments([]string{"ETH and others", "Stocks and ETFs"}, []string{"c-eth", "c-stock"}, []float64{lc[0] + lc[2] + lc[3], lc[1]}, fmtKf)
		sort.Slice(toks, func(a, b int) bool { return toks[a].VolUSD > toks[b].VolUSD })
		if len(toks) > 10 {
			toks = toks[:10]
		}
		for j := range toks {
			toks[j].Rank = j + 1
		}
		col.TopCoins = toks

		// Fee routing.
		var all Agg
		for _, h := range hours {
			if a := s.Hours[hourKey(p.Key, h)]; a != nil {
				all.add(a)
			}
		}
		col.FeeSplit = segments([]string{"Creators", "Holder rewards", "Protocol", "Launch fees"}, []string{"c-acc", "c-mid", "c-dark", "c-dim"},
			[]float64{all.CreatorUSD, all.HolderUSD, all.PlatformUSD, all.LaunchFees}, fmtUSD)
		col.PaidSplit = segments([]string{"Creators", "Protocol"}, []string{"c-acc", "c-dark"}, []float64{all.CreatorPaid, all.PlatformUSD + all.LaunchFees}, fmtUSD)
		if all.Swaps > 0 {
			col.UnpricedPct = float64(all.Unpriced) / float64(all.Swaps) * 100
			col.SpotPct = float64(all.SpotPriced) / float64(all.Swaps) * 100
		}
		col.Quotes = len(s.Quotes)

		// Platform token.
		tc := tokenCard{Symbol: p.TokenSymbol, Address: p.Token, Burned: s.Burned[p.Key]}
		if t := s.Tokens[p.Token]; t != nil && t.PriceUSD > 0 {
			tc.Image = t.Image
			tc.HasPrice = true
			tc.Price, tc.Supply = t.PriceUSD, t.Supply
			tc.FDV = tc.Price * tc.Supply
			tc.Mcap = tc.Price * (tc.Supply - tc.Burned)
			tc.BurnedValue = tc.Burned * tc.Price
			if tc.Supply > 0 {
				tc.BurnedPct = tc.Burned / tc.Supply * 100
			}
			var p24 float64
			for _, h := range hours {
				if h >= nowU-86400 {
					break
				}
				if a := s.Hours[hourKey(p.Key, h)]; a != nil && a.TokenPrice != 0 {
					p24 = a.TokenPrice
				}
			}
			tc.Change24 = pctChange(tc.Price, p24)
		}
		col.Token = tc
		pv.Columns = append(pv.Columns, col)
	}
	return pv
}

func fees(a Agg) float64 { return a.CreatorUSD + a.HolderUSD + a.PlatformUSD }

// quoteClass buckets a quote asset: 0 ETH, 1 tokenised stock/ETF, 2 stablecoin, 3 other.
func quoteClass(addr, sym string) int {
	switch {
	case addr == ethAddr || addr == wethAddr:
		return 0
	case strings.HasSuffix(sym, "on") && len(sym) > 3 && strings.ToUpper(sym[:len(sym)-2]) == sym[:len(sym)-2], sym == "bCSPX":
		return 1
	case sym == "USDC" || sym == "USDT" || sym == "DAI" || sym == "USDY" || sym == "USDG":
		return 2
	default:
		return 3
	}
}

func segments(labels, classes []string, vals []float64, f func(float64) string) []segment {
	total := 0.0
	for _, v := range vals {
		total += v
	}
	out := make([]segment, 0, len(vals))
	for i, v := range vals {
		seg := segment{Label: labels[i], Value: v, Class: classes[i], Text: f(v)}
		if total > 0 {
			seg.Pct = v / total * 100
		}
		out = append(out, seg)
	}
	return out
}

// bars renders a single-series daily bar chart in the Pons style: right-side
// y ticks, first/middle/last x labels, latest bar in the platform colour.
func bars(vals []float64, labels []string, fy func(float64) string, series int) template.HTML {
	n := len(vals)
	if n == 0 {
		return ""
	}
	const w, h, padR, padB, padT = 600.0, 230.0, 58.0, 22.0, 6.0
	maxV := 0.0
	for _, v := range vals {
		maxV = math.Max(maxV, v)
	}
	if maxV == 0 {
		maxV = 1
	}
	var b strings.Builder
	d := newDither()
	fmt.Fprintf(&b, `<svg viewBox="0 0 %g %g" class="bars" role="img">%s`, w, h, d.defs())
	plotW, plotH := w-padR, h-padT-padB
	for _, f := range []float64{1.0 / 3, 2.0 / 3, 1} {
		y := padT + plotH*(1-f)
		fmt.Fprintf(&b, `<line x1="0" y1="%.1f" x2="%g" y2="%.1f" class="grid"/><text x="%g" y="%.1f" class="tick" text-anchor="end">%s</text>`, y, plotW, y, w, y+4, fy(maxV*f))
	}
	group := plotW / float64(n)
	bw := math.Max(1, group*0.68)
	for j, v := range vals {
		bh := math.Max(2, plotH*v/maxV)
		x, y := float64(j)*group+(group-bw)/2, padT+plotH-bh
		tip := fmt.Sprintf("%s: %s", labels[j], fy(v))
		if j == n-1 {
			fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" class="glow s%d" %s/>`, x, y, bw, bh, series, d.glow())
			d.bar(&b, x, y, bw, bh, series, tip, "last")
			continue
		}
		d.bar(&b, x, y, bw, bh, 0, tip, "")
	}
	for _, j := range []int{0, n / 2, n - 1} {
		anchor := "start"
		if j == n-1 {
			anchor = "end"
		} else if j == n/2 {
			anchor = "middle"
		}
		fmt.Fprintf(&b, `<text x="%.1f" y="%g" class="tick" text-anchor="%s">%s</text>`, float64(j)*group+group/2, h-6, anchor, labels[j])
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// ---- formatting ----

func fmtUSD(v float64) string {
	switch a := math.Abs(v); {
	case a >= 1e9:
		return fmt.Sprintf("$%.1fB", v/1e9)
	case a >= 1e6:
		return fmt.Sprintf("$%.1fM", v/1e6)
	case a >= 1e3:
		return fmt.Sprintf("$%.1fK", v/1e3)
	case a == 0:
		return "$0"
	case a < 10:
		return fmt.Sprintf("$%.2f", v)
	default:
		return fmt.Sprintf("$%.0f", v)
	}
}

func fmtK(v float64) string {
	switch a := math.Abs(v); {
	case a >= 1e9:
		return fmt.Sprintf("%.1fB", v/1e9)
	case a >= 1e6:
		return fmt.Sprintf("%.1fM", v/1e6)
	case a >= 1e3:
		return fmt.Sprintf("%.1fK", v/1e3)
	default:
		return fmt.Sprintf("%.0f", v)
	}
}

var fmtKf = fmtK

func fmtKAny(v any) string {
	switch x := v.(type) {
	case int:
		return fmtK(float64(x))
	case float64:
		return fmtK(x)
	}
	return fmt.Sprint(v)
}

func fmtPrice(v float64) string {
	switch {
	case v == 0:
		return "–"
	case v >= 1:
		return fmt.Sprintf("$%.2f", v)
	case v >= 0.01:
		return fmt.Sprintf("$%.4f", v)
	default:
		return fmt.Sprintf("$%.6f", v)
	}
}

func fmtRatio(v float64) string {
	switch {
	case v == 0:
		return "–"
	case v >= 100:
		return fmt.Sprintf("%.0f×", v)
	case v >= 10:
		return fmt.Sprintf("%.1f×", v)
	default:
		return fmt.Sprintf("%.2f×", v)
	}
}

func fmtNum(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func fmtPct(v float64) string  { return fmt.Sprintf("%.0f%%", v) }
func fmtPct2(v float64) string { return fmt.Sprintf("%.2f%%", v) }

func fmtETH(v float64) string {
	if v >= 1000 {
		return fmt.Sprintf("%s ETH", fmtNum(int(v)))
	}
	return fmt.Sprintf("%.3g ETH", v)
}

func fmtAgo(ts int64) string {
	d := time.Since(time.Unix(ts, 0))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// fmtDelta renders a signed percentage change, Pons-style.
func fmtDelta(p float64) template.HTML {
	if p == 0 || math.IsInf(p, 0) || math.IsNaN(p) {
		return ""
	}
	cls := "up"
	if p < 0 {
		cls = "down"
	}
	return template.HTML(fmt.Sprintf(`<span class="%s">%+.0f%%</span>`, cls, math.Max(-999, math.Min(999, p))))
}

func pctChange(cur, prev float64) float64 {
	if prev == 0 {
		return 0
	}
	return (cur - prev) / prev * 100
}

func shortAddr(a string) string {
	if len(a) < 12 {
		return a
	}
	return a[:6] + "…" + a[len(a)-4:]
}

func (c *collector) routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/static/", http.FileServer(http.FS(assets)))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		c.state.mu.RLock()
		head := c.state.Head
		c.state.mu.RUnlock()
		fmt.Fprintf(w, "ok head=%d\n", head)
	})
	api := http.NewServeMux()
	api.HandleFunc("/api/snapshot.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		http.ServeFile(w, r, c.state.path)
	})
	api.HandleFunc("/api/v1/summary", c.apiSummary)
	api.HandleFunc("/api/v1/daily", c.apiDaily)
	api.HandleFunc("/api/v1/tokens", c.apiTokens)
	api.HandleFunc("/api/v1/tokens/{address}", c.apiToken)
	mux.Handle("/api/", newRateLimit().wrap(api))

	page := func(name string, build func(*http.Request) pageView) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if err := tmpl.ExecuteTemplate(w, name, build(r)); err != nil {
				http.Error(w, err.Error(), 500)
			}
		}
	}
	mux.HandleFunc("/{$}", page("overview.html", func(*http.Request) pageView { return buildView(c.state) }))
	mux.HandleFunc("/charts", page("charts.html", func(*http.Request) pageView { return buildCharts(c.state) }))
	mux.HandleFunc("/tokens", page("tokens.html", func(r *http.Request) pageView { return buildTokens(c.state, r) }))
	mux.HandleFunc("/api", page("api.html", func(r *http.Request) pageView {
		c.state.mu.RLock()
		defer c.state.mu.RUnlock()
		pv := envelope(c.state)
		pv.Title, pv.Page = "API", "api"
		pv.Host = r.Host
		return pv
	}))
	return mux
}

func logoOf(q *Quote) string {
	if q == nil || q.Logo == "-" {
		return ""
	}
	return q.Logo
}

// iconHTML renders a small round icon, falling back to the first letters of
// the label when no image is known. Broken remote images swap to the fallback.
func iconHTML(src, label string) template.HTML {
	initials := strings.ToUpper(label)
	if len(initials) > 2 {
		initials = initials[:2]
	}
	fb := fmt.Sprintf(`<i class="ico fb" title="%s">%s</i>`, template.HTMLEscapeString(label), template.HTMLEscapeString(initials))
	if src == "" || src == "-" {
		return template.HTML(fb)
	}
	return template.HTML(fmt.Sprintf(`<img class="ico" src="%s" alt="%s" loading="lazy" referrerpolicy="no-referrer" onerror="this.outerHTML=this.nextElementSibling.innerHTML"><template>%s</template>`,
		template.HTMLEscapeString(src), template.HTMLEscapeString(label), fb))
}

// firstHour is the earliest hour with activity for a platform (0 if none).
func firstHour(s *State, p *platform, hours []int64) int64 {
	for _, h := range hours {
		if s.Hours[hourKey(p.Key, h)] != nil {
			return h
		}
	}
	return 0
}
