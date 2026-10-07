package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Token is one launched market.
type Token struct {
	Platform  string  `json:"platform"`
	Address   string  `json:"address"`
	Name      string  `json:"name"`
	Symbol    string  `json:"symbol"`
	Creator   string  `json:"creator"`
	Quote     string  `json:"quote"`
	QuoteIsC0 bool    `json:"quoteIsC0"`
	PoolID    string  `json:"poolId"`
	TokenID   string  `json:"tokenId,omitempty"` // Sender v1 locker position id
	Created   int64   `json:"created"`
	Block     uint64  `json:"block"`
	Graduated int64   `json:"graduated,omitempty"`
	Swaps     int     `json:"swaps"`
	VolQuote  float64 `json:"volQuote"`
	VolUSD    float64 `json:"volUsd"`
	Supply    float64 `json:"supply"`   // totalSupply in whole tokens
	PriceUSD  float64 `json:"priceUsd"` // from the last observed swap
	PriceAt   int64   `json:"priceAt"`
	MetaURI   string  `json:"metaUri,omitempty"` // on-chain metadata pointer (URL or inline JSON)
	Image     string  `json:"image,omitempty"`   // resolved image URL; "-" when resolution failed
	// Stockereum: who receives the creator share, and whether it is a holder distributor.
	FeeRecipient  string `json:"feeRecipient,omitempty"`
	FeesToHolders bool   `json:"feesToHolders,omitempty"`
}

// Quote is a pricing asset (ETH, WETH, stablecoins, tokenised stocks, memecoins).
type Quote struct {
	Symbol   string  `json:"symbol"`
	Decimals int     `json:"decimals"`
	USD      float64 `json:"usd"`
	PricedAt int64   `json:"pricedAt"`
	Logo     string  `json:"logo,omitempty"`
}

// Agg is one platform's activity in one UTC hour. All USD values are estimates
// derived from quote-asset amounts; see the pricing notes in the UI.
type Agg struct {
	Launches    int     `json:"launches"`
	Graduations int     `json:"graduations"`
	Swaps       int     `json:"swaps"`
	Unpriced    int     `json:"unpriced"`     // swaps with no USD price for the quote
	SpotPriced  int     `json:"spotPriced"`   // swaps priced at current spot, not hourly
	VolETH      float64 `json:"volEth"`       // ETH/WETH-quoted volume in ETH
	VolUSD      float64 `json:"volUsd"`       // all priced volume
	CreatorUSD  float64 `json:"creatorUsd"`   // accrued to creators
	HolderUSD   float64 `json:"holderUsd"`    // accrued to holder reward distributors
	PlatformUSD float64 `json:"platformUsd"`  // accrued to protocol/treasury/burn
	LaunchFees  float64 `json:"launchFeeUsd"` // launch fees paid to the platform
	CreatorPaid float64 `json:"creatorPaid"`  // actually transferred to creators
	TokenPrice  float64 `json:"tokenPrice"`   // platform token USD price, last swap in the hour (0 = none)
}

func (a *Agg) add(b *Agg) {
	a.Launches += b.Launches
	a.Graduations += b.Graduations
	a.Swaps += b.Swaps
	a.Unpriced += b.Unpriced
	a.SpotPriced += b.SpotPriced
	a.VolETH += b.VolETH
	a.VolUSD += b.VolUSD
	a.CreatorUSD += b.CreatorUSD
	a.HolderUSD += b.HolderUSD
	a.PlatformUSD += b.PlatformUSD
	a.LaunchFees += b.LaunchFees
	a.CreatorPaid += b.CreatorPaid
	if b.TokenPrice != 0 {
		a.TokenPrice = b.TokenPrice
	}
}

// State is the whole persisted snapshot; one JSON file, rewritten atomically.
type State struct {
	Head      uint64             `json:"head"`     // last processed block
	HeadTime  int64              `json:"headTime"` // timestamp of Head
	SyncedAt  int64              `json:"syncedAt"`
	Tokens    map[string]*Token  `json:"tokens"`   // token address -> token
	Pools     map[string]string  `json:"pools"`    // pool id -> token address
	TokenIDs  map[string]string  `json:"tokenIds"` // Sender v1 position id -> token address
	Holders   map[string]bool    `json:"holders"`  // Stockereum holder-distributor addresses
	Quotes    map[string]*Quote  `json:"quotes"`
	EthHourly map[string]float64 `json:"ethHourly"` // hour unix -> ETH/USD
	Hours     map[string]*Agg    `json:"hours"`     // "<platform>/<hour unix>" -> agg
	LaunchFee map[string]float64 `json:"launchFee"` // platform -> current launch fee (ETH)
	Burned    map[string]float64 `json:"burned"`    // platform -> platform-token balance at 0x…dEaD

	mu   sync.RWMutex `json:"-"`
	path string
}

func newState(path string) *State {
	return &State{
		Tokens: map[string]*Token{}, Pools: map[string]string{}, TokenIDs: map[string]string{},
		Holders: map[string]bool{}, Quotes: map[string]*Quote{}, EthHourly: map[string]float64{},
		Hours: map[string]*Agg{}, LaunchFee: map[string]float64{}, Burned: map[string]float64{}, path: path,
	}
}

func loadState(path string) (*State, error) {
	s := newState(path)
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return s, nil
}

func (s *State) save() error {
	s.mu.RLock()
	b, err := json.Marshal(s)
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func hourOf(ts int64) int64 { return ts - ts%3600 }

func hourKey(platform string, hour int64) string { return fmt.Sprintf("%s/%d", platform, hour) }

// agg returns the mutable bucket for a platform-hour, creating it if needed.
func (s *State) agg(platform string, ts int64) *Agg {
	k := hourKey(platform, hourOf(ts))
	a := s.Hours[k]
	if a == nil {
		a = &Agg{}
		s.Hours[k] = a
	}
	return a
}

// ethPrice returns the hourly ETH/USD price for ts, or the latest known.
func (s *State) ethPrice(ts int64) float64 {
	if v, ok := s.EthHourly[fmt.Sprint(hourOf(ts))]; ok {
		return v
	}
	if q := s.Quotes[ethAddr]; q != nil {
		return q.USD
	}
	return 0
}

// priceQuote converts a quote-asset amount at time ts. spot reports whether the
// current spot price (not an hourly price) was used.
func (s *State) priceQuote(quote string, amount float64, ts int64) (usd float64, spot bool) {
	if quote == ethAddr || quote == wethAddr {
		return amount * s.ethPrice(ts), false
	}
	q := s.Quotes[quote]
	if q == nil || q.USD == 0 {
		return 0, false
	}
	return amount * q.USD, true
}

func (s *State) hours() []int64 {
	seen := map[int64]bool{}
	for k := range s.Hours {
		_, hs, _ := strings.Cut(k, "/")
		h, err := strconv.ParseInt(hs, 10, 64)
		if err != nil {
			continue
		}
		seen[h] = true
	}
	out := make([]int64, 0, len(seen))
	for h := range seen {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (s *State) headAge() time.Duration {
	if s.SyncedAt == 0 {
		return 0
	}
	return time.Since(time.Unix(s.SyncedAt, 0)).Truncate(time.Second)
}
