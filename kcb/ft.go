// kcb/ft.go
package kcb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

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

func (r *ftResponse) statusDescription() string {
	if r.Header != nil {
		return r.Header.StatusDescription
	}
	return r.StatusDescription
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
		TransactionType:      "MO",      // CRITICAL: Mobile Money
		DebitAccountNumber:   debitAccount,
		CreditAccountNumber:  phone,     // 2547XXXXXXXX
		DebitAmount:          float64(amountKES.IntPart()),
		PaymentDetails:       "Kakitu Payout",
		TransactionReference: txRef,
		Currency:             "KES",
		BeneficiaryDetails:   "Kakitu User",
		BeneficiaryBankCode:  "MPESA",  // CRITICAL: must be "MPESA" not "63902"
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshaling FT payload: %w", err)
	}
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

	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling KCB Funds Transfer: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading FT response body: %w", err)
	}
	var ftResp ftResponse
	if err := json.Unmarshal(respBody, &ftResp); err != nil {
		return nil, fmt.Errorf("parsing FT response: %w (raw: %s)", err, string(respBody))
	}
	if ftResp.code() != "0" {
		return nil, fmt.Errorf("KCB FT failed (code=%s): %s (raw: %s)",
			ftResp.code(), ftResp.statusDescription(), string(respBody))
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
