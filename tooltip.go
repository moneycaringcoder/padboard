package main

import (
	"encoding/json"
	"html/template"
)

// chartMeta is embedded on each chart <svg> so the client can resolve the
// hovered bucket from the cursor's x position.
type chartMeta struct {
	Mode   string        `json:"mode"` // "bars": buckets are evenly spaced columns
	X0     float64       `json:"x0"`   // plot left edge, viewBox units
	W      float64       `json:"w"`    // plot width, viewBox units
	Labels []string      `json:"labels"`
	Series []chartSeries `json:"series"`
}

type chartSeries struct {
	Name   string   `json:"name"`
	Logo   string   `json:"logo"`   // the platform's icon, shown in the tooltip row
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
