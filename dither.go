package main

import (
	"fmt"
	"strings"
	"sync/atomic"
)

// Ordered-dither chart fills in plain SVG: a Bayer-style dot pattern per
// platform, coloured by the pN class on the pattern's own shapes (CSS sets
// --pc from it). IDs are unique per SVG because several charts share one
// document.
var svgSeq atomic.Uint64

type dither struct{ id string }

func newDither() dither { return dither{fmt.Sprintf("d%d", svgSeq.Add(1))} }

func (d dither) defs(pads []*platform) string {
	var b strings.Builder
	b.WriteString(`<defs>`)
	for _, p := range pads {
		fmt.Fprintf(&b, `<pattern id="%s-p%d" width="4" height="4" patternUnits="userSpaceOnUse"><rect width="4" height="4" class="pbg sp p%d"/><rect x="0" y="0" width="1.4" height="1.4" class="pd sp p%d"/><rect x="2" y="2" width="1.4" height="1.4" class="pd sp p%d"/></pattern>`,
			d.id, p.Idx, p.Idx, p.Idx, p.Idx)
	}
	b.WriteString(`</defs>`)
	return b.String()
}

// seg emits one platform's slice of a stacked bar: dithered body with a crisp
// cap. Slices thinner than the 4-unit pattern read as noise, so they are solid.
func (d dither) seg(b *strings.Builder, x, y, w, h float64, p *platform) {
	fill := fmt.Sprintf(`fill:url(#%s-p%d)`, d.id, p.Idx)
	if w < 5 || h < 3 {
		fill = fmt.Sprintf(`fill:var(--c%d)`, p.Idx)
	}
	fmt.Fprintf(b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" style="%s"/><rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" class="cap sp p%d"/>`,
		x, y, w, h, fill, x, y, w, min(1.5, h), p.Idx)
}
