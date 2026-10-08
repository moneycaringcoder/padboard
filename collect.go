package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	chunkBlocks    = 10_000  // starting window; Alchemy never caps logs inside 10k blocks
	maxSpan        = 400_000 // widest window while backfilling sparse history
	confirmations  = 12
	poolTopicMax   = 1_500 // pool ids per Swap filter request
	backfillBudget = 3 * time.Minute
)

var toLower = strings.ToLower

type collector struct {
	rpc     *rpcClient
	prices  *priceClient
	state   *State
	ethFrom int64 // hour of the earliest platform genesis; ETH/USD history starts here
}

// sync advances every platform's cursor toward the current safe head,
// checkpointing after each window so a crash resumes cleanly. Platforms at the
// frontier always advance first so live numbers stay fresh; platforms behind
// it (newly added ones) then backfill for at most backfillBudget per call.
// more reports that backfill remains and the caller should call again now.
func (c *collector) sync(ctx context.Context) (more bool, err error) {
	start := time.Now()
	latest, err := c.rpc.blockNumber(ctx)
	if err != nil {
		return false, err
	}
	safe := latest - confirmations
	s := c.state
	s.mu.Lock()
	s.Tip = safe
	for _, p := range platforms {
		if _, ok := s.Heads[p.Key]; !ok {
			s.Heads[p.Key] = p.Genesis - 1
		}
	}
	s.mu.Unlock()
	if err := c.refreshPrices(ctx); err != nil {
		return false, fmt.Errorf("prices: %w", err)
	}
	// Sender's first v2 hook was added after its cursor had passed it.
	if err := c.replay(ctx, "0xee83560bb83fa38dcd3c1060233ddc7933c520cc", 26110133); err != nil {
		return false, fmt.Errorf("replay: %w", err)
	}
	span, lastGroup := uint64(chunkBlocks), ""
	for {
		s.mu.RLock()
		group, from, limit, lagging := nextRange(s.Heads, safe)
		s.mu.RUnlock()
		if group == nil {
			break
		}
		if lagging && time.Since(start) > backfillBudget {
			more = true // let the frontier refresh before backfilling further
			break
		}
		keys := make([]string, len(group))
		for i, p := range group {
			keys[i] = p.Key
		}
		if k := strings.Join(keys, ","); k != lastGroup {
			span, lastGroup = chunkBlocks, k
		}
		to := min(from+span-1, limit)
		t0 := time.Now()
		n, err := c.processRange(ctx, group, from, to)
		if err != nil {
			return false, fmt.Errorf("%s blocks %d-%d: %w", lastGroup, from, to, err)
		}
		s.mu.Lock()
		for _, p := range group {
			s.Heads[p.Key] = to
		}
		s.Head = max(s.Head, to)
		s.SyncedAt = time.Now().Unix()
		s.mu.Unlock()
		if err := s.save(); err != nil {
			return false, err
		}
		log.Printf("synced %s %d-%d: %d logs, %d tokens, %s", lastGroup, from, to, n, len(s.Tokens), time.Since(t0).Truncate(time.Millisecond))
		// Widen the window through sparse history, narrow it through dense.
		switch {
		case n < 5_000:
			span = min(span*2, maxSpan)
		case n > 50_000:
			span = max(span/2, chunkBlocks)
		}
	}
	if ts, err := c.rpc.blockTime(ctx, s.Head); err == nil {
		s.mu.Lock()
		s.HeadTime = ts
		s.mu.Unlock()
	}
	if err := c.refreshBurned(ctx); err != nil {
		return more, err
	}
	c.fillQuoteLogos(ctx)
	if err := c.fillImages(ctx); err != nil {
		log.Printf("images: %v", err)
	}
	return more, s.save()
}

// nextRange picks the platforms to advance next: those at the newest cursor
// while it trails the safe head, otherwise (lagging) those at the oldest
// cursor, which stop at the next cursor up so the groups merge. group is nil
// when every platform is at the safe head.
func nextRange(heads map[string]uint64, safe uint64) (group []*platform, from, limit uint64, lagging bool) {
	lo, hi := uint64(math.MaxUint64), uint64(0)
	for _, p := range platforms {
		lo, hi = min(lo, heads[p.Key]), max(hi, heads[p.Key])
	}
	target := hi
	if hi >= safe {
		target = lo
	}
	if target >= safe {
		return nil, 0, 0, false
	}
	limit = safe
	for _, p := range platforms {
		switch h := heads[p.Key]; {
		case h == target:
			group = append(group, p)
		case h > target:
			limit = min(limit, h)
		}
	}
	return group, target + 1, limit, target != hi
}

