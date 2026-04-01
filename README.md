# Kakitu Wallet Server

The backend server that powers the Kakitu wallet application. Kakitu is a KES stablecoin (1 KSHS = 1 KES) built as a fork of the Nano protocol, targeting mobile-first payments in Kenya.

## Requirements

**Go**

Install the latest version of [Go](https://go.dev) (1.21+).

**KSHS Node with RPC enabled**

Configured by the environment variables `RPC_URL` and `NODE_WS_URL`:

```
export RPC_URL=http://localhost:7076
export NODE_WS_URL=ws://localhost:7078
```

**Redis**

```
REDIS_HOST  # default localhost
REDIS_PORT  # default 6379
REDIS_DB    # default 0
```

**PostgreSQL**

```
DB_HOST     # The host of the database
DB_PORT     # The port to connect to on the database
DB_NAME     # The name of the database
DB_USER     # The user
DB_PASS     # The password
```

Or provide a single `DATABASE_URL` connection string (takes precedence).

**Other Configuration**

```
FCM_API_KEY          # For push notifications
WORK_URL             # Work generation server URL
BPOW_KEY             # BoomPoW key (optional, for distributed proof of work)
DONATION_ACCOUNT     # kshs_ address for donation tracking (optional)
ADMIN_API_KEY        # Admin API key for elevated rate limits (optional)
RATE_LIMIT_WHITELIST # Comma-separated IPs exempt from rate limiting (optional)
```

## Building

```bash
go build -o kakitu-server
```

## Running

```bash
./kakitu-server
```

### Price Updates

To update KSHS prices (run as a cron job every 5 minutes):

```bash
./kakitu-server -kshs-price-update
```

## Docker

```bash
docker compose up
```

## Work Generation

A work generation service is required. Set one of:

- `WORK_URL` -- a work server (can be the same as `RPC_URL` or a dedicated [nano-work-server](https://github.com/nanocurrency/nano-work-server))
- `BPOW_KEY` -- a BoomPoW API key for distributed proof of work

If both are set, BoomPoW is preferred, with `WORK_URL` as fallback.

## HTTP Callback

The HTTP callback is used for push notifications. Configure in the node's `config.json`:

```json
"callback_address": "::ffff:127.0.0.1",
"callback_port": "3000",
"callback_target": "/callback"
```

The node WebSocket (`NODE_WS_URL`) is used for real-time notifications to connected wallet clients.

## License

MIT
