package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Alchemy Prices API: spot by address and hourly ETH history.
type priceClient struct {
	base string // https://api.g.alchemy.com/prices/v1/<key>
	http *http.Client
}

func (p *priceClient) post(ctx context.Context, path string, body any, out any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", p.base+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != 200 {
		return fmt.Errorf("prices %s: http %d: %.200s", path, resp.StatusCode, raw)
	}
	return json.Unmarshal(raw, out)
}

// spot fills USD prices for the given token addresses (25 per request).
func (p *priceClient) spot(ctx context.Context, addrs []string) (map[string]float64, error) {
	out := map[string]float64{}
	type ref struct {
		Network string `json:"network"`
		Address string `json:"address"`
	}
	for i := 0; i < len(addrs); i += 25 {
		end := min(i+25, len(addrs))
		refs := make([]ref, 0, end-i)
		for _, a := range addrs[i:end] {
			if a == ethAddr {
				a = wethAddr
			}
			refs = append(refs, ref{"eth-mainnet", a})
		}
		var resp struct {
			Data []struct {
				Address string `json:"address"`
				Prices  []struct {
					Value string `json:"value"`
				} `json:"prices"`
			} `json:"data"`
		}
		if err := p.post(ctx, "/tokens/by-address", map[string]any{"addresses": refs}, &resp); err != nil {
			return nil, err
		}
		for _, d := range resp.Data {
			if len(d.Prices) == 0 {
				continue
			}
			v, err := strconv.ParseFloat(d.Prices[0].Value, 64)
			if err != nil || v <= 0 {
				continue
			}
			out[toLower(d.Address)] = v
			if toLower(d.Address) == wethAddr {
				out[ethAddr] = v
			}
		}
	}
	return out, nil
}

// ethHourly returns hour-aligned ETH/USD prices in [from, to]; the API caps
// hourly requests at 30 days, so the range is walked in 29-day windows.
func (p *priceClient) ethHourly(ctx context.Context, from, to time.Time) (map[string]float64, error) {
	out := map[string]float64{}
	for start := from; start.Before(to); start = start.Add(29 * 24 * time.Hour) {
		end := start.Add(29 * 24 * time.Hour)
		if end.After(to) {
			end = to
		}
		var resp struct {
			Data []struct {
				Value     string    `json:"value"`
				Timestamp time.Time `json:"timestamp"`
			} `json:"data"`
		}
		body := map[string]string{"symbol": "ETH", "startTime": start.UTC().Format(time.RFC3339), "endTime": end.UTC().Format(time.RFC3339), "interval": "1h"}
		if err := p.post(ctx, "/tokens/historical", body, &resp); err != nil {
			return nil, err
		}
		for _, d := range resp.Data {
			v, err := strconv.ParseFloat(d.Value, 64)
			if err != nil {
				continue
			}
			out[fmt.Sprint(hourOf(d.Timestamp.Unix()))] = v
		}
	}
	return out, nil
}