// replay applies one contract's past events once: for a contract added to a
// platform after that platform's cursor had already passed `from`.
func (c *collector) replay(ctx context.Context, addr string, from uint64) error {
	s := c.state
	s.mu.RLock()
	done, to := s.Done[addr], s.Heads[platformByAddr[addr].Key]
	s.mu.RUnlock()
	if done {
		return nil
	}
	if to >= from {
		logs, err := c.rpc.getLogs(ctx, logFilter{FromBlock: hexQty(from), ToBlock: hexQty(to), Address: []string{addr}})
		if err != nil {
			return err
		}
		sortLogs(logs)
		s.mu.Lock()
		for _, l := range logs {
			ts, err := c.logTime(ctx, l)
			if err != nil {
				s.mu.Unlock()
				return err
			}
			c.applyEvent(l, ts)
		}
		s.mu.Unlock()
		log.Printf("replayed %d logs from %s (blocks %d-%d)", len(logs), addr, from, to)
	}
	s.mu.Lock()
	s.Done[addr] = true
	s.mu.Unlock()
	return s.save()
}

func sortLogs(ls []Log) {
	sort.Slice(ls, func(i, j int) bool {
		if ls[i].BlockNumber != ls[j].BlockNumber {
			return ls[i].BlockNumber < ls[j].BlockNumber
		}
		return ls[i].LogIndex < ls[j].LogIndex
	})
}

