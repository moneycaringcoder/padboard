package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// hexUint decodes JSON-RPC quantities ("0x1a").
type hexUint uint64

func (h *hexUint) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	v, err := strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 64)
	if err != nil {
		return err
	}
	*h = hexUint(v)
	return nil
}

func hexQty(v uint64) string { return "0x" + strconv.FormatUint(v, 16) }

// Log is an eth_getLogs entry. BlockTimestamp is an Alchemy extension.
type Log struct {
	Address        string   `json:"address"`
	Topics         []string `json:"topics"`
	Data           string   `json:"data"`
	BlockNumber    hexUint  `json:"blockNumber"`
	LogIndex       hexUint  `json:"logIndex"`
	BlockTimestamp hexUint  `json:"blockTimestamp"`
	TxHash         string   `json:"transactionHash"`
	Removed        bool     `json:"removed"`
}

type rpcClient struct {
	url  string
	http *http.Client
}

type rpcReq struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type rpcResp struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (e *rpcResp) err() error {
	if e.Error == nil {
		return nil
	}
	return fmt.Errorf("rpc %d: %s", e.Error.Code, e.Error.Message)
}

// post sends a JSON body with bounded retries on 429/5xx/network errors.
func (c *rpcClient) post(ctx context.Context, body []byte) ([]byte, error) {
	var lastErr error
	for attempt := range 6 {
		if attempt > 0 {
			d := time.Duration(math.Pow(2, float64(attempt))) * 250 * time.Millisecond
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(d):
			}
		}
		req, err := http.NewRequestWithContext(ctx, "POST", c.url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		out, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("http %d: %.200s", resp.StatusCode, out)
			continue
		}
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("http %d: %.200s", resp.StatusCode, out)
		}
		return out, nil
	}
	return nil, lastErr
}

func (c *rpcClient) call(ctx context.Context, method string, params any, out any) error {
	body, _ := json.Marshal(rpcReq{"2.0", 1, method, params})
	raw, err := c.post(ctx, body)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	var r rpcResp
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if err := r.err(); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(r.Result, out)
}

// batch sends several requests in one HTTP round trip; results align with reqs.
func (c *rpcClient) batch(ctx context.Context, reqs []rpcReq) ([]json.RawMessage, error) {
	for i := range reqs {
		reqs[i].JSONRPC, reqs[i].ID = "2.0", i
	}
	body, _ := json.Marshal(reqs)
	raw, err := c.post(ctx, body)
	if err != nil {
		return nil, err
	}
	var rs []rpcResp
	if err := json.Unmarshal(raw, &rs); err != nil {
		return nil, fmt.Errorf("batch: %w", err)
	}
	out := make([]json.RawMessage, len(reqs))
	for _, r := range rs {
		if r.ID < 0 || r.ID >= len(out) {
			continue
		}
		if r.Error == nil {
			out[r.ID] = r.Result
		}
	}
	return out, nil
}

func (c *rpcClient) blockNumber(ctx context.Context) (uint64, error) {
	var h hexUint
	err := c.call(ctx, "eth_blockNumber", []any{}, &h)
	return uint64(h), err
}

func (c *rpcClient) blockTime(ctx context.Context, n uint64) (int64, error) {
	var b struct {
		Timestamp hexUint `json:"timestamp"`
	}
	if err := c.call(ctx, "eth_getBlockByNumber", []any{hexQty(n), false}, &b); err != nil {
		return 0, err
	}
	return int64(b.Timestamp), nil
}

type logFilter struct {
	FromBlock string   `json:"fromBlock"`
	ToBlock   string   `json:"toBlock"`
	Address   []string `json:"address,omitempty"`
	Topics    []any    `json:"topics,omitempty"`
}

// getLogs fetches one filter. Alchemy caps responses at 10k logs for ranges
// over 10k blocks; such ranges are halved until each part fits.
func (c *rpcClient) getLogs(ctx context.Context, f logFilter) ([]Log, error) {
	var logs []Log
	err := c.call(ctx, "eth_getLogs", []any{f}, &logs)
	if err == nil || !strings.Contains(err.Error(), "response size exceeded") {
		return logs, err
	}
	from, _ := strconv.ParseUint(strings.TrimPrefix(f.FromBlock, "0x"), 16, 64)
	to, _ := strconv.ParseUint(strings.TrimPrefix(f.ToBlock, "0x"), 16, 64)
	if to <= from {
		return nil, err
	}
	a, b := f, f
	a.ToBlock, b.FromBlock = hexQty(from+(to-from)/2), hexQty(from+(to-from)/2+1)
	la, err := c.getLogs(ctx, a)
	if err != nil {
		return nil, err
	}
	lb, err := c.getLogs(ctx, b)
	return append(la, lb...), err
}

