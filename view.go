package main

import (
	"fmt"
	"html/template"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

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
	Label                                       string
	Fees, Creators, Holders, Partners, Protocol float64
	LaunchFees                                  float64
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

// pickLink switches one side-by-side column to another platform.
type pickLink struct {
	*platform
	Href string
	On   bool
}

type column struct {
	*platform
	Today        rangeStats
	Ranges       []rangeStats // 24h, 7d, 30d, all
	All          rangeStats
	Token        tokenCard
	Tokens       int
	Progress     float64 // % of the platform's history indexed
	Pick         []pickLink
	Charts       []chartView
	Days         int
	QuoteShares  []segment
	LaunchShares []segment
	TopCoins     []tokenRow
	FeeSplit     []segment
	PaidSplit    []segment
	Months       []monthRow
	HasHolders   bool // month-table columns worth showing
	HasPartners  bool
	HasLaunchFee bool
	Milestones   []string
	UnpricedPct  float64
	SpotPct      float64
}

// boardRow is one platform in the leaderboard for one window.
type boardRow struct {
	*platform
	Stats    rangeStats
	Share    float64 // % of all platforms' volume in the window
	Spark    template.HTML
	Progress float64
	Href     string // bring this platform into the side-by-side
}

type boardRange struct {
	Key, Label string
	Rows       []boardRow
	Total      rangeStats
	Donut      template.HTML // volume dominance
}

type tickerItem struct {
	*platform
	Today rangeStats
}

// pageView is the common envelope for every page; page-specific payloads hang
// off it so the shared head/dock/foot partials work everywhere.
type pageView struct {
	Title, Page string
	Head        uint64
	HeadTime    time.Time
	SyncedAt    time.Time
	ETHUSD      float64
	Empty       bool
	Board       []boardRange // overview
	Columns     []column     // overview: the side-by-side pair
	Table       tokenTable   // tokens page
	Pads        []*platform  // tokens page filter
	Host        string       // api page
	Origin      string       // scheme://host for canonical/og tags
	Path        string
	Ticker      []tickerItem // today line, every page
	Version     string       // cache-busting token for static assets
}

// hourAgg is one platform-hour bucket; hoursByPad lists them in time order
// per platform so windows are slices, not map lookups. Caller holds s.mu.
type hourAgg struct {
	H int64
	A *Agg
}

func hoursByPad(s *State) map[string][]hourAgg {
	out := make(map[string][]hourAgg, len(platforms))
	for k, a := range s.Hours {
		key, hs, ok := strings.Cut(k, "/")
		h, err := strconv.ParseInt(hs, 10, 64)
		if !ok || err != nil {
			continue
		}
		out[key] = append(out[key], hourAgg{h, a})
	}
	for _, hs := range out {
		sort.Slice(hs, func(i, j int) bool { return hs[i].H < hs[j].H })
	}
	return out
}

// sumRange totals hourly buckets in [from, to).
func sumRange(hs []hourAgg, from, to int64) rangeStats {
	var r rangeStats
	for _, x := range hs[sort.Search(len(hs), func(i int) bool { return hs[i].H >= from }):] {
		if x.H >= to {
			break
		}
		a := x.A
		r.Volume += a.VolUSD
		r.Fees += fees(*a)
		r.Protocol += a.PlatformUSD + a.LaunchFees
		r.Creators += a.CreatorUSD
		r.Trades += a.Swaps
		r.Launches += a.Launches
		r.Graduations += a.Graduations
	}
	if r.Trades > 0 {
		r.AvgTrade = r.Volume / float64(r.Trades)
	}
	return r
}

func withDelta(r, prev rangeStats) rangeStats {
	if prev.Volume > 0 || prev.Launches > 0 {
		r.HasDelta = true
		r.DVolume = pctChange(r.Volume, prev.Volume)
		r.DFees = pctChange(r.Fees, prev.Fees)
		r.DProtocol = pctChange(r.Protocol, prev.Protocol)
		r.DLaunches = pctChange(float64(r.Launches), float64(prev.Launches))
		r.DTrades = pctChange(float64(r.Trades), float64(prev.Trades))
	}
	return r
}

// windows returns the 24h, 7d, 30d and all-time stats. Deltas compare with
// the preceding window only when that window lies fully inside the data.
func windows(hs []hourAgg, now int64) []rangeStats {
	out := make([]rangeStats, 0, 4)
	for _, w := range []struct {
		key, label string
		d          int64
	}{{"24h", "24 hours", 86400}, {"7d", "7 days", 7 * 86400}, {"30d", "30 days", 30 * 86400}} {
		r := sumRange(hs, now-w.d, now+1)
		if len(hs) > 0 && now-2*w.d >= hs[0].H {
			r = withDelta(r, sumRange(hs, now-2*w.d, now-w.d))
		}
		r.Key, r.Label = w.key, w.label
		out = append(out, r)
	}
	all := sumRange(hs, 0, math.MaxInt64)
	all.Key, all.Label = "all", "All time"
	return append(out, all)
}

// todayStats sums since UTC midnight and compares with the same hours of
// yesterday so the ticker colours compare like with like.
func todayStats(hs []hourAgg, now time.Time) rangeStats {
	nowU, midnight := now.Unix(), now.Truncate(24*time.Hour).Unix()
	return withDelta(sumRange(hs, midnight, nowU+1), sumRange(hs, midnight-86400, nowU-86400+1))
}

type dayAgg struct {
	T   int64
	Agg Agg
}

// dailyAggs buckets hourly aggregates into `bucket`-second bins (86400 for
// days, 3600 for hours), ascending.
func dailyAggs(hs []hourAgg, bucket int64) []dayAgg {
	var out []dayAgg
	for _, x := range hs {
		d := x.H - x.H%bucket
		if len(out) == 0 || out[len(out)-1].T != d {
			out = append(out, dayAgg{T: d})
		}
		out[len(out)-1].Agg.add(x.A)
	}
	return out
}

func fees(a Agg) float64 { return a.CreatorUSD + a.HolderUSD + a.PlatformUSD + a.PartnerUSD }

// progress is the share of a platform's history indexed so far; 100 once its
// cursor is within an hour of the chain tip. Caller holds s.mu.
func progress(s *State, p *platform) float64 {
	h := s.Heads[p.Key]
	if s.Tip == 0 || h+300 >= s.Tip {
		return 100
	}
	if h < p.Genesis {
		return 0
	}
	return float64(h-p.Genesis) / float64(s.Tip-p.Genesis) * 100
}

// envelope fills the shared header/footer fields. Caller holds s.mu (read).
func envelope(s *State, byPad map[string][]hourAgg) pageView {
	pv := pageView{Head: s.Head, HeadTime: time.Unix(s.HeadTime, 0).UTC(), SyncedAt: time.Unix(s.SyncedAt, 0).UTC(), Version: assetVersion, Pads: platforms}
	if q := s.Quotes[ethAddr]; q != nil {
		pv.ETHUSD = q.USD
	}
	pv.Empty = len(s.Hours) == 0
	if !pv.Empty {
		now := time.Now().UTC()
		for _, p := range platforms {
			pv.Ticker = append(pv.Ticker, tickerItem{platform: p, Today: todayStats(byPad[p.Key], now)})
		}
	}
	return pv
}

// assetVersion busts browser caches for the stylesheet only when its bytes change.
var assetVersion = func() string {
	b, _ := assets.ReadFile("static/style.css")
	return fmt.Sprintf("%x", fnv32(b))
}()

// pair resolves the side-by-side selection from ?a=&b=, defaulting to the
// first two platforms and never showing one platform twice.
func pair(q url.Values) (a, b *platform) {
	a, b = platformByKey(q.Get("a")), platformByKey(q.Get("b"))
	if a == nil {
		a = platforms[0]
	}
	if b == nil || b == a {
		b = platforms[0]
		if b == a {
			b = platforms[1]
		}
	}
	return a, b
}

func pairHref(a, b *platform) string { return "/?a=" + a.Key + "&b=" + b.Key + "#cmp" }

func buildView(s *State, q url.Values) pageView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	byPad := hoursByPad(s)
	pv := envelope(s, byPad)
	pv.Title, pv.Page = "Overview", "overview"
	if pv.Empty {
		return pv
	}
	now := time.Now().UTC()
	a, b := pair(q)
	pv.Board = board(s, byPad, now, a, b)
	for side, p := range []*platform{a, b} {
		col := buildColumn(s, p, byPad[p.Key], now, true)
		other := b
		if side == 1 {
			other = a
		}
		for _, x := range platforms {
			l := pickLink{platform: x, On: x == p}
			switch {
			case x == other && side == 0:
				l.Href = pairHref(x, p) // picking the other side's pad swaps them
			case x == other:
				l.Href = pairHref(p, x)
			case side == 0:
				l.Href = pairHref(x, other)
			default:
				l.Href = pairHref(other, x)
			}
			col.Pick = append(col.Pick, l)
		}
		pv.Columns = append(pv.Columns, col)
	}
	return pv
}

