package main

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"testing"
)

// Real Sender v1 Launched log (tx 0xcbc59e75…, block 25949160).
const senderLaunchedData = "0x000000000000000000000000000000000000000000000000000000000006141f00000000000000000000000000000000000000000000000000000000000000a000000000000000000000000000000000000000000000000000000000000000e000000000000000000000000000000000000000000000000000000000000001200000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000473656e6400000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000453454e4400000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000006668747470733a2f2f68626e72387836386364676270746b392e7075626c69632e626c6f622e76657263656c2d73746f726167652e636f6d2f6d6574612f39313936316162362d326536302d343731352d623437632d6133643666663032633438332e6a736f6e0000000000000000000000000000000000000000000000000000"

func TestDecodeSenderLaunched(t *testing.T) {
	d := hexBytes(senderLaunchedData)
	if got := wordBig(d, 0).String(); got != "398367" {
		t.Fatalf("tokenId = %s", got)
	}
	if n, s := wordString(d, 1), wordString(d, 2); n != "send" || s != "SEND" {
		t.Fatalf("name/symbol = %q/%q", n, s)
	}
	if u := wordString(d, 3); u[:8] != "https://" {
		t.Fatalf("uri = %q", u)
	}
}

func TestWordInt128Negative(t *testing.T) {
	// -1 as an int128 sign-extended to 32 bytes.
	d := hexBytes("0x" + "ff" + "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	if v := wordInt128(d, 0); v.Cmp(big.NewInt(-1)) != 0 {
		t.Fatalf("got %s", v)
	}
	if v := wordInt128(hexBytes("0x"+"00000000000000000000000000000000000000000000000000000000000003e8"), 0); v.Int64() != 1000 {
		t.Fatalf("got %s", v)
	}
}

func TestUnits(t *testing.T) {
	if v := units(big.NewInt(1_500_000_000_000_000_000), 18); v != 1.5 {
		t.Fatalf("got %v", v)
	}
	if v := units(big.NewInt(12345678), 8); v != 0.12345678 {
		t.Fatalf("got %v", v)
	}
}

// Fee accounting: Stockereum Credited events must route to platform, holders or
// creators depending on the recipient; swaps bucket by hour and price by quote.
func TestApplyEventAccounting(t *testing.T) {
	s := newState("")
	c := &collector{state: s}
	s.Quotes[ethAddr] = &Quote{Symbol: "ETH", Decimals: 18, USD: 2000}
	s.EthHourly["3600"] = 2500
	tok := &Token{Platform: "stockereum", Address: "0x00000000000000000000000000000000000000aa", Quote: ethAddr, QuoteIsC0: true, PoolID: "0x01"}
	s.Tokens[tok.Address] = tok
	s.Pools["0x01"] = tok.Address
	s.Holders["0x00000000000000000000000000000000000000dd"] = true
	escrow := stockereum.Escrows[0]
	pad := func(a string) string { return "0x000000000000000000000000" + a[2:] }
	amount := "0x0000000000000000000000000000000000000000000000000de0b6b3a7640000" // 1e18

	ts := int64(3600 + 100) // inside hour 3600 → hourly price 2500
	c.applyEvent(Log{Address: escrow, Topics: []string{tStkCredited, pad(stockereum.PlatformRecipient), pad(ethAddr)}, Data: amount}, ts)
	c.applyEvent(Log{Address: escrow, Topics: []string{tStkCredited, pad("0x00000000000000000000000000000000000000dd"), pad(ethAddr)}, Data: amount}, ts)
	c.applyEvent(Log{Address: escrow, Topics: []string{tStkCredited, pad("0x00000000000000000000000000000000000000cc"), pad(ethAddr)}, Data: amount}, ts)
	// Swap: amount0 = -2 ETH (quote is currency0), amount1 positive.
	swap := "0x" + "ffffffffffffffffffffffffffffffffffffffffffffffffe43e9298b1380000" + "0000000000000000000000000000000000000000000000000000000000000001"
	c.applyEvent(Log{Address: poolManager, Topics: []string{tPMSwap, "0x01", pad(ethAddr)}, Data: swap}, ts)

	a := s.Hours[hourKey("stockereum", 3600)]
	if a == nil {
		t.Fatal("no hour bucket")
	}
	if a.PlatformUSD != 2500 || a.HolderUSD != 2500 || a.CreatorUSD != 2500 {
		t.Fatalf("fees = %+v", a)
	}
	if a.Swaps != 1 || a.VolETH != 2 || a.VolUSD != 5000 || a.SpotPriced != 0 {
		t.Fatalf("volume = %+v", a)
	}
	if tok.Swaps != 1 || tok.VolUSD != 5000 {
		t.Fatalf("token = %+v", tok)
	}
	// Outside the hourly series the latest spot ETH price is used.
	if usd, spot := s.priceQuote(ethAddr, 1, 999_999); usd != 2000 || spot {
		t.Fatalf("fallback price = %v spot=%v", usd, spot)
	}
}

