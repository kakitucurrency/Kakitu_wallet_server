# Kakitu API Documentation Site — Design Spec

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a public-facing developer documentation site at `api.kakitu.org` covering the Kakitu wallet server's HTTP API, WebSocket protocol, and M-Pesa integration.

**Architecture:** Standalone repo `kakitu-docs`, Docusaurus 3 with the `classic` preset. Scalar renders the OpenAPI spec as an interactive API reference page. Hand-written MDX pages cover WebSocket and M-Pesa. Deployed to Vercel; `api.kakitu.org` points to it via CNAME.

**Tech Stack:** Docusaurus 3, MDX, `openapi.yaml` (OpenAPI 3.1), Scalar (via `@scalar/docusaurus`), Vercel

---

## 1. Repo Structure

```
kakitu-docs/
├── docusaurus.config.ts        # Site config, navbar, footer, Scalar plugin
├── sidebars.ts                 # Sidebar navigation
├── openapi.yaml                # OpenAPI 3.1 spec for all HTTP endpoints
├── static/
│   └── img/
│       └── kakitu-logo.png     # Logo (copy from yellow-v2)
├── src/
│   └── css/
│       └── custom.css          # Brand colors (#f0b429 amber primary)
└── docs/
    ├── intro.md                # Introduction + quick-start
    ├── getting-started.md      # Auth, rate limits, base URLs, cURL example
    ├── websocket.md            # WebSocket protocol guide
    ├── mpesa.md                # M-Pesa cash-in/out flow guide
    ├── alerts.md               # GET /alerts/{lang}
    └── errors.md               # Error codes and HTTP status codes
```

---

## 2. Pages

### `docs/intro.md`
- What Kakitu is (1 KSHS = 1 KES stablecoin on a Nano-fork blockchain)
- What the API provides (wallet ops, real-time updates, M-Pesa on/off-ramp)
- Base URL: `https://walletapi.kakitu.africa`
- Link to quick-start section

### `docs/getting-started.md`
- **Base URL:** `https://walletapi.kakitu.africa`
- **Rate limiting:** 100 requests/minute per IP. Header `Authorization: <ADMIN_API_KEY>` bypasses limit.
- **CORS:** All origins allowed.
- **Content-Type:** `application/json` for all POST requests.
- **Quick example:** cURL for `account_balance`

```bash
curl -X POST https://walletapi.kakitu.africa/api \
  -H "Content-Type: application/json" \
  -d '{"action":"account_balance","account":"kshs_1xxx..."}'
```

### `docs/websocket.md`
- **Endpoint:** `wss://walletapi.kakitu.africa`
- **Connection:** Standard WebSocket upgrade
- **Keepalive:** Server pings every 54 seconds; client must pong within 60 seconds or connection closes
- **`account_subscribe` action:** full field table (account, uuid, currency, fcm_token_v2, notification_enabled)
- **Subscribe response:** full field table (balance, frontier, price, btc, pending_count, etc.)
- **`fcm_update` action:** field table
- **Real-time server events:**
  - Price update (every 60 seconds)
  - Block notification (when a send targets subscribed account)
- **Code example:** JavaScript WebSocket client connecting and subscribing

```js
const ws = new WebSocket('wss://walletapi.kakitu.africa');
ws.onopen = () => {
  ws.send(JSON.stringify({
    action: 'account_subscribe',
    account: 'kshs_1xxx...',
    currency: 'KES',
  }));
};
ws.onmessage = (e) => console.log(JSON.parse(e.data));
```

### `docs/mpesa.md`
- **Cash-in flow** (Mermaid sequence diagram):
  ```
  App → POST /mpesa/cashin → Safaricom STK Push → User approves on phone
  Safaricom → POST /mpesa/cashin/callback → Server sends KSHS from treasury → App polls balance
  ```
- **Cash-out flow** (Mermaid sequence diagram):
  ```
  App sends KSHS to treasury on-chain → App → POST /mpesa/cashout (with tx_hash)
  Safaricom B2C → User receives KES on phone → POST /mpesa/cashout/callback → confirmed
  ```
- **Phone number formats accepted:** `07XXXXXXXX`, `+2547XXXXXXXX`, `2547XXXXXXXX`
- **Amount:** KES integer, minimum 1
- **Sandbox note:** `MPESA_ENVIRONMENT=sandbox` uses Safaricom test shortcode `174379`; callbacks are simulated
- **Double-spend protection:** each `tx_hash` can only be used once for cashout