// board ranks every platform per window by volume, with its share of the
// combined volume and a 30-day volume sparkline.
func board(s *State, byPad map[string][]hourAgg, now time.Time, a, b *platform) []boardRange {
	nowU := now.Unix()
	stats := make([][]rangeStats, len(platforms))
	sparks := make([]template.HTML, len(platforms))
	for i, p := range platforms {
		hs := byPad[p.Key]
		stats[i] = windows(hs, nowU)
		day := nowU - nowU%86400
		vals := make([]float64, 30)
		for _, d := range dailyAggs(hs, 86400) {
			if j := 29 - int((day-d.T)/86400); j >= 0 && j < 30 {
				vals[j] = d.Agg.VolUSD
			}
		}
		sparks[i] = spark(vals)
	}
	var out []boardRange
	for w := range stats[0] {
		br := boardRange{Key: stats[0][w].Key, Label: stats[0][w].Label}
		for i, p := range platforms {
			r := stats[i][w]
			br.Total.Volume += r.Volume
			br.Total.Fees += r.Fees
			br.Total.Protocol += r.Protocol
			br.Total.Trades += r.Trades
			br.Total.Launches += r.Launches
			href := pairHref(p, a) // bring it in next to the left column's pad
			if p == a || p == b {
				href = "#cmp"
			}
			br.Rows = append(br.Rows, boardRow{platform: p, Stats: r, Spark: sparks[i], Progress: progress(s, p), Href: href})
		}
		for i := range br.Rows {
			if br.Total.Volume > 0 {
				br.Rows[i].Share = br.Rows[i].Stats.Volume / br.Total.Volume * 100
			}
		}
		if br.Total.Trades > 0 {
			br.Total.AvgTrade = br.Total.Volume / float64(br.Total.Trades)
		}
		sort.SliceStable(br.Rows, func(i, j int) bool { return br.Rows[i].Stats.Volume > br.Rows[j].Stats.Volume })
		br.Donut = donut(br.Rows, br.Total.Volume)
		out = append(out, br)
	}
	return out
}