func TestPriceFromSqrt(t *testing.T) {
	// sqrtPriceX96 for price 1:1 in raw units is exactly 2^96.
	one := new(big.Int).Lsh(big.NewInt(1), 96)
	if p := priceFromSqrt(one, false, 18); p != 1 {
		t.Fatalf("1:1 18-dec = %v", p)
	}
	// Token (18 dec) as currency0 vs an 8-decimal quote (WBTC): raw 1:1 means
	// 1e-18 token-units buy 1e-8 quote-units → 1e10 quote per token.
	if p := priceFromSqrt(one, false, 8); p != 1e10 {
		t.Fatalf("1:1 8-dec = %v", p)
	}
	// Quote as currency0 inverts: sqrt(4)·2^96 → ratio 4 → price 0.25.
	four := new(big.Int).Mul(one, big.NewInt(2))
	if p := priceFromSqrt(four, true, 18); p != 0.25 {
		t.Fatalf("inverted = %v", p)
	}
}

// Frontier platforms advance first; lagging ones backfill up to the next
// cursor so they merge into one group.
func TestNextRange(t *testing.T) {
	keys := func(g []*platform) (out []string) {
		for _, p := range g {
			out = append(out, p.Key)
		}
		return out
	}
	heads := map[string]uint64{"stockereum": 100, "sender": 100, "stroid": 50, "clanker": 10}
	steps := []struct {
		group       string
		from, limit uint64
		lagging     bool
		advanceTo   uint64
	}{
		{"[stockereum sender]", 101, 120, false, 120},
		{"[clanker]", 11, 50, true, 50},
		{"[stroid clanker]", 51, 120, true, 120},
	}
	for i, w := range steps {
		g, from, limit, lagging := nextRange(heads, 120)
		if got := fmt.Sprint(keys(g)); got != w.group || from != w.from || limit != w.limit || lagging != w.lagging {
			t.Fatalf("step %d: %s %d-%d lagging=%v", i, got, from, limit, lagging)
		}
		for _, p := range g {
			heads[p.Key] = w.advanceTo
		}
	}
	if g, _, _, _ := nextRange(heads, 120); g != nil {
		t.Fatalf("expected done, got %v", keys(g))
	}
}

func TestStroidAccounting(t *testing.T) {
	s := newState("")
	c := &collector{state: s}
	s.Quotes[ethAddr] = &Quote{Symbol: "ETH", Decimals: 18, USD: 2000}
	if err := c.applyLaunch(context.Background(), logStroidLaunchV3); err != nil {
		t.Fatal(err)
	}
	tok := s.Tokens["0xdd329755a6fd7595fef0a34ae18aed1e4797b62e"]
	if tok == nil || tok.Platform != "stroid" || tok.Symbol == "" || tok.Name == "" || tok.Creator != "0xdf2237114d595e0bf4d35cbcdebcdf43c55c4669" || tok.Quote != ethAddr || !tok.QuoteIsC0 || tok.PoolID != "" {
		t.Fatalf("launch = %+v", tok)
	}
	// Fee and partner events belong to another real token; register it.
	s.Tokens["0x9710ae597e3ed4fb826e51002ee3d146219a1572"] = &Token{Platform: "stroid", Address: "0x9710ae597e3ed4fb826e51002ee3d146219a1572"}
	ts := int64(1_800_000_000)
	c.applyEvent(logStroidFee, ts)
	c.applyEvent(logStroidPartner, ts)
	a := s.Hours[hourKey("stroid", hourOf(ts))]
	// V3 identity: total = creator + protocol + partner (here 25 / 50 / 25 percent).
	total := units(wordBig(hexBytes(logStroidFee.Data), 0), 18) * 2000
	if got := a.CreatorUSD + a.PlatformUSD + a.PartnerUSD; math.Abs(got-total) > 1e-9 || a.CreatorUSD == 0 || a.PartnerUSD == 0 {
		t.Fatalf("split %+v, total %v", a, total)
	}
	// A pool on the hook that the launchpad never created is not Stroid's.
	foreign := logStroidFee
	foreign.Topics = []string{tStrFeeAccrued, "0x00000000000000000000000000000000000000000000000000000000000000ff"}
	before := *a
	c.applyEvent(foreign, ts)
	if *a != before {
		t.Fatal("foreign token accounted")
	}
}

func TestClankerAccounting(t *testing.T) {
	s := newState("")
	c := &collector{state: s}
	s.Quotes[ethAddr] = &Quote{Symbol: "ETH", Decimals: 18, USD: 2000} // WETH prices as ETH
	s.Quotes[wethAddr] = &Quote{Symbol: "WETH", Decimals: 18}
	if err := c.applyLaunch(context.Background(), logClankerCreated); err != nil {
		t.Fatal(err)
	}
	tok := s.Tokens["0x90d54d77453286d30db891d729a0e18ba7081b07"]
	if tok == nil || tok.Platform != "clanker" || tok.Quote != wethAddr || tok.Symbol == "" || tok.Creator != "0x477b7fcf2879fbeecb83781509a4ef73d9d84ed4" || len(tok.PoolID) != 66 || s.Pools[tok.PoolID] != tok.Address {
		t.Fatalf("launch = %+v", tok)
	}
	// Token sorts below WETH, so WETH is currency1 and rewards1 is the WETH side.
	tok.QuoteIsC0 = false
	ts := int64(logClankerRewards.BlockTimestamp)
	c.applyEvent(logClankerRewards, ts)
	a := s.Hours[hourKey("clanker", hourOf(ts))]
	want := units(wordBig(hexBytes(logClankerRewards.Data), 1), 18) * 2000
	if a == nil || math.Abs(a.CreatorUSD-want) > 1e-12 || a.PartnerUSD != 0 {
		t.Fatalf("rewards = %+v want creator %v", a, want)
	}
}
