package main

import (
	"encoding/json"
	"html/template"
)

// chartMeta is embedded on each chart <svg> so the client can resolve the
// hovered bucket from the cursor's x position and show every series at once.
type chartMeta struct {
	Mode   string        `json:"mode"` // "bars" (bucket centres) or "lines" (points)
	X0     float64       `json:"x0"`   // plot left edge, viewBox units
	W      float64       `json:"w"`    // plot width, viewBox units
	Labels []string      `json:"labels"`
	Series []chartSeries `json:"series"`
}

type chartSeries struct {
	Name   string   `json:"name"`
	Class  string   `json:"cls"`
	Values []string `json:"values"` // pre-formatted; "" = no data
}

func chartAttr(m chartMeta) template.HTMLAttr {
	b, _ := json.Marshal(m)
	return template.HTMLAttr(`data-chart='` + template.HTMLEscapeString(string(b)) + `'`)
}

func formatSeries(vals []float64, fy func(float64) string) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		if v != 0 {
			out[i] = fy(v)
		}
	}
	return out
}

// overlayMeta builds chart metadata for the two-platform overlay charts.
func overlayMeta(mode string, plotW float64, labels []string, series [][]float64, fy func(float64) string) template.HTMLAttr {
	m := chartMeta{Mode: mode, X0: 0, W: plotW, Labels: labels}
	for i, s := range series {
		if s == nil {
			continue
		}
		m.Series = append(m.Series, chartSeries{Name: platforms[i].Name, Class: "s" + string(rune('1'+i)), Values: formatSeries(s, fy)})
	}
	return chartAttr(m)
}