// buildColumn assembles one platform's numbers; detail adds the charts,
// tables and splits that only the overview columns render. Caller holds s.mu.
func buildColumn(s *State, p *platform, hs []hourAgg, now time.Time, detail bool) column {
	nowU := now.Unix()
	col := column{platform: p, Progress: progress(s, p)}
	col.Today = todayStats(hs, now)
	col.Ranges = windows(hs, nowU)
	col.All = col.Ranges[3]

	// Platform token.
	tc := tokenCard{Symbol: p.TokenSymbol, Address: p.Token, Burned: s.Burned[p.Key]}
	if t := s.Tokens[p.Token]; p.Token != "" && t != nil && t.PriceUSD > 0 {
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
		for _, x := range hs {
			if x.H >= nowU-86400 {
				break
			}
			if x.A.TokenPrice != 0 {
				p24 = x.A.TokenPrice
			}
		}
		tc.Change24 = pctChange(tc.Price, p24)
	}
	col.Token = tc

	// Tokens: quote classification, launches split, top coins.
	var qv, lc [4]float64
	var toks []tokenRow
	for _, t := range s.Tokens {
		if t.Platform != p.Key {
			continue
		}
		col.Tokens++
		if !detail {
			continue
		}
		sym := "?"
		if q := s.Quotes[t.Quote]; q != nil && q.Symbol != "" {
			sym = q.Symbol
		}
		c := quoteClass(t.Quote, sym)
		qv[c] += t.VolUSD
		lc[c]++
		toks = append(toks, tokenRow{Token: t, QuoteSymbol: sym, QuoteLogo: logoOf(s.Quotes[t.Quote]), FDV: t.PriceUSD * t.Supply})
	}
	if !detail {
		return col
	}
	names := [4]string{"ETH", "Stocks and ETFs", "Stablecoins", "Other"}
	classes := [4]string{"c-eth", "c-stock", "c-stable", "c-other"}
	col.QuoteShares = nonZero(segments(names[:], classes[:], qv[:], fmtUSD))
	col.LaunchShares = nonZero(segments([]string{"ETH and others", "Stocks and ETFs"}, []string{"c-eth", "c-stock"}, []float64{lc[0] + lc[2] + lc[3], lc[1]}, fmtK))
	sort.Slice(toks, func(a, b int) bool { return toks[a].VolUSD > toks[b].VolUSD })
	toks = toks[:min(10, len(toks))]
	for j := range toks {
		toks[j].Rank = j + 1
	}
	col.TopCoins = toks

	// Daily charts.
	days := dailyAggs(hs, 86400)
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
	chart := func(key, label string, vals []float64, total string, fy func(float64) string) chartView {
		return chartView{Key: key, Label: label, Total: total, SVG: bars(vals, labels, fy, p)}
	}
	col.Charts = []chartView{
		chart("volume", "Volume", series(func(a Agg) float64 { return a.VolUSD }), fmtUSD(col.All.Volume), fmtUSD),
		chart("launches", "Launches", series(func(a Agg) float64 { return float64(a.Launches) }), fmtK(float64(col.All.Launches)), fmtK),
		chart("fees", "Fees", series(fees), fmtUSD(col.All.Fees), fmtUSD),
		chart("revenue", "Revenue", series(func(a Agg) float64 { return a.PlatformUSD + a.LaunchFees }), fmtUSD(col.All.Protocol), fmtUSD),
		chart("trades", "Trades", series(func(a Agg) float64 { return float64(a.Swaps) }), fmtK(float64(col.All.Trades)), fmtK),
	}
	if tc.HasPrice {
		col.Charts = append(col.Charts, chart("price", "$"+p.TokenSymbol, series(func(a Agg) float64 { return a.TokenPrice }), fmtPrice(tc.Price), fmtPrice))
	}

	// Milestones: days from first activity to cumulative volume marks.
	cum, mi := 0.0, 0
	marks := []float64{10e6, 25e6, 50e6, 100e6, 250e6, 500e6, 1e9}
	for j, d := range days {
		cum += d.Agg.VolUSD
		for mi < len(marks) && cum >= marks[mi] {
			col.Milestones = append(col.Milestones, fmt.Sprintf("%d days to %s", j+1, fmtUSD(marks[mi])))
			mi++
		}
	}

	// Months, newest first, and the all-time fee routing.
	var all Agg
	for j := len(days) - 1; j >= 0; j-- {
		d := days[j]
		all.add(&d.Agg)
		label := time.Unix(d.T, 0).UTC().Format("Jan 2006")
		if n := len(col.Months); n == 0 || col.Months[n-1].Label != label {
			col.Months = append(col.Months, monthRow{Label: label})
		}
		m := &col.Months[len(col.Months)-1]
		m.Fees += fees(d.Agg)
		m.Creators += d.Agg.CreatorUSD
		m.Holders += d.Agg.HolderUSD
		m.Partners += d.Agg.PartnerUSD
		m.Protocol += d.Agg.PlatformUSD
		m.LaunchFees += d.Agg.LaunchFees
	}
	col.HasHolders, col.HasPartners, col.HasLaunchFee = all.HolderUSD > 0, all.PartnerUSD > 0, all.LaunchFees > 0
	col.FeeSplit = nonZero(segments([]string{"Creators", "Holder rewards", "Partners", "Protocol", "Launch fees"}, []string{"c-acc", "c-mid", "c-part", "c-dark", "c-dim"},
		[]float64{all.CreatorUSD, all.HolderUSD, all.PartnerUSD, all.PlatformUSD, all.LaunchFees}, fmtUSD))
	col.PaidSplit = segments([]string{"Creators", "Protocol"}, []string{"c-acc", "c-dark"}, []float64{all.CreatorPaid, all.PlatformUSD + all.LaunchFees}, fmtUSD)
	if all.Swaps > 0 {
		col.UnpricedPct = float64(all.Unpriced) / float64(all.Swaps) * 100
		col.SpotPct = float64(all.SpotPriced) / float64(all.Swaps) * 100
	}
	return col
}

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

