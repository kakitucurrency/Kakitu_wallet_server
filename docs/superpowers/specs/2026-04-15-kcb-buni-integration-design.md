# KCB Buni Integration Design

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace the Safaricom Daraja M-Pesa integration with KCB Buni as the sole payment rail for cash-in (STK Push) and cash-out (Funds Transfer MO), adding a polling fallback for unreliable FT callbacks.

**Architecture:** New `kcb/` package mirrors the existing `mpesa/` structure. Old `mpesa/` package and all dependent files are deleted. A background polling goroutine in `main.go` resolves stalled cashout transactions using `QueryCoreTransactionStatus`. Flutter updates only the endpoint URLs and one response field name.

**Tech Stack:** Go 1.21, Chi v5, GORM, PostgreSQL, Redis (token cache), KCB Buni API v1 (OAuth2 + STK Push + Funds Transfer + Query)

---

## Environment Variables

```bash
KCB_CONSUMER_KEY=        # OAuth2 client ID (KCB Buni dashboard)
KCB_CONSUMER_SECRET=     # OAuth2 client secret
KCB_COMPANY_CODE=        # e.g. KE0010001 — provided by KCB
KCB_DEBIT_ACCOUNT=       # Kakitu's KCB account number (debited on cashout)
KCB_CALLBACK_URL=        # https://walletapi.kakitu.org (no trailing slash)
KCB_ENV=sandbox          # "sandbox" | "production"
```

**Base URLs:**
- Sandbox: `https://uat.buni.kcbgroup.com`
- Production: `https://api.buni.kcbgroup.com`

All 6 variables are already set as `placeholder` in Railway. Update them once KCB provides credentials.

---

## Files to Create

| File | Purpose |
|------|---------|
| `kcb/auth.go` | OAuth2 token fetch + Redis cache |
| `kcb/stk.go` | STK Push (cash-in initiation) |
| `kcb/ft.go` | Funds Transfer type MO (cash-out to M-Pesa) |
| `kcb/query.go` | QueryCoreTransactionStatus (polling fallback) |
| `models/dbmodels/kcb_transaction.go` | DB model |
| `repository/kcb_txn_repo.go` | Data access layer |
| `controller/kcb_c.go` | HTTP handlers |

## Files to Delete

```
mpesa/auth.go
mpesa/stk.go
mpesa/b2c.go
mpesa/models.go
controller/mpesa_c.go
repository/mpesa_txn_repo.go
models/dbmodels/mpesa_txns.go
```

## Files to Modify

| File | Change |
|------|--------|
| `main.go` | Remove mpesa routes/wiring; add `/kcb/` routes; add polling goroutine; add KCBController wiring |
| `database/postgres.go` | Replace `MpesaTransaction` in `AutoMigrate` with `KCBTransaction` |
| `lib/ui/exchange/mpay_sheet.dart` | `/mpesa/cashin` → `/kcb/cashin` |
| `lib/ui/sell/sell_sheet.dart` | `/mpesa/cashout` → `/kcb/cashout`; `conversation_id` → `transaction_reference` |

---

## Database Model

```go
// models/dbmodels/kcb_transaction.go
type KCBTransaction struct {
    Base                               // UUID PK, CreatedAt, UpdatedAt
    Type                 string          `gorm:"not null"`                    // "cashin" | "cashout"
    AmountKES            decimal.Decimal `gorm:"type:decimal(10,2);not null"`
    KshsAddress          string          `gorm:"not null"`                    // 0x... ETH address
    Phone                string          `gorm:"not null"`                    // 2547XXXXXXXX
    CheckoutRequestID    string          `gorm:"uniqueIndex"`                 // STK — cashin only
    TransactionReference string          `gorm:"uniqueIndex"`                 // Our ref ≤12 chars — cashout only
    RetrievalRefNumber   string          `gorm:"index"`                       // KCB's ref on FT accept
    MpesaReceipt         string          // STK receipt (cashin success)
    TxHash               string          `gorm:"uniqueIndex:idx_kcb_tx_hash,where:tx_hash <> ''"` // cashout dedup
    Status               string          `gorm:"not null;default:'pending'"` // pending|processing|completed|failed
    PollAttempts         int             `gorm:"default:0"`
    LastPolledAt         *time.Time
}
```

---

## Repository Interface