// refreshPrices updates spot prices for every known quote and extends the
// hourly ETH series up to the present.
func (c *collector) refreshPrices(ctx context.Context) error {
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
	// ETH hourly: back to the earliest platform genesis once, then forward to now.
	if c.ethFrom == 0 {
		g := platforms[0].Genesis
		for _, p := range platforms {
			g = min(g, p.Genesis)
		}
		ts, err := c.rpc.blockTime(ctx, g)
		if err != nil {
			return err
		}
		c.ethFrom = hourOf(ts)
	}
	var lo, hi int64
	s.mu.RLock()
	for k := range s.EthHourly {
		h, _ := strconv.ParseInt(k, 10, 64)
		if lo == 0 || h < lo {
			lo = h
		}
		hi = max(hi, h)
	}
	back := lo == 0 || (lo > c.ethFrom && !s.Done["eth-history"])
	s.mu.RUnlock()
	type span struct{ from, to int64 }
	var spans []span
	switch {
	case hi == 0:
		spans = append(spans, span{c.ethFrom, time.Now().Unix()})
	default:
		if back {
			spans = append(spans, span{c.ethFrom, lo})
		}
		if time.Since(time.Unix(hi, 0)) >= 30*time.Minute {
			spans = append(spans, span{hi, time.Now().Unix()})
		}
	}
	for _, sp := range spans {
		series, err := c.prices.ethHourly(ctx, time.Unix(sp.from, 0), time.Unix(sp.to, 0))
		if err != nil {
			return err
		}
		s.mu.Lock()
		for k, v := range series {
			s.EthHourly[k] = v
		}
		s.mu.Unlock()
	}
	if back {
		s.mu.Lock()
		s.Done["eth-history"] = true // the API may simply have nothing older
		s.mu.Unlock()
	}
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

// processRange fetches and applies every log of the given platforms in [from, to].
func (c *collector) processRange(ctx context.Context, group []*platform, from, to uint64) (int, error) {
	s := c.state
	f := logFilter{FromBlock: hexQty(from), ToBlock: hexQty(to)}

	// 1. Platform contracts: launches, fee events, graduations.
	f.Address = contractsOf(group)
	plat, err := c.rpc.getLogs(ctx, f)
	if err != nil {
		return 0, err
	}
	for _, l := range plat {
		if err := c.applyLaunch(ctx, l); err != nil {
			return 0, err
		}
	}

	// 2. Bind launches to their v4 pools via PoolManager Initialize (gives the
	// quote side). Most launch events name the pool id; Stroid's name only the
	// token, which is currency1 against native ETH.
	var ids, toks []string
	for _, l := range plat {
		switch l.Topics[0] {
		case tStkLaunchedV1, tStkLaunchedV2:
			ids = append(ids, "0x"+hexOf(word(hexBytes(l.Data), 0)))
		case tClkCreated:
			ids = append(ids, "0x"+hexOf(word(hexBytes(l.Data), 8)))
		case tSndLaunched:
			ids = append(ids, toLower(l.Topics[3]))
		case tSndLaunchedV2:
			ids = append(ids, toLower(l.Topics[1]))
		case tStrLaunchedV1, tStrLaunchedV2, tStrLaunchedV3:
			toks = append(toks, toLower(l.Topics[1]))
		}
	}
	pm := logFilter{FromBlock: f.FromBlock, ToBlock: f.ToBlock, Address: []string{poolManager}}
	byID, err := c.logsByTopic(ctx, pm, tPMInitialize, 1, ids)
	if err != nil {
		return 0, err
	}
	byToken, err := c.logsByTopic(ctx, pm, tPMInitialize, 3, toks)
	if err != nil {
		return 0, err
	}
	var newQuotes []string
	s.mu.Lock()
	for _, l := range append(byID, byToken...) {
		id := toLower(l.Topics[1])
		tok := s.Tokens[s.Pools[id]]
		if tok == nil {
			// Anyone can open another pool for a token; take the one on the platform's own hook.
			t := s.Tokens[topicAddr(l.Topics[3])]
			if p := platformByAddr[wordAddr(hexBytes(l.Data), 2)]; t == nil || t.PoolID != "" || p == nil || p.Key != t.Platform {
				continue
			}
			tok = t
			tok.PoolID = id
			s.Pools[id] = t.Address
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

	// 3. Swaps on every pool of these platforms.
	in := map[string]bool{}
	for _, p := range group {
		in[p.Key] = true
	}
	s.mu.RLock()
	var pools []string
	for id, addr := range s.Pools {
		if t := s.Tokens[addr]; t != nil && in[t.Platform] {
			pools = append(pools, id)
		}
	}
	s.mu.RUnlock()
	swaps, err := c.logsByTopic(ctx, pm, tPMSwap, 1, pools)
	if err != nil {
		return 0, err
	}

	// 4. Apply fee/graduation/swap logs in chain order.
	rest := append(swaps, plat...)
	sortLogs(rest)
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

// logsByTopic fetches logs matching f with topic0 and any of values at topic
// position pos, poolTopicMax values per request, requests in parallel.
func (c *collector) logsByTopic(ctx context.Context, f logFilter, topic0 string, pos int, values []string) ([]Log, error) {
	parts := make([][]Log, (len(values)+poolTopicMax-1)/poolTopicMax)
	errs := make([]error, len(parts))
	var wg sync.WaitGroup
	for k := range parts {
		g := f
		g.Topics = make([]any, pos+1)
		g.Topics[0], g.Topics[pos] = topic0, values[k*poolTopicMax:min((k+1)*poolTopicMax, len(values))]
		wg.Go(func() { parts[k], errs[k] = c.rpc.getLogs(ctx, g) })
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	var out []Log
	for _, ls := range parts {
		out = append(out, ls...)
	}
	return out, nil
}

// fillMetadata fetches supply for new tokens, and name/symbol for those whose
// launch event did not carry them.
func (c *collector) fillMetadata(ctx context.Context) error {
	s := c.state
	s.mu.RLock()
	var missing, unnamed []string
	for a, t := range s.Tokens {
		if t.Supply == 0 {
			missing = append(missing, a)
			if t.Symbol == "" {
				unnamed = append(unnamed, a)
			}
		}
	}
	s.mu.RUnlock()
	if len(missing) == 0 {
		return nil
	}
	symbols, err := c.rpc.callStrings(ctx, unnamed, selSymbol)
	if err != nil {
		return err
	}
	names, err := c.rpc.callStrings(ctx, unnamed, selName)
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
	var addrs []string
	for _, p := range platforms {
		if p.Token != "" {
			addrs = append(addrs, p.Token)
		}
	}
	burned, err := c.rpc.callUnits(ctx, addrs, callBalanceDead)
	if err != nil {
		return err
	}
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	for _, p := range platforms {
		if p.Token != "" {
			c.state.Burned[p.Key] = burned[p.Token]
		}
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
	case tStrLaunchedV1, tStrLaunchedV2: // pool is bound from PoolManager Initialize
		tok = &Token{Platform: p.Key, Address: topicAddr(l.Topics[1]), Creator: topicAddr(l.Topics[2]), Quote: ethAddr, QuoteIsC0: true,
			Name: wordString(data, 0), Symbol: wordString(data, 1), MetaURI: wordString(data, 2)}
	case tStrLaunchedV3: // word 0 is the partner
		tok = &Token{Platform: p.Key, Address: topicAddr(l.Topics[1]), Creator: topicAddr(l.Topics[2]), Quote: ethAddr, QuoteIsC0: true,
			Name: wordString(data, 1), Symbol: wordString(data, 2), MetaURI: wordString(data, 3)}
	case tClkCreated:
		tok = &Token{Platform: p.Key, Address: topicAddr(l.Topics[1]), Creator: topicAddr(l.Topics[2]), PoolID: "0x" + hexOf(word(data, 8)), Quote: wordAddr(data, 9),
			Name: wordString(data, 2), Symbol: wordString(data, 3)}
		if img := gatewayURL(strings.TrimSpace(wordString(data, 1))); strings.HasPrefix(img, "https://") {
			tok.Image = img
		}
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
	if tok.PoolID != "" {
		s.Pools[tok.PoolID] = tok.Address
	}
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
	case tStrFeeAccrued: // Stroid hook: per-swap fee in ETH (total, creator, protocol)
		if t := s.Tokens[topicAddr(l.Topics[1])]; t == nil || t.Platform != p.Key {
			return // a pool on the hook that the launchpad did not create
		}
		a := s.agg(p.Key, ts)
		a.CreatorUSD += c.quoteUSD(ethAddr, wordBig(data, 1), ts)
		a.PlatformUSD += c.quoteUSD(ethAddr, wordBig(data, 2), ts)
	case tStrPartnerFee, tStrModuleFee: // Stroid V3: cuts taken from the protocol share
		if t := s.Tokens[topicAddr(l.Topics[1])]; t == nil || t.Platform != p.Key {
			return
		}
		s.agg(p.Key, ts).PartnerUSD += c.quoteUSD(ethAddr, wordBig(data, 0), ts)
	case tStrClaimed:
		if topicAddr(l.Topics[1]) != p.PlatformRecipient {
			// ponytail: partner claims count as creator payouts too; split if partners grow.
			s.agg(p.Key, ts).CreatorPaid += c.quoteUSD(ethAddr, wordBig(data, 0), ts)
		}
	case tClkRewards: // LP locker: fees collected for the launch's reward recipients
		tok := s.Tokens[topicAddr(l.Topics[1])]
		if tok == nil {
			return
		}
		c0, c1 := tok.Address, tok.Quote
		if tok.QuoteIsC0 {
			c0, c1 = c1, c0
		}
		r0, r1 := wordUints(data, 2), wordUints(data, 3)
		a := s.agg(p.Key, ts)
		for i := range max(len(r0), len(r1)) {
			usd := 0.0
			if i < len(r0) {
				usd += c.assetUSD(c0, r0[i], ts)
			}
			if i < len(r1) {
				usd += c.assetUSD(c1, r1[i], ts)
			}
			if i == 0 {
				a.CreatorUSD += usd
			} else {
				a.PartnerUSD += usd
			}
		}
	case tClkProtocolFees: // hook: Clanker's protocol cut, moved out when rewards are collected
		s.agg(p.Key, ts).PlatformUSD += c.assetUSD(topicAddr(l.Topics[1]), wordBig(data, 0), ts)
	case tClkClaimTokens: // fee locker: a reward recipient withdraws
		// ponytail: partner withdrawals count as creator payouts too; split by owner if partners grow.
		s.agg(p.Key, ts).CreatorPaid += c.assetUSD(topicAddr(l.Topics[2]), wordBig(data, 0), ts)
	}
}

// assetUSD prices a raw amount of a quote asset or of a launched token (at its
// last observed price; launched tokens have 18 decimals). Caller holds s.mu.
func (c *collector) assetUSD(asset string, raw *big.Int, ts int64) float64 {
	if t := c.state.Tokens[asset]; t != nil && c.state.Quotes[asset] == nil {
		return units(raw, 18) * t.PriceUSD
	}
	return c.quoteUSD(asset, raw, ts)
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