func nonZero(segs []segment) []segment {
	out := segs[:0]
	for _, s := range segs {
		if s.Value != 0 {
			out = append(out, s)
		}
	}
	return out
}

// bars renders a single-series daily bar chart: right-side y ticks,
// first/middle/last x labels, latest bar in the platform colour (--pc).
func bars(vals []float64, labels []string, fy func(float64) string, p *platform) template.HTML {
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
	meta := chartAttr(chartMeta{Mode: "bars", X0: 0, W: w - padR, Labels: labels, Series: []chartSeries{{Name: p.Name, Class: fmt.Sprintf("p%d", p.Idx), Values: formatSeries(vals, fy)}}})
	fmt.Fprintf(&b, `<svg viewBox="0 0 %g %g" class="bars" role="img" %s>%s`, w, h, meta, d.defs())
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
		if j == n-1 {
			fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" class="glow" %s/>`, x, y, bw, bh, d.glow())
			d.bar(&b, x, y, bw, bh, true, j, "last")
			continue
		}
		d.bar(&b, x, y, bw, bh, false, j, "")
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

// spark is a tiny 30-bar volume strip in the row's platform colour; today is
// the bright bar on the right.
func spark(vals []float64) template.HTML {
	const w, h, gap = 120.0, 28.0, 1.2
	maxV := 0.0
	for _, v := range vals {
		maxV = math.Max(maxV, v)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %g %g" class="spark" aria-hidden="true">`, w, h)
	bw := w/float64(len(vals)) - gap
	for j, v := range vals {
		bh := 1.5
		if maxV > 0 {
			bh = math.Max(1.5, h*v/maxV)
		}
		cls := ""
		if j == len(vals)-1 {
			cls = ` class="now"`
		}
		fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f"%s/>`, float64(j)*(bw+gap), h-bh, bw, bh, cls)
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// donut draws each platform's share of combined volume as a ring segment in
// its colour, largest first from 12 o'clock, with the total in the middle.
func donut(rows []boardRow, total float64) template.HTML {
	const c, r = 100.0, 78.0
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 200 200" class="donut" role="img" aria-label="Share of volume"><circle cx="%g" cy="%g" r="%g" class="ring"/>`, c, c, r)
	var shown []boardRow
	for _, row := range rows {
		if row.Share > 0 {
			shown = append(shown, row)
		}
	}
	gap := 0.0
	if len(shown) > 1 {
		gap = 0.8 // percent of the ring left between segments
	}
	at := 0.0
	for _, row := range shown {
		l := math.Max(row.Share-gap, 0.35)
		fmt.Fprintf(&b, `<circle cx="%g" cy="%g" r="%g" pathLength="100" class="seg p%d" stroke-dasharray="%.2f %.2f" stroke-dashoffset="%.2f" transform="rotate(-90 %g %g)" data-tip="%s · %s · %s"/>`,
			c, c, r, row.Idx, l, 100-l, -at, c, c, template.HTMLEscapeString(row.Name), fmtPct(row.Share), fmtUSD(row.Stats.Volume))
		at += row.Share
	}
	fmt.Fprintf(&b, `<text x="%g" y="%g" class="dv">%s</text><text x="%g" y="%g" class="dl">volume</text></svg>`, c, c+4, fmtUSD(total), c, c+26)
	return template.HTML(b.String())
}
