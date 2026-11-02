# Mock Inventory Feed

Pretends to be a dealer's inventory system. It posts random vehicle batches to the [KL_Test](../KL_Test) API through `POST /v1/ingest/vehicles`. It uses only the Go standard library.

## Run

Start the API first, then the feed:

```bash
(cd ../KL_Test && docker compose up -d)
go run .
```

Stop it with Ctrl+C. By default it sends a batch of 1 to 20 vehicles every 30 seconds.

**It writes to whichever database that API uses**, so a long run keeps adding vehicles to it.

## Options

| Flag | Default | Purpose |
|---|---|---|
| `-url` | `http://localhost:8080` (or `API_URL`) | API base URL |
| `-interval` | `30s` | Time between batches |
| `-batch` | `20` | Largest batch size, 1 to 1000 |
| `-count` | `0` | Batches to send; `0` runs until stopped |
| `-seed` | random | The same seed repeats the same VINs |
| `-dry-run` | off | Print batches instead of sending them |

```bash
go run . -dry-run -count 1           # see what a batch looks like
go run . -count 5 -interval 1s       # send five batches quickly, then exit
go run . -interval 5s -batch 100     # a busier feed
```

## Authentication

The feed needs a token with the `ingest` role. It looks for one in this order:

1. `INGEST_TOKEN`: a ready-made token, used as is. It stops working when it expires.
2. `JWT_SECRET`: mints a 5-minute token for each batch. This must match the API's secret.
3. The default secret in KL_Test's `docker-compose.yml`, so a local stack works with no setup.

## What a batch contains

Once earlier vehicles exist, each item in a batch is one of:

| Share | Item | API counts it as |
|---|---|---|
| 60% | New vehicle | `inserted` |
| 30% | Price change on a vehicle the feed sent earlier | `updated` |
| 10% | A vehicle the feed sent earlier marked `SOLD` or, less often, `REMOVED` | `updated` |

- **New vehicles:** 80% were received in the last week and 20% are 91 to 180 days old, so aging figures move too.
- **Memory:** the feed only remembers vehicles the API accepted, and only until it exits. It never changes vehicles from other sources, such as the seed data.
- **Status:** it is always sent, because the API sets a vehicle to `IN_STOCK` when it is left out.

## Failures

| Response | What the feed does |
|---|---|
| 401 or 403 | Stops with an error, because retrying cannot help |
| Anything else, or API unreachable | Logs a warning, drops the batch and tries again at the next interval |

With `-count`, the feed exits with status 1 if any batch failed.

## Removing mock data

Every mock VIN starts with `FEED`. If nobody has logged an action on those vehicles, this removes them from KL_Test's compose database:

```bash
psql postgres://inventory:inventory@localhost:5433/inventory \
  -c "DELETE FROM vehicles WHERE vin LIKE 'FEED%'"
```

If actions exist, the foreign key makes that fail with an error. Delete those vehicles' rows from `vehicle_actions` and `idempotency_keys` first, or reset everything with `make seed` in KL_Test.

## Test

```bash
go test -race ./...
```
