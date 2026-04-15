// kcb/stk.go
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
		PhoneNumber:            phone,
		Amount:                 amountKES,
		InvoiceNumber:          debitAccount + "-" + ref8, // CRITICAL: prefix required for shared-shortcode routing
		SharedShortCode:        true,
		OrgShortCode:           "", // CRITICAL: must be empty string — placeholder causes HTTP 500
		OrgPassKey:             "", // CRITICAL: must be empty string — placeholder causes HTTP 500
		CallbackUrl:            callbackURL + "/kcb/cashin/callback",
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

	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling KCB STK Push: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading STK response body: %w", err)
	}
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
