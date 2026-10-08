# Padboard

Launchpads, side by side.

[padboard.app](https://padboard.app)

A dashboard comparing launchpad activity, trading volume, and fee distribution
on Ethereum mainnet: [Stockereum](https://stockereum.com),
[Sender](https://sender.family), [Stroid](https://stroid.fun) and
[Clanker](https://clanker.world).

Everything is read from chain logs (factory launches, Uniswap v4 pool swaps,
each platform's fee, hook and locker contracts) and aggregated into an hourly
snapshot. Nothing is scraped from the platforms' websites. Each platform has
its own sync cursor: a newly added one backfills from its first launch while
the others keep syncing every interval.

## Run

Requires Go 1.27 and an Alchemy API key for Ethereum mainnet (JSON-RPC and
the Prices API are used).

```sh
echo "ALCHEMY_API_KEY=..." > .env
go run .            # first run backfills every platform from its first launch (a few minutes), then serves on :8080
```

Environment:

| Variable        | Default | Purpose                                          |
| --------------- | ------- | ------------------------------------------------ |
| `ALCHEMY_API_KEY` | —     | required                                         |
| `PORT`          | `8080`  | HTTP listen port                                 |
| `DATA_DIR`      | `data`  | where `state.json` (the snapshot) is written     |
| `SYNC_INTERVAL` | `5m`    | how often new blocks are pulled                  |
| `SYNC_ONCE`     | unset   | if set, exit after one sync once backfill is done (for cron-style use) |

Pages: `/` overview (every launchpad ranked for 24h/7d/30d/all with a dominance
donut, a card per launchpad, one stacked chart comparing them all, and detail
cards with a launchpad picker; refreshes itself every minute while visible),
`/tokens` (every launch, paged by 100), `/p/{launchpad}` (one launchpad: windows,
activity chart, trading, fees, platform token, top coins), `/t/{address}` (one
token), `/api` (docs). The dock's search (`/` key) finds tokens and launchpads
via `/search?q=`. JSON: `/api/v1/summary`,
`/api/v1/daily?interval=day|hour`, `/api/v1/tokens?pad&sort&q&page`,
`/api/v1/tokens/{address}`, `/api/snapshot.json`; free, 60 req/min per IP.
`/healthz` for probes. Pages, JSON and static files are rendered once per sync
and minute, served gzipped with content-hash ETags (304 when unchanged).

## Development

```sh
gofmt -l . && go vet ./... && go test ./...
```

Early development.
