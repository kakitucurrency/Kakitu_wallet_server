# KCB Buni Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the Safaricom Daraja M-Pesa integration with KCB Buni as the sole payment rail, adding STK Push cash-in, Funds Transfer MO cash-out, and a 30-second polling goroutine for stalled cashout transactions.

**Architecture:** New `kcb/` package (4 files: auth, stk, ft, query) mirrors the deleted `mpesa/` package. New DB model `kcb_transactions`, new repository `KCBTxnRepo`, new controller `KCBController`. Polling goroutine in `main.go`. Flutter updates two endpoint URLs and one response field name.

**Tech Stack:** Go 1.21, Chi v5, GORM/PostgreSQL, Redis (token cache), KCB Buni API v1 (OAuth2 + STK Push + Funds Transfer + QueryCoreTransactionStatus), shopspring/decimal, google/uuid

---

## File Map

| Action | File |
|--------|------|
| Create | `kcb/auth.go` |
| Create | `kcb/stk.go` |
| Create | `kcb/ft.go` |
| Create | `kcb/query.go` |
| Create | `models/dbmodels/kcb_transaction.go` |
| Create | `repository/kcb_txn_repo.go` |
| Create | `controller/kcb_c.go` |
| Modify | `main.go` |
| Modify | `database/postgres.go` |
| Delete | `mpesa/auth.go`, `mpesa/stk.go`, `mpesa/b2c.go`, `mpesa/models.go` |
| Delete | `controller/mpesa_c.go` |
| Delete | `repository/mpesa_txn_repo.go` |
| Delete | `models/dbmodels/mpesa_txns.go` |
| Modify (Flutter) | `lib/ui/exchange/mpay_sheet.dart` |
| Modify (Flutter) | `lib/ui/sell/sell_sheet.dart` |

---

### Task 1: DB Model

**Files:**
- Create: `models/dbmodels/kcb_transaction.go`
- Modify: `database/postgres.go`

- [ ] **Step 1: Create the model file**

```go
// models/dbmodels/kcb_transaction.go
package dbmodels

import (
	"time"

	"github.com/shopspring/decimal"
)

// KCBTransaction records every KCB Buni cash-in (STK Push) and cash-out (Funds Transfer MO).
type KCBTransaction struct {
	Base
	Type                 string          `json:"type" gorm:"not null"`                    // "cashin" | "cashout"
	AmountKES            decimal.Decimal `json:"amount_kes" gorm:"type:decimal(10,2);not null"`
	KshsAddress          string          `json:"kshs_address" gorm:"not null"`             // 0x... ETH address
	Phone                string          `json:"phone" gorm:"not null"`                    // 2547XXXXXXXX
	CheckoutRequestID    string          `json:"checkout_request_id" gorm:"uniqueIndex"`   // STK Push ID — cashin only
	TransactionReference string          `json:"transaction_reference" gorm:"uniqueIndex"` // Our ≤12-char ref — cashout only
	RetrievalRefNumber   string          `json:"retrieval_ref_number" gorm:"index"`         // KCB's ref on FT accept
	MpesaReceipt         string          `json:"mpesa_receipt"`                             // STK receipt on cashin success
	TxHash               string          `json:"tx_hash" gorm:"uniqueIndex:idx_kcb_tx_hash,where:tx_hash <> ''"` // cashout dedup
	Status               string          `json:"status" gorm:"not null;default:'pending'"` // pending|processing|completed|failed|mint_failed
	PollAttempts         int             `json:"poll_attempts" gorm:"default:0"`
	LastPolledAt         *time.Time      `json:"last_polled_at"`
}
```

- [ ] **Step 2: Update AutoMigrate in `database/postgres.go`**

Replace `&dbmodels.MpesaTransaction{}` with `&dbmodels.KCBTransaction{}`:

```go
func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(
		&dbmodels.FcmToken{},
		&dbmodels.KCBTransaction{},
		&dbmodels.StripeAddress{},
		&dbmodels.StripeCard{},
	)
}
```

- [ ] **Step 3: Verify the build compiles**

```bash
cd /Users/kiptengwer/Documents/kakitu/kakitu_wallet_server
go build ./...
```

