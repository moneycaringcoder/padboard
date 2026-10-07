package main

import (
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
