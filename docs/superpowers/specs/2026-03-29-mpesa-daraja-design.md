# M-Pesa Daraja Integration Design

**Date:** 2026-03-29
**Repo:** kakitu_wallet_server
**Status:** Approved

---

## Overview

Add M-Pesa Daraja API support to the kakitu_wallet_server so users can:
- **Cash in** — pay KES via M-Pesa STK Push → receive equivalent KSHS tokens in their wallet (1 KES = 1 KSHS)
- **Cash out** — send KSHS to the treasury wallet on-chain → receive KES via M-Pesa B2C

No phone numbers are stored. The phone is used only in-flight to call Daraja APIs.

---

## Architecture

### New packages and files

```
mpesa/
  auth.go        — Daraja OAuth token fetch + Redis cache
  stk.go         — STK Push initiation (C2B)
  b2c.go         — B2C transfer (cash-out)
  models.go      — Daraja request/response structs

controller/
  mpesa_c.go     — HTTP handlers for all M-Pesa routes

models/dbmodels/
  mpesa_txns.go  — mpesa_transactions table model + GORM migration
```

### New routes (mounted in main.go)

| Method | Path | Purpose |
|--------|------|---------|
| `POST` | `/mpesa/cashin` | Initiate STK Push |
| `POST` | `/mpesa/cashin/callback` | Safaricom C2B IPN |
| `POST` | `/mpesa/cashout` | Verify on-chain tx + trigger B2C |
| `POST` | `/mpesa/cashout/callback` | Safaricom B2C result IPN |

---

## Data Model

### `mpesa_transactions` (Postgres, via GORM)

| Column | Type | Description |
|--------|------|-------------|
| `id` | uuid (PK) | Auto-generated |
| `type` | varchar(10) | `"cashin"` or `"cashout"` |
| `amount_kes` | decimal(10,2) | Amount in KES |
| `kshs_address` | varchar(65) | Destination (cashin) or source (cashout) |
| `merchant_req_id` | varchar(100) | Daraja CheckoutRequestID (cashin) or ConversationID (cashout) |
| `mpesa_receipt` | varchar(50) | M-Pesa receipt number (on success) |
| `tx_hash` | varchar(64) | On-chain block hash (cashout only) |
| `status` | varchar(20) | `pending`, `confirmed`, `failed` |
| `created_at` | timestamp | Auto |
| `updated_at` | timestamp | Auto |

### Redis

- `mpesa:oauth_token` — cached Daraja OAuth token, TTL = `expires_in - 60` seconds

---

## Cash-In Flow (C2B STK Push)

```
Flutter app
  │
  ├─ POST /mpesa/cashin  {phone, amount_kes, kshs_address}
  │
  └─► Server
        1. Validate kshs_address format (kshs_ prefix)
        2. Get Daraja OAuth token (Redis cache or fresh fetch)
        3. Call Daraja STK Push API → get CheckoutRequestID
        4. Save mpesa_transactions {type=cashin, amount_kes,
           kshs_address, merchant_req_id=CheckoutRequestID, status=pending}
        5. Return {success: true, checkout_request_id} to app
        │
        └─► Safaricom sends STK Push prompt to user's phone
              │
              └─► User enters M-Pesa PIN
                    │
                    └─► POST /mpesa/cashin/callback  (Safaricom IPN)
                          1. Verify ResultCode == 0 (success)
                          2. Look up transaction by CheckoutRequestID
                          3. Send KSHS from treasury to kshs_address
                             via RPCClient (process block, signed with TREASURY_SEED)
                          4. Update transaction status → confirmed
                             store mpesa_receipt
                          5. Return 200 OK to Safaricom
```

---

## Cash-Out Flow (B2C)

```
Flutter app
  │
  ├─ [User sends KSHS to TREASURY_ADDRESS on-chain]
  │
  ├─ POST /mpesa/cashout  {phone, amount_kes, tx_hash}
  │
  └─► Server
        1. Validate tx_hash exists on-chain via RPCClient block_info
        2. Verify block destination == TREASURY_ADDRESS env var
        3. Verify block amount matches amount_kes (raw KSHS = amount_kes × 10^30)
        4. Check tx_hash not already in mpesa_transactions (double-spend guard)
        5. Get Daraja OAuth token (Redis cache or fresh fetch)
        6. Call Daraja B2C API → get ConversationID
        7. Save mpesa_transactions {type=cashout, amount_kes,
           tx_hash, merchant_req_id=ConversationID, status=pending}
        8. Return {success: true} to app
        │
        └─► Safaricom processes B2C transfer to user's phone
              │
              └─► POST /mpesa/cashout/callback  (Safaricom result IPN)
                    1. Verify ResultCode == 0 (success)
                    2. Look up transaction by ConversationID
                    3. Update status → confirmed or failed
                    4. Return 200 OK to Safaricom
```

---

## Treasury Wallet

- Separate `kshs_` address funded from the genesis wallet when depleted
- Managed via `TREASURY_SEED` env var — server derives address and signs send blocks
- Used only for cash-in outgoing transfers (sending KSHS to users)
- `TREASURY_ADDRESS` env var holds the public address (used for cash-out verification)

---

## Daraja Client (`mpesa/`)

### auth.go
- `GetToken() (string, error)` — checks Redis for `mpesa:oauth_token`, fetches fresh if missing
- Caches with TTL = `expires_in - 60` seconds

### stk.go
- `InitiateSTKPush(token, phone, amount, callbackURL) (checkoutRequestID string, error)`
- Builds Daraja STK Push request with timestamp + base64 password
- Sandbox shortcode: `174379`, passkey from env

### b2c.go
- `InitiateB2C(token, phone, amount, callbackURL) (conversationID string, error)`
- Uses initiator name + security credential from env

### models.go
- All Daraja request/response structs (STKPushRequest, STKPushResponse, B2CRequest, B2CResponse, CallbackBody, etc.)

---

## Environment Variables

```
MPESA_CONSUMER_KEY        — Daraja app consumer key
MPESA_CONSUMER_SECRET     — Daraja app consumer secret
MPESA_SHORTCODE           — Till/Paybill number
MPESA_PASSKEY             — STK Push passkey
MPESA_B2C_INITIATOR       — B2C initiator name
MPESA_B2C_SECURITY_CRED   — B2C encrypted security credential
MPESA_CALLBACK_URL        — Base public URL (e.g. https://walletapi.kakitu.africa)
MPESA_ENVIRONMENT         — "sandbox" or "production"
TREASURY_ADDRESS          — Treasury kshs_ wallet address
TREASURY_SEED             — Treasury wallet seed (hex)
```

Daraja base URLs:
- sandbox: `https://sandbox.safaricom.co.ke`
- production: `https://api.safaricom.co.ke`

---

## Error Handling

- STK Push fails → return error to app, no DB row saved
- Callback ResultCode != 0 → update transaction status to `failed`, do not send KSHS
- On-chain tx invalid (cashout) → return 400 to app, no B2C initiated
- Double tx_hash → return 409 Conflict to app
- Treasury insufficient funds → log error, update status to `failed` (manual replenishment needed)
- Always return `200 OK` to Safaricom callbacks regardless of internal errors (Safaricom retries on non-200)
