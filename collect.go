package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	chunkBlocks   = 10_000 // Alchemy: unlimited logs per response within 10k blocks
	confirmations = 12
	poolTopicMax  = 1_500 // pool ids per Swap filter request
)

var toLower = strings.ToLower

type collector struct {
	rpc    *rpcClient
	prices *priceClient
	state  *State
}

// sync advances the state from Head to the current safe head, chunk by chunk,
// checkpointing after each chunk so a crash resumes cleanly.
func (c *collector) sync(ctx context.Context) error {
	latest, err := c.rpc.blockNumber(ctx)
	if err != nil {
		return err
	}
	safe := latest - confirmations
	s := c.state
	if s.Head == 0 {
		s.Head = genesisBlock - 1
	}
	if err := c.refreshPrices(ctx, safe); err != nil {
		return fmt.Errorf("prices: %w", err)
	}
	for from := s.Head + 1; from <= safe; from += chunkBlocks {
		to := min(from+chunkBlocks-1, safe)
		t0 := time.Now()
		n, err := c.processRange(ctx, from, to)
		if err != nil {
			return fmt.Errorf("blocks %d-%d: %w", from, to, err)
		}
		s.mu.Lock()
		s.Head = to
		s.SyncedAt = time.Now().Unix()
		s.mu.Unlock()
		if err := s.save(); err != nil {
			return err
		}
		log.Printf("synced %d-%d: %d logs, %d tokens, %s", from, to, n, len(s.Tokens), time.Since(t0).Truncate(time.Millisecond))
	}
	if ts, err := c.rpc.blockTime(ctx, s.Head); err == nil {
		s.mu.Lock()
		s.HeadTime = ts
		s.mu.Unlock()
	}
	if err := c.refreshBurned(ctx); err != nil {
		return err
	}
	c.fillQuoteLogos(ctx)
	if err := c.fillImages(ctx); err != nil {
		log.Printf("images: %v", err)
	}
	return s.save()
}

// refreshPrices updates spot prices for every known quote and extends the
// hourly ETH series up to the present.
func (c *collector) refreshPrices(ctx context.Context, head uint64) error {
	s := c.state
	s.mu.RLock()
	addrs := []string{ethAddr}
	for a := range s.Quotes {
		if a != ethAddr {
			addrs = append(addrs, a)
		}
	}
	s.mu.RUnlock()
	if err := c.priceQuotes(ctx, addrs); err != nil {
		return err
	}
	// ETH hourly: from the last stored hour (or genesis) to now.
	start := time.Unix(1_788_300_000, 0) // 2026-09-01, before either factory existed
	s.mu.RLock()
	for k := range s.EthHourly {
		var h int64
		fmt.Sscan(k, &h)
		if t := time.Unix(h, 0); t.After(start) {
			start = t
		}
	}
	s.mu.RUnlock()
	if time.Since(start) < 30*time.Minute {
		return nil
	}
	series, err := c.prices.ethHourly(ctx, start, time.Now())
	if err != nil {
		return err
	}
	s.mu.Lock()
	for k, v := range series {
		s.EthHourly[k] = v
	}
	s.mu.Unlock()
	return nil
}

