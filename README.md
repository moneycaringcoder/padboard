# Padboard

Launchpads, side by side.

[padboard.app](https://padboard.app)

A single-page dashboard comparing launchpad activity, trading volume, and fee
distribution on Ethereum mainnet, starting with
[Stockereum](https://stockereum.com) and [Sender](https://sender.family).

Everything is read from chain logs (factory launches, Uniswap v4 pool swaps,
each platform's fee and locker contracts) and aggregated into an hourly
snapshot. Nothing is scraped from the platforms' websites.

## Run

Requires Go 1.27 and an Alchemy API key for Ethereum mainnet (JSON-RPC and
the Prices API are used).

```sh
echo "ALCHEMY_API_KEY=..." > .env
go run .            # first run backfills ~35 days of logs (a few minutes), then serves on :8080
```

Environment:

| Variable        | Default | Purpose                                          |
| --------------- | ------- | ------------------------------------------------ |
| `ALCHEMY_API_KEY` | —     | required                                         |
| `PORT`          | `8080`  | HTTP listen port                                 |
| `DATA_DIR`      | `data`  | where `state.json` (the snapshot) is written     |
| `SYNC_INTERVAL` | `1h`    | how often new blocks are pulled                  |
| `SYNC_ONCE`     | unset   | if set, exit after one sync (for cron-style use) |

Pages: `/` overview, `/charts` (both pads overlaid, auto-refreshing), `/tokens`
(every launch, paged by 100), `/api` (docs). JSON: `/api/v1/summary`,
`/api/v1/daily?interval=day|hour`, `/api/v1/tokens?pad&sort&q&page`,
`/api/v1/tokens/{address}`, `/api/snapshot.json`; free, 60 req/min per IP.
`/healthz` for probes.

## Development

```sh
gofmt -l . && go vet ./... && go test ./...
```

Early development.