Expected: no errors (will fail if mpesa_txns.go is still referenced — that's fine, we fix it in Task 8)

- [ ] **Step 4: Commit**

```bash
git add models/dbmodels/kcb_transaction.go database/postgres.go
git commit -m "feat(kcb): add KCBTransaction DB model"
```

---

### Task 2: Repository

**Files:**
- Create: `repository/kcb_txn_repo.go`

- [ ] **Step 1: Create the repository**

```go
// repository/kcb_txn_repo.go
package repository

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/kakitucurrency/kakitu-wallet-server/models/dbmodels"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// ErrDuplicateKCBTxHash is returned when a cashout tx_hash was already used.
var ErrDuplicateKCBTxHash = errors.New("duplicate tx_hash: cash-out already exists")

type KCBTxnRepo struct {
	DB *gorm.DB
}

// CreatePendingCashIn saves a new pending STK Push transaction.
func (r *KCBTxnRepo) CreatePendingCashIn(amountKES decimal.Decimal, phone, kshsAddress, checkoutRequestID string) (*dbmodels.KCBTransaction, error) {
	txn := &dbmodels.KCBTransaction{
		Type:              "cashin",
		AmountKES:         amountKES,
		Phone:             phone,
		KshsAddress:       kshsAddress,
		CheckoutRequestID: checkoutRequestID,
		Status:            "pending",
	}
	if err := r.DB.Create(txn).Error; err != nil {
		return nil, err
	}
	return txn, nil
}

// ClaimPendingCashIn atomically transitions a cashin from "pending" → "processing".
// Returns nil, nil if the row was already processed (idempotent).
func (r *KCBTxnRepo) ClaimPendingCashIn(checkoutRequestID string) (*dbmodels.KCBTransaction, error) {
	result := r.DB.
		Model(&dbmodels.KCBTransaction{}).
		Where("checkout_request_id = ? AND status = 'pending'", checkoutRequestID).
		Update("status", "processing")
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	var txn dbmodels.KCBTransaction
	if err := r.DB.Where("checkout_request_id = ?", checkoutRequestID).First(&txn).Error; err != nil {
		return nil, err
	}
	return &txn, nil
}

// CreatePendingCashOutAtomic atomically checks for duplicate tx_hash and inserts
// within a single DB transaction using SELECT FOR UPDATE.
func (r *KCBTxnRepo) CreatePendingCashOutAtomic(amountKES decimal.Decimal, phone, kshsAddress, txHash, txRef string) (*dbmodels.KCBTransaction, error) {
	var txn *dbmodels.KCBTransaction
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Raw(
			"SELECT COUNT(*) FROM kcb_transactions WHERE tx_hash = ? AND type = 'cashout' FOR UPDATE",
			txHash,
		).Scan(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrDuplicateKCBTxHash
		}
		txn = &dbmodels.KCBTransaction{
			Type:                 "cashout",
			AmountKES:            amountKES,
			Phone:                phone,
			KshsAddress:          kshsAddress,
			TxHash:               txHash,
			TransactionReference: txRef,
			Status:               "pending",
		}
		return tx.Create(txn).Error
	})
	return txn, err
}

// UpdateCashOutAccepted transitions a cashout from "pending" → "processing" and stores KCB's ref.
func (r *KCBTxnRepo) UpdateCashOutAccepted(id uuid.UUID, retrievalRefNumber string) error {
	return r.DB.Model(&dbmodels.KCBTransaction{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"status":               "processing",
			"retrieval_ref_number": retrievalRefNumber,
		}).Error
}

// UpdateStatus sets status and optionally mpesa_receipt on a transaction.
func (r *KCBTxnRepo) UpdateStatus(id uuid.UUID, status, receipt string) error {
	updates := map[string]interface{}{"status": status}
	if receipt != "" {
		updates["mpesa_receipt"] = receipt
	}
	return r.DB.Model(&dbmodels.KCBTransaction{}).Where("id = ?", id).Updates(updates).Error
}

// GetByTransactionReference finds a cashout by our 12-char txRef.
func (r *KCBTxnRepo) GetByTransactionReference(txRef string) (*dbmodels.KCBTransaction, error) {
	var txn dbmodels.KCBTransaction
	if err := r.DB.Where("transaction_reference = ?", txRef).First(&txn).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &txn, nil
}

// GetStaleCashOuts returns cashout transactions stuck in "processing" that need polling.
// Conditions: status=processing, last_polled_at is null or >60s ago, poll_attempts<10.
func (r *KCBTxnRepo) GetStaleCashOuts() ([]*dbmodels.KCBTransaction, error) {
	var txns []*dbmodels.KCBTransaction
	err := r.DB.Where(
		"type = 'cashout' AND status = 'processing' AND poll_attempts < 10 AND (last_polled_at IS NULL OR last_polled_at < ?)",
		time.Now().Add(-60*time.Second),
	).Find(&txns).Error
	return txns, err
}

// IncrementPollAttempt bumps poll_attempts and sets last_polled_at to now.
func (r *KCBTxnRepo) IncrementPollAttempt(id uuid.UUID) error {
	now := time.Now()
	return r.DB.Model(&dbmodels.KCBTransaction{}).Where("id = ?", id).Updates(map[string]interface{}{
		"poll_attempts":  gorm.Expr("poll_attempts + 1"),
		"last_polled_at": now,
	}).Error
}
```

- [ ] **Step 2: Verify build**

```bash
go build ./repository/...
```

Expected: compiles cleanly

- [ ] **Step 3: Commit**

```bash
git add repository/kcb_txn_repo.go
git commit -m "feat(kcb): add KCBTxnRepo"
```

---

### Task 3: KCB Auth (`kcb/auth.go`)

**Files:**
- Create: `kcb/auth.go`

- [ ] **Step 1: Create the file**

```go
// kcb/auth.go
package kcb

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kakitucurrency/kakitu-wallet-server/database"
	"github.com/kakitucurrency/kakitu-wallet-server/utils"
)

const redisTokenKey = "kcb:oauth_token"

// BaseURL returns the KCB Buni base URL based on KCB_ENV.
func BaseURL() string {
	if utils.GetEnv("KCB_ENV", "sandbox") == "production" {
		return "https://api.buni.kcbgroup.com"
	}
	return "https://uat.buni.kcbgroup.com"
}

type tokenResponse struct {
	AccessToken string  `json:"access_token"`
	ExpiresIn   float64 `json:"expires_in"` // KCB returns a number, not a string
}

// GetToken returns a valid KCB Buni OAuth2 token, using Redis cache when possible.
func GetToken() (string, error) {
	cached, err := database.GetRedisDB().Get(redisTokenKey)
	if err == nil && cached != "" {
		return cached, nil
	}

	consumerKey := utils.GetEnv("KCB_CONSUMER_KEY", "")
	consumerSecret := utils.GetEnv("KCB_CONSUMER_SECRET", "")
	if consumerKey == "" || consumerSecret == "" {
		return "", fmt.Errorf("KCB_CONSUMER_KEY and KCB_CONSUMER_SECRET must be set")
	}

	endpoint := BaseURL() + "/token"
	formData := url.Values{}
	formData.Set("grant_type", "client_credentials")

	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(formData.Encode()))
	if err != nil {
		return "", fmt.Errorf("building KCB auth request: %w", err)
	}
	creds := base64.StdEncoding.EncodeToString([]byte(consumerKey + ":" + consumerSecret))
	req.Header.Set("Authorization", "Basic "+creds)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling KCB token endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("parsing KCB token response: %w (raw: %s)", err, string(body))
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("empty access token from KCB: %s", string(body))
	}

	ttl := time.Duration(tr.ExpiresIn-60) * time.Second
	_ = database.GetRedisDB().Set(redisTokenKey, tr.AccessToken, ttl)

	return tr.AccessToken, nil
}
```

- [ ] **Step 2: Verify build**

```bash
go build ./kcb/...
```

Expected: compiles cleanly

- [ ] **Step 3: Commit**

```bash
git add kcb/auth.go
git commit -m "feat(kcb): add KCB Buni OAuth2 token management"
```

---

### Task 4: KCB STK Push (`kcb/stk.go`)

**Files:**
- Create: `kcb/stk.go`

- [ ] **Step 1: Create the file**

```go
// kcb/stk.go
package kcb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/kakitucurrency/kakitu-wallet-server/utils"
)

// stkPushRequest is the JSON body sent to KCB Buni /mm/api/request/1.0.0/stkpush
type stkPushRequest struct {
	PhoneNumber            string `json:"phoneNumber"`
	Amount                 string `json:"amount"`
	InvoiceNumber          string `json:"invoiceNumber"`
	SharedShortCode        bool   `json:"sharedShortCode"`
	OrgShortCode           string `json:"orgShortCode"`
	OrgPassKey             string `json:"orgPassKey"`
	CallbackUrl            string `json:"callbackUrl"`
	TransactionDescription string `json:"transactionDescription"`
}

type stkResponseHeader struct {
	StatusCode        string `json:"statusCode"`
	StatusDescription string `json:"statusDescription"`
}

type stkPushResponseBody struct {
	CheckoutRequestID string `json:"CheckoutRequestID"`
	MerchantRequestID string `json:"MerchantRequestID"`
	ResponseCode      int    `json:"ResponseCode"`
	CustomerMessage   string `json:"CustomerMessage"`
}

type stkPushResponse struct {
	Header   stkResponseHeader   `json:"header"`
	Response stkPushResponseBody `json:"response"`
}

// STKPushResult holds the IDs returned by a successful STK Push.
type STKPushResult struct {
	CheckoutRequestID string
	MerchantRequestID string
}

// STKPush initiates an M-Pesa STK Push via KCB Buni.
// phone must be 2547XXXXXXXX. amountKES is a whole-number string e.g. "500".
// ref8 is an 8-char hex string used to build the invoiceNumber.
func STKPush(token, phone, amountKES, ref8 string) (*STKPushResult, error) {
	debitAccount := utils.GetEnv("KCB_DEBIT_ACCOUNT", "")
	callbackURL := utils.GetEnv("KCB_CALLBACK_URL", "")
	if debitAccount == "" {
		return nil, fmt.Errorf("KCB_DEBIT_ACCOUNT must be set")
	}

	payload := stkPushRequest{
		PhoneNumber:     phone,
		Amount:          amountKES,
		InvoiceNumber:   debitAccount + "-" + ref8, // CRITICAL: prefix required for shared-shortcode routing
		SharedShortCode: true,
		OrgShortCode:    "", // CRITICAL: must be empty string — placeholder causes HTTP 500
		OrgPassKey:      "", // CRITICAL: must be empty string — placeholder causes HTTP 500
		CallbackUrl:     callbackURL + "/kcb/cashin/callback",
		TransactionDescription: "Cash In",
	}

	body, _ := json.Marshal(payload)
	messageID := strings.ReplaceAll(uuid.New().String(), "-", "") // 32 alphanumeric chars

	req, err := http.NewRequest(
		http.MethodPost,
		BaseURL()+"/mm/api/request/1.0.0/stkpush",
		bytes.NewBuffer(body),
	)
	if err != nil {
		return nil, fmt.Errorf("building STK request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("routeCode", "207")
	req.Header.Set("operation", "STKPush")
	req.Header.Set("messageId", messageID)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling KCB STK Push: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	var stkResp stkPushResponse
	if err := json.Unmarshal(respBody, &stkResp); err != nil {
		return nil, fmt.Errorf("parsing STK response: %w (raw: %s)", err, string(respBody))
	}
	if stkResp.Header.StatusCode != "0" {
		return nil, fmt.Errorf("KCB STK Push failed (code=%s): %s (raw: %s)",
			stkResp.Header.StatusCode, stkResp.Header.StatusDescription, string(respBody))
	}

	return &STKPushResult{
		CheckoutRequestID: stkResp.Response.CheckoutRequestID,
		MerchantRequestID: stkResp.Response.MerchantRequestID,
	}, nil
}

// STKCallbackBody is what KCB POSTs to /kcb/cashin/callback.
// The format mirrors the Safaricom Daraja STK callback exactly.
type STKCallbackBody struct {
	Body struct {
		StkCallback struct {
			MerchantRequestID string `json:"MerchantRequestID"`
			CheckoutRequestID string `json:"CheckoutRequestID"`
			ResultCode        int    `json:"ResultCode"`
			ResultDesc        string `json:"ResultDesc"`
			CallbackMetadata  *struct {
				Item []struct {
					Name  string      `json:"Name"`
					Value interface{} `json:"Value"`
				} `json:"Item"`
			} `json:"CallbackMetadata"`
		} `json:"stkCallback"`
	} `json:"Body"`
}
```

- [ ] **Step 2: Verify build**

```bash
go build ./kcb/...
```

Expected: compiles cleanly

- [ ] **Step 3: Commit**

```bash
git add kcb/stk.go
git commit -m "feat(kcb): add STK Push and callback model"
```

---

### Task 5: KCB Funds Transfer (`kcb/ft.go`)

**Files:**
- Create: `kcb/ft.go`

- [ ] **Step 1: Create the file**

```go
// kcb/ft.go
package kcb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/kakitucurrency/kakitu-wallet-server/utils"
	"github.com/shopspring/decimal"
)

type ftRequest struct {
	CompanyCode          string  `json:"companyCode"`
	TransactionType      string  `json:"transactionType"`
	DebitAccountNumber   string  `json:"debitAccountNumber"`
	CreditAccountNumber  string  `json:"creditAccountNumber"`
	DebitAmount          float64 `json:"debitAmount"`
	PaymentDetails       string  `json:"paymentDetails"`
	TransactionReference string  `json:"transactionReference"`
	Currency             string  `json:"currency"`
	BeneficiaryDetails   string  `json:"beneficiaryDetails"`
	BeneficiaryBankCode  string  `json:"beneficiaryBankCode"`
}

// ftResponse handles both the flat and nested response shapes KCB may return.
type ftResponse struct {
	// flat shape
	StatusCode         string `json:"statusCode"`
	StatusMessage      string `json:"statusMessage"`
	StatusDescription  string `json:"statusDescription"`
	MerchantID         string `json:"merchantID"`
	RetrievalRefNumber string `json:"retrievalRefNumber"`
	// nested shape
	Header *struct {
		StatusCode         string `json:"statusCode"`
		StatusMessage      string `json:"statusMessage"`
		StatusDescription  string `json:"statusDescription"`
		MerchantID         string `json:"merchantID"`
		RetrievalRefNumber string `json:"retrievalRefNumber"`
	} `json:"header"`
}

func (r *ftResponse) code() string {
	if r.Header != nil && r.Header.StatusCode != "" {
		return r.Header.StatusCode
	}
	return r.StatusCode
}

func (r *ftResponse) retrievalRef() string {
	if r.Header != nil {
		return r.Header.RetrievalRefNumber
	}
	return r.RetrievalRefNumber
}

func (r *ftResponse) merchantID() string {
	if r.Header != nil {
		return r.Header.MerchantID
	}
	return r.MerchantID
}

// FTResult holds the references returned by a successful Funds Transfer.
type FTResult struct {
	RetrievalRefNumber string
	MerchantID         string
}

// FundsTransfer sends KES from Kakitu's KCB account to a user's M-Pesa wallet.
// txRef must be ≤12 chars. phone is 2547XXXXXXXX. amountKES is a whole positive number.
// CRITICAL: uses transactionType="MO" + beneficiaryBankCode="MPESA" — do NOT use "EF"+"63902".
func FundsTransfer(token, txRef, phone string, amountKES decimal.Decimal) (*FTResult, error) {
	companyCode := utils.GetEnv("KCB_COMPANY_CODE", "")
	debitAccount := utils.GetEnv("KCB_DEBIT_ACCOUNT", "")
	if companyCode == "" || debitAccount == "" {
		return nil, fmt.Errorf("KCB_COMPANY_CODE and KCB_DEBIT_ACCOUNT must be set")
	}

	payload := ftRequest{
		CompanyCode:          companyCode,
		TransactionType:      "MO",     // CRITICAL: Mobile Money
		DebitAccountNumber:   debitAccount,
		CreditAccountNumber:  phone,    // 2547XXXXXXXX
		DebitAmount:          float64(amountKES.IntPart()),
		PaymentDetails:       "Kakitu Payout",
		TransactionReference: txRef,
		Currency:             "KES",
		BeneficiaryDetails:   "Kakitu User",
		BeneficiaryBankCode:  "MPESA", // CRITICAL: must be "MPESA" not "63902"
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(
		http.MethodPost,
		BaseURL()+"/fundstransfer/1.0.0/api/v1/transfer",
		bytes.NewBuffer(body),
	)
	if err != nil {
		return nil, fmt.Errorf("building FT request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling KCB Funds Transfer: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	var ftResp ftResponse
	if err := json.Unmarshal(respBody, &ftResp); err != nil {
		return nil, fmt.Errorf("parsing FT response: %w (raw: %s)", err, string(respBody))
	}
	if ftResp.code() != "0" {
		return nil, fmt.Errorf("KCB FT failed (code=%s): %s (raw: %s)",
			ftResp.code(), ftResp.StatusDescription, string(respBody))
	}

	return &FTResult{
		RetrievalRefNumber: ftResp.retrievalRef(),
		MerchantID:         ftResp.merchantID(),
	}, nil
}

// FTCallbackBody is what KCB POSTs to /kcb/cashout/callback after processing.
type FTCallbackBody struct {
	FtReference              string `json:"ftReference"`
	TransactionDate          string `json:"transactionDate"`
	Amount                   string `json:"amount"`
	TransactionStatus        string `json:"transactionStatus"`   // "SUCCESS" | "FAILED"
	TransactionMessage       string `json:"transactionMessage"`
	BeneficiaryAccountNumber string `json:"beneficiaryAccountNumber"`
	BeneficiaryName          string `json:"beneficiaryName"`
	TransactionReference     string `json:"transactionReference"` // matches our txRef
	MerchantId               string `json:"merchantId"`
	DebitAccountNumber       string `json:"debitAccountNumber"`
}
```

- [ ] **Step 2: Verify build**

```bash
go build ./kcb/...
```

Expected: compiles cleanly

- [ ] **Step 3: Commit**

```bash
git add kcb/ft.go
git commit -m "feat(kcb): add Funds Transfer MO and callback model"
```

---

### Task 6: KCB Query Transaction (`kcb/query.go`)

**Files:**
- Create: `kcb/query.go`

- [ ] **Step 1: Create the file**

```go
// kcb/query.go
package kcb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

type queryReqHeader struct {
	MessageID           string `json:"messageId"`
	FeatureCode         string `json:"featureCode"`
	FeatureName         string `json:"featureName"`
	ServiceCode         string `json:"serviceCode"`
	ServiceName         string `json:"serviceName"`
	ServiceSubCategory  string `json:"serviceSubCategory"`
	MinorServiceVersion string `json:"minorServiceVersion"`
	ChannelCode         string `json:"channelCode"`
	ChannelName         string `json:"channelName"`
	RouteCode           string `json:"routeCode"`
	TimeStamp           string `json:"timeStamp"`
	ServiceMode         string `json:"serviceMode"`
	SubscribeEvents     string `json:"subscribeEvents"`
	CallBackURL         string `json:"callBackURL"`
}

type queryRequest struct {
	Header         queryReqHeader `json:"header"`
	RequestPayload struct {
		TransactionInfo struct {
			PrimaryData struct {
				BusinessKey     string `json:"businessKey"`
				BusinessKeyType string `json:"businessKeyType"`
			} `json:"primaryData"`
			AdditionalDetails struct {
				CompanyCode string `json:"companyCode"`
			} `json:"additionalDetails"`
		} `json:"transactionInfo"`
	} `json:"requestPayload"`
}

type queryResponse struct {
	Header struct {
		StatusCode string `json:"statusCode"`
	} `json:"header"`
	ResponsePayload struct {
		TransactionInfo struct {
			PrimaryData struct {
				TransactionStatus string `json:"transactionStatus"`
			} `json:"primaryData"`
		} `json:"transactionInfo"`
	} `json:"responsePayload"`
}

// QueryTransaction queries the status of a cashout by our transactionReference.
// Returns "SUCCESS", "FAILED", or "PENDING".
func QueryTransaction(token, txRef, companyCode string) (string, error) {
	messageID := strings.ReplaceAll(uuid.New().String(), "-", "")

	var req queryRequest
	req.Header = queryReqHeader{
		MessageID:           messageID,
		FeatureCode:         "101",
		FeatureName:         "FinancialInquiries",
		ServiceCode:         "1004",
		ServiceName:         "TransactionInfo",
		ServiceSubCategory:  "ACCOUNT",
		MinorServiceVersion: "1.0",
		ChannelCode:         "206",
		ChannelName:         "ibank",
		RouteCode:           "001",
		TimeStamp:           time.Now().UTC().Format(time.RFC3339Nano),
		ServiceMode:         "sync",
		SubscribeEvents:     "1",
		CallBackURL:         "",
	}
	req.RequestPayload.TransactionInfo.PrimaryData.BusinessKey = txRef
	req.RequestPayload.TransactionInfo.PrimaryData.BusinessKeyType = "FT.REF"
	req.RequestPayload.TransactionInfo.AdditionalDetails.CompanyCode = companyCode

	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequest(
		http.MethodPost,
		BaseURL()+"/v1/core/t24/querytransaction/1.0.0/api/transactioninfo",
		bytes.NewBuffer(body),
	)
	if err != nil {
		return "PENDING", fmt.Errorf("building query request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("messageID", messageID)
	httpReq.Header.Set("channelCode", "206")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "PENDING", fmt.Errorf("calling KCB query: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	var qResp queryResponse
	if err := json.Unmarshal(respBody, &qResp); err != nil {
		return "PENDING", nil // treat parse failure as still pending
	}

	status := qResp.ResponsePayload.TransactionInfo.PrimaryData.TransactionStatus
	switch status {
	case "SUCCESS":
		return "SUCCESS", nil
	case "FAILED":
		return "FAILED", nil
	default:
		return "PENDING", nil
	}
}
```

- [ ] **Step 2: Verify build**

```bash
go build ./kcb/...
```

Expected: compiles cleanly

- [ ] **Step 3: Commit**

```bash
git add kcb/query.go
git commit -m "feat(kcb): add QueryCoreTransactionStatus polling"
```

---

### Task 7: Controller (`controller/kcb_c.go`)

**Files:**
- Create: `controller/kcb_c.go`

- [ ] **Step 1: Create the controller**

```go
// controller/kcb_c.go
package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/go-chi/render"
	"github.com/google/uuid"
	"github.com/kakitucurrency/kakitu-wallet-server/ethereum"
	"github.com/kakitucurrency/kakitu-wallet-server/kcb"
	"github.com/kakitucurrency/kakitu-wallet-server/repository"
	"github.com/kakitucurrency/kakitu-wallet-server/utils"
	"github.com/shopspring/decimal"
	"k8s.io/klog/v2"
)

// KCBController handles KCB Buni cash-in and cash-out HTTP endpoints.
type KCBController struct {
	EthClient *ethereum.Client
	TxnRepo   *repository.KCBTxnRepo
}

// normalizeKCBPhone converts Kenyan phone numbers to 2547XXXXXXXX required by KCB Buni.
// Accepts 07XXXXXXXX, +2547XXXXXXXX, 2547XXXXXXXX.
func normalizeKCBPhone(phone string) (string, error) {
	digits := regexp.MustCompile(`\D`).ReplaceAllString(phone, "")
	switch {
	case strings.HasPrefix(digits, "254") && len(digits) == 12:
		return digits, nil
	case strings.HasPrefix(digits, "0") && len(digits) == 10:
		return "254" + digits[1:], nil
	case len(digits) == 9:
		return "254" + digits, nil
	default:
		return "", fmt.Errorf("unrecognised phone format: %s", phone)
	}
}

// isValidTxHash returns true if hash is a 0x-prefixed 32-byte hex string.
func isValidKCBTxHash(hash string) bool {
	if len(hash) != 66 || !strings.HasPrefix(hash, "0x") {
		return false
	}
	for _, c := range hash[2:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// validateKCBCallbackAuth checks that callbacks carry a valid CALLBACK_SECRET.
// When CALLBACK_SECRET is empty all callbacks are allowed (dev/sandbox).
func validateKCBCallbackAuth(r *http.Request) bool {
	secret := utils.GetEnv("CALLBACK_SECRET", "")
	if secret == "" {
		return true
	}
	if r.Header.Get("Authorization") == secret {
		return true
	}
	if r.URL.Query().Get("secret") == secret {
		return true
	}
	return false
}

// ── Cash-In ──────────────────────────────────────────────────────────────────

type kcbCashInRequest struct {
	Phone       string `json:"phone"`
	AmountKES   string `json:"amount_kes"`
	KshsAddress string `json:"kshs_address"`
}

// HandleCashIn initiates an STK Push to collect KES from the user's M-Pesa.
// POST /kcb/cashin
func (kc *KCBController) HandleCashIn(w http.ResponseWriter, r *http.Request) {
	var req kcbCashInRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ErrBadrequest(w, r, "invalid_request_body")
		return
	}
	if req.Phone == "" {
		ErrBadrequest(w, r, "phone_required")
		return
	}
	if req.AmountKES == "" {
		ErrBadrequest(w, r, "amount_kes_required")
		return
	}
	if !common.IsHexAddress(req.KshsAddress) {
		ErrBadrequest(w, r, "invalid_kshs_address")
		return
	}

	phone, err := normalizeKCBPhone(req.Phone)
	if err != nil {
		ErrBadrequest(w, r, fmt.Sprintf("invalid_phone: %s", err))
		return
	}

	amount, err := strconv.ParseInt(req.AmountKES, 10, 64)
	if err != nil || amount <= 0 {
		ErrBadrequest(w, r, "amount_kes_must_be_positive_integer")
		return
	}

	token, err := kcb.GetToken()
	if err != nil {
		klog.Errorf("HandleCashIn: GetToken error: %v", err)
		ErrInternalServerError(w, r, "token_error")
		return
	}

	// ref8 is used to build invoiceNumber = "{debitAccount}-{ref8}"
	ref8 := strings.ReplaceAll(uuid.New().String(), "-", "")[:8]

	result, err := kcb.STKPush(token, phone, req.AmountKES, ref8)
	if err != nil {
		klog.Errorf("HandleCashIn: STKPush error: %v", err)
		ErrInternalServerError(w, r, "stk_push_failed")
		return
	}

	amountDec := decimal.NewFromInt(amount)
	if _, err := kc.TxnRepo.CreatePendingCashIn(amountDec, phone, req.KshsAddress, result.CheckoutRequestID); err != nil {
		klog.Errorf("HandleCashIn: CreatePendingCashIn error: %v", err)
		ErrInternalServerError(w, r, "database_error")
		return
	}

	render.Status(r, http.StatusOK)
	render.JSON(w, r, map[string]string{
		"status":              "pending",
		"checkout_request_id": result.CheckoutRequestID,
		"message":             "Enter your M-Pesa PIN to complete payment",
	})
}

// HandleCashInCallback processes the STK Push result posted by KCB.
// POST /kcb/cashin/callback
func (kc *KCBController) HandleCashInCallback(w http.ResponseWriter, r *http.Request) {
	if !validateKCBCallbackAuth(r) {
		klog.Warning("Rejected KCB cash-in callback: invalid or missing secret")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	// KCB requires a 200 response immediately.
	w.WriteHeader(http.StatusOK)

	var body kcb.STKCallbackBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return
	}

	cb := body.Body.StkCallback
	checkoutRequestID := cb.CheckoutRequestID

	if cb.ResultCode != 0 {
		klog.Infof("STK failed for %s: ResultCode=%d %s", checkoutRequestID, cb.ResultCode, cb.ResultDesc)
		txn, _ := kc.TxnRepo.ClaimPendingCashIn(checkoutRequestID)
		if txn != nil {
			_ = kc.TxnRepo.UpdateStatus(txn.ID, "failed", "")
		}
		return
	}

	// Extract MpesaReceiptNumber from CallbackMetadata.Item[].
	var receipt string
	if cb.CallbackMetadata != nil {
		for _, item := range cb.CallbackMetadata.Item {
			if item.Name == "MpesaReceiptNumber" {
				if v, ok := item.Value.(string); ok {
					receipt = v
				}
				break
			}
		}
	}
	if receipt == "" {
		klog.Errorf("HandleCashInCallback: missing MpesaReceiptNumber for %s", checkoutRequestID)
		txn, _ := kc.TxnRepo.ClaimPendingCashIn(checkoutRequestID)
		if txn != nil {
			_ = kc.TxnRepo.UpdateStatus(txn.ID, "failed", "")
		}
		return
	}

	// Atomically claim the pending transaction — prevents double-mint on duplicate callbacks.
	txn, err := kc.TxnRepo.ClaimPendingCashIn(checkoutRequestID)
	if err != nil {
		klog.Errorf("HandleCashInCallback: ClaimPendingCashIn error for %s: %v", checkoutRequestID, err)
		return
	}
	if txn == nil {
		klog.Warningf("HandleCashInCallback: duplicate or unknown callback for %s, skipping", checkoutRequestID)
		return
	}

	amountKES, _ := txn.AmountKES.Float64()
	if _, err := kc.EthClient.MintKSHS(txn.KshsAddress, receipt, int64(amountKES)); err != nil {
		klog.Errorf("MINT_FAILED — user %s owed %s KSHS, receipt: %s | error: %v",
			txn.KshsAddress, txn.AmountKES.String(), receipt, err)
		_ = kc.TxnRepo.UpdateStatus(txn.ID, "mint_failed", receipt)
		return
	}

	_ = kc.TxnRepo.UpdateStatus(txn.ID, "completed", receipt)
}

// ── Cash-Out ─────────────────────────────────────────────────────────────────

type kcbCashOutRequest struct {
	Phone       string `json:"phone"`
	AmountKES   string `json:"amount_kes"`
	TxHash      string `json:"tx_hash"`
	KshsAddress string `json:"kshs_address"`
}

// HandleCashOut verifies the on-chain KSHS burn and initiates a KCB Funds Transfer to M-Pesa.
// POST /kcb/cashout
func (kc *KCBController) HandleCashOut(w http.ResponseWriter, r *http.Request) {
	var req kcbCashOutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ErrBadrequest(w, r, "invalid_request_body")
		return
	}
	if req.Phone == "" {
		ErrBadrequest(w, r, "phone_required")
		return
	}
	if req.AmountKES == "" {
		ErrBadrequest(w, r, "amount_kes_required")
		return
	}
	if !isValidKCBTxHash(req.TxHash) {
		ErrBadrequest(w, r, "invalid_tx_hash")
		return
	}
	if !common.IsHexAddress(req.KshsAddress) {
		ErrBadrequest(w, r, "invalid_kshs_address")
		return
	}

	phone, err := normalizeKCBPhone(req.Phone)
	if err != nil {
		ErrBadrequest(w, r, fmt.Sprintf("invalid_phone: %s", err))
		return
	}

	amount, err := strconv.ParseInt(req.AmountKES, 10, 64)
	if err != nil || amount <= 0 {
		ErrBadrequest(w, r, "amount_kes_must_be_positive_integer")
		return
	}

	// Verify the on-chain ERC20 burn before sending any money.
	if err := kc.EthClient.VerifyBurn(req.TxHash, req.KshsAddress, amount); err != nil {
		ErrBadrequest(w, r, fmt.Sprintf("on_chain_verification_failed: %s", err))
		return
	}

	// txRef ≤ 12 chars: "KK" + first 10 hex chars of a UUID
	txRef := "KK" + strings.ReplaceAll(uuid.New().String(), "-", "")[:10]

	amountDec := decimal.NewFromInt(amount)
	txn, err := kc.TxnRepo.CreatePendingCashOutAtomic(amountDec, phone, req.KshsAddress, req.TxHash, txRef)
	if err != nil {
		if errors.Is(err, repository.ErrDuplicateKCBTxHash) {
			render.Status(r, http.StatusConflict)
			render.JSON(w, r, map[string]string{"error": "tx_hash_already_used"})
			return
		}
		klog.Errorf("HandleCashOut: CreatePendingCashOutAtomic error: %v", err)
		ErrInternalServerError(w, r, "database_error")
		return
	}

	token, err := kcb.GetToken()
	if err != nil {
		_ = kc.TxnRepo.UpdateStatus(txn.ID, "failed", "")
		klog.Errorf("HandleCashOut: GetToken error: %v", err)
		ErrInternalServerError(w, r, "token_error")
		return
	}

	ftResult, err := kcb.FundsTransfer(token, txRef, phone, amountDec)
	if err != nil {
		_ = kc.TxnRepo.UpdateStatus(txn.ID, "failed", "")
		klog.Errorf("HandleCashOut: FundsTransfer error for %s: %v", txRef, err)
		ErrInternalServerError(w, r, "payout_failed")
		return
	}

	if err := kc.TxnRepo.UpdateCashOutAccepted(txn.ID, ftResult.RetrievalRefNumber); err != nil {
		klog.Errorf("CRITICAL: FT accepted but UpdateCashOutAccepted failed — MANUAL RECONCILIATION REQUIRED | txRef: %s | retrievalRef: %s | phone: %s | amount: %d KES | err: %v",
			txRef, ftResult.RetrievalRefNumber, phone, amount, err)
	}

	render.Status(r, http.StatusOK)
	render.JSON(w, r, map[string]string{
		"status":                "processing",
		"transaction_reference": txRef,
	})
}

// HandleCashOutCallback processes the Funds Transfer result posted by KCB.
// POST /kcb/cashout/callback
func (kc *KCBController) HandleCashOutCallback(w http.ResponseWriter, r *http.Request) {
	if !validateKCBCallbackAuth(r) {
		klog.Warning("Rejected KCB cash-out callback: invalid or missing secret")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	w.WriteHeader(http.StatusOK)

	var body kcb.FTCallbackBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return
	}

	txn, err := kc.TxnRepo.GetByTransactionReference(body.TransactionReference)
	if err != nil {
		klog.Errorf("HandleCashOutCallback: GetByTransactionReference error for %s: %v", body.TransactionReference, err)
		return
	}
	if txn == nil {
		klog.Warningf("HandleCashOutCallback: unknown transactionReference %s", body.TransactionReference)
		return
	}

	switch body.TransactionStatus {
	case "SUCCESS":
		_ = kc.TxnRepo.UpdateStatus(txn.ID, "completed", body.FtReference)
	case "FAILED":
		klog.Errorf("KCB cashout FAILED for txRef=%s | message: %s", body.TransactionReference, body.TransactionMessage)
		_ = kc.TxnRepo.UpdateStatus(txn.ID, "failed", "")
	default:
		klog.Warningf("HandleCashOutCallback: unknown transactionStatus '%s' for %s", body.TransactionStatus, body.TransactionReference)
	}
}
```

- [ ] **Step 2: Verify build**

```bash
go build ./controller/...
```

Expected: compiles cleanly (may show unused import warnings if kcb package not fully wired yet — that's fine)

- [ ] **Step 3: Commit**

```bash
git add controller/kcb_c.go
git commit -m "feat(kcb): add KCBController with cashin/cashout handlers"
```

---

### Task 8: Wire `main.go` and delete old files

**Files:**
- Modify: `main.go`
- Delete: `mpesa/auth.go`, `mpesa/stk.go`, `mpesa/b2c.go`, `mpesa/models.go`, `controller/mpesa_c.go`, `repository/mpesa_txn_repo.go`, `models/dbmodels/mpesa_txns.go`

- [ ] **Step 1: Delete the old mpesa files**

```bash
rm /Users/kiptengwer/Documents/kakitu/kakitu_wallet_server/mpesa/auth.go
rm /Users/kiptengwer/Documents/kakitu/kakitu_wallet_server/mpesa/stk.go
rm /Users/kiptengwer/Documents/kakitu/kakitu_wallet_server/mpesa/b2c.go
rm /Users/kiptengwer/Documents/kakitu/kakitu_wallet_server/mpesa/models.go
rm /Users/kiptengwer/Documents/kakitu/kakitu_wallet_server/controller/mpesa_c.go
rm /Users/kiptengwer/Documents/kakitu/kakitu_wallet_server/repository/mpesa_txn_repo.go
rm /Users/kiptengwer/Documents/kakitu/kakitu_wallet_server/models/dbmodels/mpesa_txns.go
```

- [ ] **Step 2: Update `main.go` — replace mpesa wiring with KCB**

In `main.go`, find these lines (~162–179) and replace:

```go
// REMOVE these lines:
mpesaTxnRepo := &repository.MpesaTxnRepo{
    DB: db,
}
// ...
mc := controller.MpesaController{EthClient: ethClient, MpesaTxnRepo: mpesaTxnRepo}
```

Replace with:

```go
kcbTxnRepo := &repository.KCBTxnRepo{DB: db}
kc := &controller.KCBController{EthClient: ethClient, TxnRepo: kcbTxnRepo}
```

- [ ] **Step 3: Update `main.go` — replace routes**

Find the `/mpesa` route block (~244–250):

```go
// REMOVE:
app.Route("/mpesa", func(r chi.Router) {
    r.Get("/config", mc.HandleConfig)
    r.Post("/cashin", mc.HandleCashIn)
    r.Post("/cashin/callback", mc.HandleCashInCallback)
    r.Post("/cashout", mc.HandleCashOut)
    r.Post("/cashout/callback", mc.HandleCashOutCallback)
})
```

Replace with:

```go
// KCB Buni routes
app.Route("/kcb", func(r chi.Router) {
    r.Post("/cashin",          kc.HandleCashIn)
    r.Post("/cashin/callback", kc.HandleCashInCallback)
    r.Post("/cashout",         kc.HandleCashOut)
    r.Post("/cashout/callback", kc.HandleCashOutCallback)
})
```

- [ ] **Step 4: Add the polling goroutine to `main.go`**

After the route setup and before `http.ListenAndServe`, add:

```go
// KCB cashout polling — resolves stalled FT transactions every 30 seconds.
companyCode := utils.GetEnv("KCB_COMPANY_CODE", "")
go func() {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    for range ticker.C {
        txns, err := kcbTxnRepo.GetStaleCashOuts()
        if err != nil {
            klog.Errorf("KCB polling: GetStaleCashOuts error: %v", err)
            continue
        }
        for _, txn := range txns {
            token, err := kcb.GetToken()
            if err != nil {
                klog.Errorf("KCB polling: GetToken error: %v", err)
                break
            }
            status, err := kcb.QueryTransaction(token, txn.TransactionReference, companyCode)
            if err != nil {
                klog.Errorf("KCB polling: QueryTransaction error for %s: %v", txn.TransactionReference, err)
                _ = kcbTxnRepo.IncrementPollAttempt(txn.ID)
                continue
            }
            switch status {
            case "SUCCESS":
                _ = kcbTxnRepo.UpdateStatus(txn.ID, "completed", txn.RetrievalRefNumber)
                klog.Infof("KCB polling: cashout %s resolved as SUCCESS", txn.TransactionReference)
            case "FAILED":
                _ = kcbTxnRepo.UpdateStatus(txn.ID, "failed", "")
                klog.Errorf("KCB polling: cashout %s resolved as FAILED", txn.TransactionReference)
            default:
                _ = kcbTxnRepo.IncrementPollAttempt(txn.ID)
            }
        }
    }
}()
```

- [ ] **Step 5: Remove unused imports from `main.go`**

Remove any `"github.com/kakitucurrency/kakitu-wallet-server/mpesa"` import. Add:

```go
"github.com/kakitucurrency/kakitu-wallet-server/kcb"
```

- [ ] **Step 6: Full build check**

```bash
go build ./...
```

Expected: zero errors, zero unused imports

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "feat(kcb): wire KCBController into main.go, remove Daraja integration"
```

---

### Task 9: Flutter URL Updates

**Files:**
- Modify: `lib/ui/exchange/mpay_sheet.dart` (in kakitu_wallet_flutter-master)
- Modify: `lib/ui/sell/sell_sheet.dart`

- [ ] **Step 1: Update cash-in URL in `mpay_sheet.dart`**

Find and replace the endpoint URL:

```bash
grep -n "mpesa/cashin" /Users/kiptengwer/Documents/kakitu/kakitu_wallet_flutter-master/lib/ui/exchange/mpay_sheet.dart
```

Change `/mpesa/cashin` → `/kcb/cashin` (leave everything else unchanged)

- [ ] **Step 2: Update cash-out URL in `sell_sheet.dart`**

```bash
grep -n "mpesa/cashout" /Users/kiptengwer/Documents/kakitu/kakitu_wallet_flutter-master/lib/ui/sell/sell_sheet.dart
```

Change `/mpesa/cashout` → `/kcb/cashout`

- [ ] **Step 3: Update response field in `sell_sheet.dart`**

The cashout response no longer returns `conversation_id`. It returns `transaction_reference`. Find any reference to `conversation_id` in the response parsing and change it to `transaction_reference`:

```bash
grep -n "conversation_id" /Users/kiptengwer/Documents/kakitu/kakitu_wallet_flutter-master/lib/ui/sell/sell_sheet.dart
```

Change `json['conversation_id']` → `json['transaction_reference']`

- [ ] **Step 4: Verify Flutter still analyzes cleanly**

```bash
cd /Users/kiptengwer/Documents/kakitu/kakitu_wallet_flutter-master
flutter analyze lib/ui/exchange/mpay_sheet.dart lib/ui/sell/sell_sheet.dart
```

Expected: No issues found

- [ ] **Step 5: Commit Flutter changes**

```bash
cd /Users/kiptengwer/Documents/kakitu/kakitu_wallet_flutter-master
git add lib/ui/exchange/mpay_sheet.dart lib/ui/sell/sell_sheet.dart
git commit -m "feat(kcb): update cashin/cashout URLs and response field to KCB Buni"
```

---

## Final Verification

After all 9 tasks:

```bash
# Server
cd /Users/kiptengwer/Documents/kakitu/kakitu_wallet_server
go build ./...
# Expected: zero errors

# Flutter
cd /Users/kiptengwer/Documents/kakitu/kakitu_wallet_flutter-master
flutter analyze
# Expected: 0 errors

# Confirm mpesa package is gone
ls mpesa/
# Expected: directory not found or empty
```

---

## Post-Implementation: Testing with Real Credentials

Once KCB provides credentials, update Railway variables:
```bash
railway variables set KCB_CONSUMER_KEY=<real> KCB_CONSUMER_SECRET=<real> KCB_COMPANY_CODE=<real> KCB_DEBIT_ACCOUNT=<real>
```

Then test the cash-in flow end-to-end:
1. `POST /kcb/cashin` with a real Kenyan phone, amount 1, and a valid ETH address
2. User enters M-Pesa PIN on their phone
3. KCB posts callback to `/kcb/cashin/callback`
4. KSHS minted to the ETH address