```go
// repository/kcb_txn_repo.go
type KCBTxnRepo struct { DB *gorm.DB }

// Cash-in
CreatePendingCashIn(amountKES decimal.Decimal, phone, kshsAddress, checkoutRequestID string) (*dbmodels.KCBTransaction, error)
GetByCheckoutRequestID(id string) (*dbmodels.KCBTransaction, error)
ClaimPendingCashIn(checkoutRequestID string) (*dbmodels.KCBTransaction, error) // atomic pending→processing

// Cash-out
CreatePendingCashOut(amountKES decimal.Decimal, phone, kshsAddress, txHash, txRef string) (*dbmodels.KCBTransaction, error)
TxHashExists(txHash string) (bool, error)
UpdateCashOutAccepted(id uuid.UUID, retrievalRefNumber string) error  // pending→processing
UpdateStatus(id uuid.UUID, status, receipt string) error

// Polling
GetStaleCashOuts() ([]*dbmodels.KCBTransaction, error) // status=processing, last_polled>60s, attempts<10
IncrementPollAttempt(id uuid.UUID) error
```

`ClaimPendingCashIn` uses `SELECT ... FOR UPDATE` inside a transaction — same pattern as existing `mpesa_txn_repo.go`.

---

## KCB Package: `kcb/auth.go`

```go
// Redis key: "kcb:token"
// Fetch: POST {baseURL}/token
//   Authorization: Basic base64(consumerKey:consumerSecret)
//   Content-Type: application/x-www-form-urlencoded
//   Body: grant_type=client_credentials
// Cache with TTL = expires_in - 60 seconds
// Returns: access_token string
func GetToken() (string, error)
```

---

## KCB Package: `kcb/stk.go`

```go
// POST {baseURL}/mm/api/request/1.0.0/stkpush
// Headers: Authorization: Bearer {token}, routeCode: 207, operation: STKPush,
//          messageId: {32-char UUID no dashes}
// CRITICAL: sharedShortCode=true, orgShortCode="", orgPassKey=""
//           invoiceNumber = "{KCB_DEBIT_ACCOUNT}-{ref8}" (prefix required for routing)
// Success: header.statusCode == "0" → return CheckoutRequestID
type STKPushResult struct {
    CheckoutRequestID string
    MerchantRequestID string
}
func STKPush(token, phone, amountKES, ref string) (*STKPushResult, error)
```

---

## KCB Package: `kcb/ft.go`

```go
// POST {baseURL}/fundstransfer/1.0.0/api/v1/transfer
// CRITICAL: transactionType="MO", beneficiaryBankCode="MPESA" — NOT "EF"+"63902"
// transactionReference ≤ 12 chars
// Success: statusCode == "0" (flat or nested under "header")
// Returns: retrievalRefNumber for tracking
type FTResult struct {
    RetrievalRefNumber string
    MerchantID         string
}
func FundsTransfer(token, txRef, phone string, amountKES decimal.Decimal, beneficiaryName string) (*FTResult, error)
```

---

## KCB Package: `kcb/query.go`

```go
// POST {baseURL}/v1/core/t24/querytransaction/1.0.0/api/transactioninfo
// Header: messageID={32-char UUID}, channelCode=206
// Body header: featureCode="101", serviceCode="1004", serviceMode="sync"
// requestPayload.transactionInfo.primaryData: businessKey=txRef, businessKeyType="FT.REF"
// Returns: "SUCCESS" | "FAILED" | "PENDING"
func QueryTransaction(token, txRef, companyCode string) (string, error)
```

---

## Controller: `controller/kcb_c.go`

```go
type KCBController struct {
    TxnRepo   *repository.KCBTxnRepo
    EthClient *ethereum.Client
}
```

### `POST /kcb/cashin`

Request: `{phone string, amount_kes string, kshs_address string}`

Validation:
- `phone` matches `^2547\d{8}$`
- `amount_kes` parseable as whole positive integer ≥ 1
- `kshs_address` passes `utils.ValidateEthAddress`

Flow:
1. `kcb.GetToken()`
2. `kcb.STKPush(token, phone, amountKES, ref8)`  — `invoiceNumber = KCB_DEBIT_ACCOUNT + "-" + ref8`
3. `TxnRepo.CreatePendingCashIn(..., result.CheckoutRequestID)`
4. Return `200 {checkout_request_id, message: "Enter your M-Pesa PIN to complete payment"}`

### `POST /kcb/cashin/callback`

Request (KCB posts): `{Body: {stkCallback: {ResultCode, CheckoutRequestID, CallbackMetadata: {Item: [...]}}}}`