### `docs/alerts.md`
- `GET /alerts/{lang}` — returns active alert JSON for the given language code
- `GET /alerts/` — returns English alert
- Use case: in-app maintenance banners or announcements

### `docs/errors.md`
- **HTTP status codes used:** 200, 400, 409, 500
- **400 Bad Request:** invalid phone, address format, amount, or block validation failure
- **409 Conflict:** `tx_hash` already used for cashout
- **500 Internal Server Error:** upstream Daraja or Nano RPC failure
- **WebSocket disconnects:** ping timeout (60 seconds), server restart

---

## 3. `openapi.yaml` — Endpoints to Document

### `POST /api` — Nano RPC Proxy

All Nano wallet actions share one endpoint. Scalar renders each action as a separate operation using `oneOf` on the request body.

**Key actions to document explicitly** (others documented as "pass-through to Nano RPC"):

| Action | Description |
|--------|-------------|
| `account_balance` | Get balance in raw units |
| `account_history` | Paginated transaction history |
| `account_info` | Full account info (frontier, balance, representative) |
| `pending` / `receivable` | List unconfirmed incoming transactions |
| `process` | Submit a signed state block |
| `block_info` | Look up a block by hash |
| `accounts_balances` | Bulk balance lookup |

**Common parameters:**
- `action` (string, required)
- `account` (string) — `kshs_` address
- `count` (integer, default 1000, max 100000 with admin key)

**Rate limit header:** `Authorization: <ADMIN_API_KEY>`

### `GET /mpesa/config`
```yaml
responses:
  '200':
    content:
      application/json:
        schema:
          type: object
          properties:
            treasury_address:
              type: string
              example: "kshs_1treasury..."
```

### `POST /mpesa/cashin`
```yaml
requestBody:
  required: true
  content:
    application/json:
      schema:
        type: object
        required: [phone, amount_kes, kshs_address]
        properties:
          phone:
            type: string
            example: "0712345678"
          amount_kes:
            type: string
            example: "100"
          kshs_address:
            type: string
            example: "kshs_1xxx..."
responses:
  '200':
    content:
      application/json:
        schema:
          type: object
          properties:
            status:
              type: string
              example: "pending"
            checkout_request_id:
              type: string
  '400':
    description: Invalid phone, amount, or address
```

### `POST /mpesa/cashout`
```yaml
requestBody:
  required: true
  content:
    application/json:
      schema:
        type: object
        required: [phone, amount_kes, tx_hash]
        properties:
          phone:
            type: string
          amount_kes:
            type: string
          tx_hash:
            type: string
            description: "Hash of the on-chain send to the treasury address"
responses:
  '200':
    content:
      application/json:
        schema:
          type: object
          properties:
            status:
              type: string
              example: "pending"
  '400':
    description: Validation failure
  '409':
    description: tx_hash already used
```

---

## 4. Docusaurus Config

```typescript
// docusaurus.config.ts (key settings)
const config: Config = {
  title: 'Kakitu API',
  tagline: 'Developer documentation for the Kakitu wallet API',
  url: 'https://api.kakitu.org',
  baseUrl: '/',
  themeConfig: {
    navbar: {
      logo: { src: 'img/kakitu-logo.png' },
      items: [
        { to: '/docs/intro', label: 'Docs', position: 'left' },
        { to: '/api-reference', label: 'API Reference', position: 'left' },
        { href: 'https://kakitu.org', label: 'Explorer', position: 'right' },
      ],
    },
    colorMode: { defaultMode: 'dark', disableSwitch: false },
  },
  plugins: [
    [
      '@scalar/docusaurus',
      {
        label: 'API Reference',
        route: '/api-reference',
        configuration: {
          spec: { url: '/openapi.yaml' },
          theme: 'saturn',
          darkMode: true,
        },
      },
    ],
  ],
};
```

---

## 5. Deployment

- **Repo:** `github.com/kakitucurrency/kakitu-docs` (new public repo)
- **Hosting:** Vercel — connect GitHub repo, auto-deploy on push to `main`
- **Domain:** In Vercel project settings, add custom domain `api.kakitu.org`. Add a `CNAME api kakitu-docs.vercel.app` DNS record in the kakitu.org DNS zone.
- **Build command:** `npm run build`
- **Output directory:** `build`

No server-side rendering, no environment variables required. Fully static.

---

## 6. Brand

- **Primary color:** `#f0b429` (amber — matches yellow-v2 explorer)
- **Font:** System font stack (Docusaurus default)
- **Dark mode default** (matches the explorer's dark theme)
- **Logo:** Kakitu logo from `yellow-v2/src/assets/kakitu-logo.png`
