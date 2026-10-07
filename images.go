package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Token and quote-asset icons. Launch tokens point at metadata (a URL to JSON
// or inline JSON) via metadataURI()/metadataUri(); quote assets use Alchemy's
// token metadata logo, then a stock-logo CDN for tokenised equities.

const imageBudget = 80 // tokens per platform whose metadata we resolve (by volume)

// fillImages resolves images for the highest-volume tokens still lacking one.
func (c *collector) fillImages(ctx context.Context) error {
	s := c.state
	s.mu.RLock()
	var pending []*Token
	for _, p := range platforms {
		var toks []*Token
		for _, t := range s.Tokens {
			if t.Platform == p.Key {
				toks = append(toks, t)
			}
		}
		sort.Slice(toks, func(i, j int) bool { return toks[i].VolUSD > toks[j].VolUSD })
		for i, t := range toks {
			if i >= imageBudget {
				break
			}
			if t.Image == "" {
				pending = append(pending, t)
			}
		}
	}
	s.mu.RUnlock()
	if len(pending) == 0 {
		return nil
	}

	// 1. On-chain metadata pointers, one batch per platform selector.
	for _, p := range platforms {
		sel := selMetaStk
		if p.Key == sender.Key {
			sel = selMetaSender
		}
		var addrs []string
		for _, t := range pending {
			if t.Platform == p.Key && t.MetaURI == "" {
				addrs = append(addrs, t.Address)
			}
		}
		if len(addrs) == 0 {
			continue
		}
		uris, err := c.rpc.callStrings(ctx, addrs, sel)
		if err != nil {
			return err
		}
		s.mu.Lock()
		for _, a := range addrs {
			s.Tokens[a].MetaURI = uris[a]
		}
		s.mu.Unlock()
	}

	// 2. Resolve each pointer to an image URL, 8 hosts at a time.
	var resolved atomic.Int32
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, t := range pending {
		s.mu.RLock()
		uri := t.MetaURI
		s.mu.RUnlock()
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			img := resolveImage(ctx, c.prices.http, uri)
			if img == "" {
				img = "-"
			} else {
				resolved.Add(1)
			}
			s.mu.Lock()
			t.Image = img
			s.mu.Unlock()
		})
	}
	wg.Wait()
	log.Printf("images: resolved %d/%d", resolved.Load(), len(pending))
	return nil
}

// resolveImage turns a metadata pointer into an https image URL, or "".
func resolveImage(ctx context.Context, hc *http.Client, uri string) string {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return ""
	}
	var raw []byte
	if strings.HasPrefix(uri, "{") {
		raw = []byte(uri)
	} else {
		u := gatewayURL(uri)
		if !strings.HasPrefix(u, "https://") {
			return ""
		}
		ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			return ""
		}
		resp, err := hc.Do(req)
		if err != nil {
			return ""
		}
		defer resp.Body.Close()
		if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "image/") {
			return u // the pointer was the image itself
		}
		raw, _ = io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	for _, k := range []string{"image", "image_url", "imageUrl", "logo", "icon"} {
		if v, ok := m[k].(string); ok && v != "" {
			if u := gatewayURL(v); strings.HasPrefix(u, "https://") {
				return u
			}
		}
	}
	return ""
}

func gatewayURL(u string) string {
	switch {
	case strings.HasPrefix(u, "ipfs://"):
		return "https://ipfs.io/ipfs/" + strings.TrimPrefix(strings.TrimPrefix(u, "ipfs://"), "ipfs/")
	case strings.HasPrefix(u, "ar://"):
		return "https://arweave.net/" + strings.TrimPrefix(u, "ar://")
	}
	return u
}

// quoteLogo picks an icon for a quote asset: Alchemy's token logo, else a
// stock-logo CDN for tokenised equities (NVDAon → NVDA), else none.
func (c *collector) quoteLogo(ctx context.Context, addr, symbol string) string {
	if addr == ethAddr || addr == wethAddr {
		return "/static/eth.svg"
	}
	var meta struct {
		Logo string `json:"logo"`
	}
	if err := c.rpc.call(ctx, "alchemy_getTokenMetadata", []any{addr}, &meta); err == nil && meta.Logo != "" {
		return meta.Logo
	}
	if quoteClass(addr, symbol) == 1 {
		ticker := strings.TrimSuffix(symbol, "on")
		if symbol == "bCSPX" {
			ticker = "CSPX"
		}
		return "https://financialmodelingprep.com/image-stock/" + ticker + ".png"
	}
	return ""
}

// fillQuoteLogos resolves icons for quotes seen before logo support existed.
func (c *collector) fillQuoteLogos(ctx context.Context) {
	s := c.state
	s.mu.RLock()
	type q struct{ addr, sym string }
	var todo []q
	for a, v := range s.Quotes {
		if v.Logo == "" {
			todo = append(todo, q{a, v.Symbol})
		}
	}
	s.mu.RUnlock()
	for _, t := range todo {
		logo := c.quoteLogo(ctx, t.addr, t.sym)
		if logo == "" {
			logo = "-"
		}
		s.mu.Lock()
		s.Quotes[t.addr].Logo = logo
		s.mu.Unlock()
	}
}