Flow:
1. Parse `ResultCode` and `CheckoutRequestID`
2. `ClaimPendingCashIn(checkoutRequestID)` — if not found or already claimed, return 200 (idempotent)
3. If `ResultCode != 0` → `UpdateStatus(failed, "")`, return `{"ResultCode":0,"ResultDesc":"Success"}`
4. Extract `Amount`, `MpesaReceiptNumber` from `CallbackMetadata.Item`
5. `EthClient.Mint(txn.KshsAddress, amount)` — mint KSHS
6. `UpdateStatus(completed, mpesaReceipt)`
7. Return `{"ResultCode":0,"ResultDesc":"Success"}` — KCB requires this exact shape

### `POST /kcb/cashout`

Request: `{phone string, amount_kes string, kshs_address string, tx_hash string}`

Validation: same as cashin + `tx_hash` non-empty

Flow:
1. `TxnRepo.TxHashExists(txHash)` → 409 `"duplicate_tx_hash"` if exists
2. `txRef = "KK" + hex(uuid[:5])` — 12 chars total
3. `TxnRepo.CreatePendingCashOut(..., txHash, txRef)`
4. `kcb.GetToken()`
5. `kcb.FundsTransfer(token, txRef, phone, amountKES, "Kakitu User")`
6. `statusCode != "0"` → `UpdateStatus(failed, "")`, return 502 `"payout_failed"`
7. `UpdateCashOutAccepted(id, result.RetrievalRefNumber)`
8. Return `200 {status: "processing", transaction_reference: txRef}`

### `POST /kcb/cashout/callback`

Request (KCB posts): `{transactionStatus, transactionReference, ftReference, transactionMessage}`

Flow:
1. Match `txn` by `transactionReference`
2. `transactionStatus == "SUCCESS"` → `UpdateStatus(completed, ftReference)`
3. `transactionStatus == "FAILED"` → `UpdateStatus(failed, transactionMessage)`
4. Return `200 OK`

---

## Polling Goroutine (`main.go`)

```go
go func() {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    for range ticker.C {
        txns, _ := kcbTxnRepo.GetStaleCashOuts()
        for _, txn := range txns {
            token, _ := kcb.GetToken()
            status, _ := kcb.QueryTransaction(token, txn.TransactionReference, companyCode)
            switch status {
            case "SUCCESS":
                kcbTxnRepo.UpdateStatus(txn.ID, "completed", txn.RetrievalRefNumber)
            case "FAILED":
                kcbTxnRepo.UpdateStatus(txn.ID, "failed", "")
            default: // PENDING
                kcbTxnRepo.IncrementPollAttempt(txn.ID)
            }
        }
    }
}()
```

`GetStaleCashOuts` query:
```sql
WHERE status = 'processing'
  AND (last_polled_at IS NULL OR last_polled_at < NOW() - INTERVAL '60 seconds')
  AND poll_attempts < 10
```

---

## Routes (`main.go`)

```go
// Remove entirely:
app.Route("/mpesa", ...)

// Add:
app.Route("/kcb", func(r chi.Router) {
    r.Post("/cashin",           kc.HandleCashIn)
    r.Post("/cashin/callback",  kc.HandleCashInCallback)
    r.Post("/cashout",          kc.HandleCashOut)
    r.Post("/cashout/callback", kc.HandleCashOutCallback)
})
```

---

## Flutter Changes

**`lib/ui/exchange/mpay_sheet.dart`:**
- URL: `/mpesa/cashin` → `/kcb/cashin`
- Request/response shape: identical

**`lib/ui/sell/sell_sheet.dart`:**
- URL: `/mpesa/cashout` → `/kcb/cashout`
- Response field: `conversation_id` → `transaction_reference`

---

## Critical API Gotchas (from buni docs)

1. **STK invoiceNumber** must be prefixed `"{KCB_DEBIT_ACCOUNT}-{ref}"` — without it KCB rejects with "unable to process to account"
2. **orgShortCode and orgPassKey** must be empty strings `""` — any placeholder value causes HTTP 500 "STK Authentication failure"
3. **Funds Transfer to M-Pesa** must use `transactionType: "MO"` + `beneficiaryBankCode: "MPESA"` — using `"EF"` + `"63902"` silently fails (API accepts, money never arrives)
4. **messageId** headers must be ≤32 alphanumeric chars — strip UUID dashes
5. **FT response** can be flat `{statusCode, ...}` OR nested `{header: {statusCode, ...}}` — handle both
6. **STK callback** must return `{"ResultCode":0,"ResultDesc":"Success"}` — KCB stops retrying if you return an error shape
7. **Token endpoint** uses `grant_type=client_credentials` in POST body (not query param)
