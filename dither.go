package main

import (
	"fmt"
	"strings"
	"sync/atomic"
)

// Ordered-dither chart fills in plain SVG: a Bayer-style dot pattern per
// series, a per-shape fade mask so bars and areas thin out toward the base,
// and a blur filter for bloom. Colours come from CSS classes on the pattern
// primitives so the fills follow the theme. IDs are unique per SVG because
// several charts share one document.
var svgSeq atomic.Uint64

type dither struct{ id string }

func newDither() dither { return dither{fmt.Sprintf("d%d", svgSeq.Add(1))} }

func (d dither) defs() string {
	var b strings.Builder
	b.WriteString(`<defs>`)
	for _, s := range []string{"s0", "s1", "s2"} {
		fmt.Fprintf(&b, `<pattern id="%s-%s" width="4" height="4" patternUnits="userSpaceOnUse"><rect width="4" height="4" class="pbg %s"/><rect x="0" y="0" width="1.4" height="1.4" class="pd %s"/><rect x="2" y="2" width="1.4" height="1.4" class="pd %s"/></pattern>`, d.id, s, s, s, s)
	}
	fmt.Fprintf(&b, `<linearGradient id="%s-fade" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#fff"/><stop offset="0.55" stop-color="#fff" stop-opacity="0.75"/><stop offset="1" stop-color="#fff" stop-opacity="0.18"/></linearGradient>`, d.id)
	fmt.Fprintf(&b, `<mask id="%s-m" maskContentUnits="objectBoundingBox"><rect width="1" height="1" fill="url(#%s-fade)"/></mask>`, d.id, d.id)
	fmt.Fprintf(&b, `<filter id="%s-glow" x="-30%%" y="-30%%" width="160%%" height="160%%"><feGaussianBlur stdDeviation="3.5"/></filter>`, d.id)
	b.WriteString(`</defs>`)
	return b.String()
}

// fill returns inline style for a dithered, faded fill (inline beats the CSS
// class fills used elsewhere). Bars thinner than the 4-unit pattern read as
// noise, so they fall back to a solid faded fill.
func (d dither) fill(series int) string {
	return fmt.Sprintf(`style="fill:url(#%s-s%d)" mask="url(#%s-m)"`, d.id, series, d.id)
}

var solidFill = [3]string{"#8a8f9a", "var(--c1)", "var(--c2)"}

func (d dither) fillFor(series int, w float64) string {
	if w < 5 {
		return fmt.Sprintf(`style="fill:%s" mask="url(#%s-m)"`, solidFill[series], d.id)
	}
	return d.fill(series)
}

func (d dither) flat(series int) string {
	return fmt.Sprintf(`style="fill:url(#%s-s%d)"`, d.id, series)
}

func (d dither) glow() string { return fmt.Sprintf(`filter="url(#%s-glow)"`, d.id) }

// bar emits a dithered bar with a crisp cap; series 0 is the neutral tone.
// j is the bucket index the client tooltip highlights on hover.
func (d dither) bar(b *strings.Builder, x, y, w, h float64, series, j int, extraClass string) {
	if h <= 0 {
		return
	}
	cap := minf(2, h)
	fmt.Fprintf(b, `<g class="bar s%d %s" data-j="%d"><rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" %s/><rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" class="cap s%d"/></g>`,
		series, extraClass, j, x, y, w, h, d.fillFor(series, w), x, y, w, cap, series)
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