// callMany runs eth_call with the same calldata against many addresses and
// returns the raw hex result per address (missing on error).
func (c *rpcClient) callMany(ctx context.Context, addrs []string, calldata string) (map[string]string, error) {
	out := make(map[string]string, len(addrs))
	for i := 0; i < len(addrs); i += 50 {
		end := min(i+50, len(addrs))
		reqs := make([]rpcReq, 0, end-i)
		for _, a := range addrs[i:end] {
			reqs = append(reqs, rpcReq{Method: "eth_call", Params: []any{map[string]string{"to": a, "data": calldata}, "latest"}})
		}
		res, err := c.batch(ctx, reqs)
		if err != nil {
			return nil, err
		}
		for j, r := range res {
			var s string
			if r == nil || json.Unmarshal(r, &s) != nil {
				continue
			}
			out[addrs[i+j]] = s
		}
	}
	return out, nil
}

// callStrings decodes callMany results as ABI strings (or bytes32 fallback).
func (c *rpcClient) callStrings(ctx context.Context, addrs []string, selector string) (map[string]string, error) {
	raw, err := c.callMany(ctx, addrs, selector)
	if err != nil {
		return nil, err
	}
	for a, s := range raw {
		raw[a] = decodeStringResult(s)
	}
	return raw, nil
}

// callUnits decodes callMany results as 18-decimal token amounts.
func (c *rpcClient) callUnits(ctx context.Context, addrs []string, calldata string) (map[string]float64, error) {
	raw, err := c.callMany(ctx, addrs, calldata)
	if err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(raw))
	for a, s := range raw {
		if b := hexBytes(s); len(b) >= 32 {
			out[a] = units(wordBig(b, 0), 18)
		}
	}
	return out, nil
}

func (c *rpcClient) callUint(ctx context.Context, addr, selector string) (uint64, error) {
	var s string
	if err := c.call(ctx, "eth_call", []any{map[string]string{"to": addr, "data": selector}, "latest"}, &s); err != nil {
		return 0, err
	}
	b := hexBytes(s)
	if len(b) < 32 {
		return 0, errors.New("short eth_call result")
	}
	return wordBig(b, 0).Uint64(), nil
}

// ---- ABI decoding helpers ----

func hexBytes(s string) []byte {
	b, _ := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	return b
}

func word(data []byte, i int) []byte {
	if len(data) < (i+1)*32 {
		return make([]byte, 32)
	}
	return data[i*32 : (i+1)*32]
}

func wordBig(data []byte, i int) *big.Int { return new(big.Int).SetBytes(word(data, i)) }

func wordBool(data []byte, i int) bool { return word(data, i)[31] != 0 }

func wordAddr(data []byte, i int) string { return "0x" + hex.EncodeToString(word(data, i)[12:]) }

func topicAddr(t string) string {
	if len(t) < 66 {
		return ""
	}
	return "0x" + strings.ToLower(t[26:])
}

// wordInt128 decodes a sign-extended int128 stored in a 32-byte word.
func wordInt128(data []byte, i int) *big.Int {
	v := wordBig(data, i)
	if v.Bit(255) == 1 {
		v.Sub(v, new(big.Int).Lsh(big.NewInt(1), 256))
	}
	return v
}

// wordString decodes a dynamic string whose offset is in word i.
func wordString(data []byte, i int) string {
	off := wordBig(data, i)
	if !off.IsInt64() || off.Int64()+32 > int64(len(data)) {
		return ""
	}
	o := int(off.Int64())
	n := new(big.Int).SetBytes(data[o : o+32])
	if !n.IsInt64() || int64(o+32)+n.Int64() > int64(len(data)) {
		return ""
	}
	return string(data[o+32 : o+32+int(n.Int64())])
}

// wordUints decodes a dynamic uint256[] whose offset is in word i.
func wordUints(data []byte, i int) []*big.Int {
	off := wordBig(data, i)
	if !off.IsInt64() || off.Int64()+32 > int64(len(data)) {
		return nil
	}
	o := int(off.Int64())
	n := new(big.Int).SetBytes(data[o : o+32])
	if !n.IsInt64() || int64(o+32)+32*n.Int64() > int64(len(data)) {
		return nil
	}
	out := make([]*big.Int, n.Int64())
	for k := range out {
		out[k] = new(big.Int).SetBytes(data[o+32+32*k : o+64+32*k])
	}
	return out
}

// decodeStringResult handles both ABI strings and legacy bytes32 symbols.
func decodeStringResult(s string) string {
	b := hexBytes(s)
	if len(b) == 32 {
		return strings.TrimRight(string(b), "\x00")
	}
	return strings.ToValidUTF8(wordString(b, 0), "")
}

// units converts a raw token amount to a float in whole units.
func units(v *big.Int, decimals int) float64 {
	f := new(big.Float).SetInt(v)
	f.Quo(f, new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)))
	out, _ := f.Float64()
	return out
}