// priceQuotes fetches spot prices and, for unseen quotes, decimals and symbol.
func (c *collector) priceQuotes(ctx context.Context, addrs []string) error {
	if len(addrs) == 0 {
		return nil
	}
	prices, err := c.prices.spot(ctx, addrs)
	if err != nil {
		return err
	}
	s := c.state
	var unknown []string
	s.mu.Lock()
	now := time.Now().Unix()
	for _, a := range addrs {
		q := s.Quotes[a]
		if q == nil {
			q = &Quote{Decimals: 18}
			if a == ethAddr {
				q.Symbol = "ETH"
			} else {
				unknown = append(unknown, a)
			}
			s.Quotes[a] = q
		}
		if v, ok := prices[a]; ok {
			q.USD, q.PricedAt = v, now
		}
	}
	s.mu.Unlock()
	if len(unknown) == 0 {
		return nil
	}
	symbols, err := c.rpc.callStrings(ctx, unknown, selSymbol)
	if err != nil {
		return err
	}
	logos := make(map[string]string, len(unknown))
	for _, a := range unknown {
		logos[a] = c.quoteLogo(ctx, a, symbols[a])
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range unknown {
		s.Quotes[a].Symbol = symbols[a]
		s.Quotes[a].Logo = logos[a]
		if d, err := c.rpc.callUint(ctx, a, selDecimals); err == nil {
			s.Quotes[a].Decimals = int(d)
		}
	}
	return nil
}

// processRange fetches and applies every relevant log in [from, to].
func (c *collector) processRange(ctx context.Context, from, to uint64) (int, error) {
	s := c.state
	f := logFilter{FromBlock: hexQty(from), ToBlock: hexQty(to)}

	// 1. Platform contracts: launches, fee events, graduations.
	f.Address = allPlatformAddrs()
	plat, err := c.rpc.getLogs(ctx, f)
	if err != nil {
		return 0, err
	}
	for _, l := range plat {
		if err := c.applyLaunch(ctx, l); err != nil {
			return 0, err
		}
	}

	// 2. Pool initialisation for pools launched in this range (gives quote side).
	var newPools []string
	for _, l := range plat {
		switch l.Topics[0] {
		case tStkLaunchedV1, tStkLaunchedV2:
			newPools = append(newPools, "0x"+hexOf(word(hexBytes(l.Data), 0)))
		case tSndLaunched:
			newPools = append(newPools, toLower(l.Topics[3]))
		case tSndLaunchedV2:
			newPools = append(newPools, toLower(l.Topics[1]))
		}
	}
	if len(newPools) > 0 {
		inits, err := c.rpc.getLogs(ctx, logFilter{FromBlock: f.FromBlock, ToBlock: f.ToBlock, Address: []string{poolManager}, Topics: []any{tPMInitialize, newPools}})
		if err != nil {
			return 0, err
		}
		var newQuotes []string
		s.mu.Lock()
		for _, l := range inits {
			tok := s.Tokens[s.Pools[toLower(l.Topics[1])]]
			if tok == nil {
				continue
			}
			c0, c1 := topicAddr(l.Topics[2]), topicAddr(l.Topics[3])
			tok.QuoteIsC0 = c0 != tok.Address
			tok.Quote = c1
			if tok.QuoteIsC0 {
				tok.Quote = c0
			}
			if s.Quotes[tok.Quote] == nil {
				newQuotes = append(newQuotes, tok.Quote)
			}
		}
		s.mu.Unlock()
		if err := c.priceQuotes(ctx, dedupe(newQuotes)); err != nil {
			return 0, err
		}
	}

	// 3. Swaps on every known pool.
	s.mu.RLock()
	pools := make([]string, 0, len(s.Pools))
	for id := range s.Pools {
		pools = append(pools, id)
	}
	s.mu.RUnlock()
	// Pool-id chunks are independent filters; fetch them concurrently.
	parts := make([][]Log, (len(pools)+poolTopicMax-1)/poolTopicMax)
	errs := make([]error, len(parts))
	var wg sync.WaitGroup
	for k := range parts {
		part := pools[k*poolTopicMax : min((k+1)*poolTopicMax, len(pools))]
		wg.Go(func() {
			parts[k], errs[k] = c.rpc.getLogs(ctx, logFilter{FromBlock: f.FromBlock, ToBlock: f.ToBlock, Address: []string{poolManager}, Topics: []any{tPMSwap, part}})
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return 0, err
	}
	var swaps []Log
	for _, ls := range parts {
		swaps = append(swaps, ls...)
	}

	// 4. Apply fee/graduation/swap logs in chain order.
	rest := append(swaps, plat...)
	sort.Slice(rest, func(i, j int) bool {
		if rest[i].BlockNumber != rest[j].BlockNumber {
			return rest[i].BlockNumber < rest[j].BlockNumber
		}
		return rest[i].LogIndex < rest[j].LogIndex
	})
	s.mu.Lock()
	for _, l := range rest {
		ts, err := c.logTime(ctx, l)
		if err != nil {
			s.mu.Unlock()
			return 0, err
		}
		c.applyEvent(l, ts)
	}
	s.mu.Unlock()
	return len(plat) + len(swaps), c.fillMetadata(ctx)
}

// fillMetadata fetches name/symbol/supply for tokens not yet described.
func (c *collector) fillMetadata(ctx context.Context) error {
	s := c.state
	s.mu.RLock()
	var missing []string
	for a, t := range s.Tokens {
		if t.Supply == 0 {
			missing = append(missing, a)
		}
	}
	s.mu.RUnlock()
	if len(missing) == 0 {
		return nil
	}
	symbols, err := c.rpc.callStrings(ctx, missing, selSymbol)
	if err != nil {
		return err
	}
	names, err := c.rpc.callStrings(ctx, missing, selName)
	if err != nil {
		return err
	}
	supply, err := c.rpc.callUnits(ctx, missing, selTotalSupply)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range missing {
		t := s.Tokens[a]
		if t.Symbol == "" {
			t.Symbol, t.Name = symbols[a], names[a]
		}
		if t.Symbol == "" {
			t.Symbol = "?"
		}
		t.Supply = supply[a]
	}
	return nil
}

// refreshBurned reads each platform token's balance at the dead address.
func (c *collector) refreshBurned(ctx context.Context) error {
	addrs := make([]string, 0, len(platforms))
	for _, p := range platforms {
		addrs = append(addrs, p.Token)
	}
	burned, err := c.rpc.callUnits(ctx, addrs, callBalanceDead)
	if err != nil {
		return err
	}
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	for _, p := range platforms {
		c.state.Burned[p.Key] = burned[p.Token]
	}
	return nil
}

var blockTimes = map[uint64]int64{}

// logTime prefers Alchemy's blockTimestamp field and falls back to a header fetch.
func (c *collector) logTime(ctx context.Context, l Log) (int64, error) {
	if l.BlockTimestamp != 0 {
		return int64(l.BlockTimestamp), nil
	}
	n := uint64(l.BlockNumber)
	if ts, ok := blockTimes[n]; ok {
		return ts, nil
	}
	ts, err := c.rpc.blockTime(ctx, n)
	if err != nil {
		return 0, err
	}
	blockTimes[n] = ts
	return ts, nil
}

// applyLaunch registers new tokens and launch-fee changes. Launch events are
// handled before everything else so pool and position maps exist for the rest.
func (c *collector) applyLaunch(ctx context.Context, l Log) error {
	s := c.state
	p := platformByAddr[toLower(l.Address)]
	if p == nil || len(l.Topics) == 0 {
		return nil
	}
	data := hexBytes(l.Data)
	ts, err := c.logTime(ctx, l)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.LaunchFee[p.Key]; !ok {
		s.LaunchFee[p.Key] = p.InitialLaunchFee
	}
	var tok *Token
	switch l.Topics[0] {
	case tStkLaunchedV1, tStkLaunchedV2:
		tok = &Token{Platform: p.Key, Address: topicAddr(l.Topics[1]), Creator: topicAddr(l.Topics[2]), Quote: topicAddr(l.Topics[3]), PoolID: "0x" + hexOf(word(data, 0))}
	case tSndLaunched:
		tok = &Token{Platform: p.Key, Address: topicAddr(l.Topics[1]), Creator: topicAddr(l.Topics[2]), PoolID: toLower(l.Topics[3]),
			TokenID: wordBig(data, 0).String(), Name: wordString(data, 1), Symbol: wordString(data, 2)}
		s.TokenIDs[tok.TokenID] = tok.Address
	case tSndLaunchedV2:
		tok = &Token{Platform: p.Key, Address: topicAddr(l.Topics[2]), Creator: topicAddr(l.Topics[3]), PoolID: toLower(l.Topics[1]), Quote: wordAddr(data, 1)}
	case tStkCreationFee, tSndLaunchFee:
		s.LaunchFee[p.Key] = units(wordBig(data, 0), 18)
	case tSndLaunchFeeV2:
		s.LaunchFee[p.Key] = units(wordBig(data, 1), 18)
	}
	if tok == nil {
		return nil
	}
	if _, dup := s.Tokens[tok.Address]; dup {
		return nil
	}
	tok.Created, tok.Block = ts, uint64(l.BlockNumber)
	s.Tokens[tok.Address] = tok
	s.Pools[tok.PoolID] = tok.Address
	a := s.agg(p.Key, ts)
	a.Launches++
	a.LaunchFees += s.LaunchFee[p.Key] * s.ethPrice(ts)
	return nil
}

// applyEvent handles swaps, fee accruals, payouts and graduations.
// Caller holds s.mu.
func (c *collector) applyEvent(l Log, ts int64) {
	s := c.state
	data := hexBytes(l.Data)
	addr := toLower(l.Address)

	if addr == poolManager {
		if l.Topics[0] != tPMSwap {
			return
		}
		tok := s.Tokens[s.Pools[toLower(l.Topics[1])]]
		if tok == nil || tok.Quote == "" {
			return
		}
		amt := wordInt128(data, 1)
		if tok.QuoteIsC0 {
			amt = wordInt128(data, 0)
		}
		amt.Abs(amt)
		c.addVolume(tok, amt, ts)
		c.setPrice(tok, wordBig(data, 2), ts)
		return
	}

	p := platformByAddr[addr]
	if p == nil {
		return
	}
	switch l.Topics[0] {
	case tStkLaunchOpened: // hook: fee recipient + holder-rewards flag
		tok := s.Tokens[topicAddr(l.Topics[2])]
		if tok == nil {
			return
		}
		tok.FeeRecipient = topicAddr(l.Topics[3])
		tok.FeesToHolders = wordBool(data, 6)
		if tok.FeesToHolders {
			s.Holders[tok.FeeRecipient] = true
		}
	case tStkCredited, tStkClaimed: // escrow: per-swap fee accrual / payout
		account, currency := topicAddr(l.Topics[1]), topicAddr(l.Topics[2])
		idx := 0
		if l.Topics[0] == tStkClaimed {
			idx = 1
		}
		usd := c.quoteUSD(currency, wordBig(data, idx), ts)
		a := s.agg(p.Key, ts)
		switch {
		case l.Topics[0] == tStkClaimed:
			if account != p.PlatformRecipient && !s.Holders[account] {
				a.CreatorPaid += usd
			}
		case account == p.PlatformRecipient:
			a.PlatformUSD += usd
		case s.Holders[account]:
			a.HolderUSD += usd
		default:
			a.CreatorUSD += usd
		}
	case tSndFeesCollected: // v1 locker: LP fees split creator/protocol
		tok := s.Tokens[s.TokenIDs[wordBig(hexBytes(l.Topics[1]), 0).String()]]
		if tok == nil {
			return
		}
		a := s.agg(p.Key, ts)
		a.CreatorUSD += c.quoteUSD(tok.Quote, wordBig(data, 2), ts)
		a.PlatformUSD += c.quoteUSD(tok.Quote, wordBig(data, 3), ts)
	case tSndCreatorClaim: // v1 locker: creator payout
		tok := s.Tokens[s.TokenIDs[wordBig(hexBytes(l.Topics[1]), 0).String()]]
		if tok == nil {
			return
		}
		s.agg(p.Key, ts).CreatorPaid += c.quoteUSD(tok.Quote, wordBig(data, 1), ts)
	case tSndFeeDistrib, tSndFeesClaimed: // v2 hook/locker: immediate split + payout
		tok := s.Tokens[s.Pools[toLower(l.Topics[1])]]
		if tok == nil {
			return
		}
		a := s.agg(p.Key, ts)
		creator := c.quoteUSD(tok.Quote, wordBig(data, 1), ts)
		a.CreatorUSD += creator
		a.CreatorPaid += creator
		a.PlatformUSD += c.quoteUSD(tok.Quote, wordBig(data, 3), ts)
		a.HolderUSD += c.quoteUSD(tok.Quote, wordBig(data, 4), ts)
	case tSndGraduated:
		tok := s.Tokens[topicAddr(l.Topics[2])]
		if tok == nil || tok.Graduated != 0 {
			return
		}
		tok.Graduated = ts
		s.agg(p.Key, ts).Graduations++
	}
}

func (c *collector) addVolume(tok *Token, raw *big.Int, ts int64) {
	s := c.state
	q := s.Quotes[tok.Quote]
	dec := 18
	if q != nil {
		dec = q.Decimals
	}
	amt := units(raw, dec)
	usd, spot := s.priceQuote(tok.Quote, amt, ts)
	a := s.agg(tok.Platform, ts)
	a.Swaps++
	tok.Swaps++
	tok.VolQuote += amt
	if tok.Quote == ethAddr || tok.Quote == wethAddr {
		a.VolETH += amt
	}
	if usd == 0 {
		a.Unpriced++
		return
	}
	if spot {
		a.SpotPriced++
	}
	a.VolUSD += usd
	tok.VolUSD += usd
}

// setPrice derives the token's USD price from the pool's post-swap sqrtPriceX96.
// Launch tokens have 18 decimals on both platforms.
func (c *collector) setPrice(tok *Token, sqrtPriceX96 *big.Int, ts int64) {
	s := c.state
	dec := 18
	if q := s.Quotes[tok.Quote]; q != nil {
		dec = q.Decimals
	}
	inQuote := priceFromSqrt(sqrtPriceX96, tok.QuoteIsC0, dec)
	usd, _ := s.priceQuote(tok.Quote, inQuote, ts)
	if usd <= 0 || math.IsInf(usd, 0) || math.IsNaN(usd) {
		return
	}
	tok.PriceUSD, tok.PriceAt = usd, ts
	for _, p := range platforms {
		if p.Token == tok.Address {
			s.agg(p.Key, ts).TokenPrice = usd
		}
	}
}

// priceFromSqrt returns the token price in whole quote units. sqrtPriceX96 is
// sqrt(currency1/currency0) in raw units scaled by 2^96.
func priceFromSqrt(sqrtPriceX96 *big.Int, quoteIsC0 bool, quoteDecimals int) float64 {
	f, _ := new(big.Float).SetInt(sqrtPriceX96).Float64()
	ratio := f / (1 << 96)
	ratio *= ratio // currency1 per currency0, raw units
	if ratio == 0 {
		return 0
	}
	if quoteIsC0 {
		ratio = 1 / ratio
	}
	return ratio * math.Pow10(18-quoteDecimals)
}

func (c *collector) quoteUSD(quote string, raw *big.Int, ts int64) float64 {
	dec := 18
	if q := c.state.Quotes[quote]; q != nil {
		dec = q.Decimals
	}
	usd, _ := c.state.priceQuote(quote, units(raw, dec), ts)
	return usd
}

func hexOf(b []byte) string { return fmt.Sprintf("%x", b) }

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
