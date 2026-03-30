package mpesa

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/kakitucurrency/kakitu-wallet-server/utils"
)

// InitiateSTKPush sends an STK Push prompt to the user's phone.
// phone must be in format 2547XXXXXXXX. amountKES is an integer string e.g. "100".
// Returns the CheckoutRequestID on success.
func InitiateSTKPush(token, phone, amountKES, callbackURL string) (string, error) {
	shortCode := utils.GetEnv("MPESA_SHORTCODE", "")
	passKey := utils.GetEnv("MPESA_PASSKEY", "")
	if shortCode == "" || passKey == "" {
		return "", fmt.Errorf("MPESA_SHORTCODE and MPESA_PASSKEY must be set")
	}

	timestamp := time.Now().Format("20060102150405")
	rawPassword := shortCode + passKey + timestamp
	password := base64.StdEncoding.EncodeToString([]byte(rawPassword))

	payload := STKPushRequest{
		BusinessShortCode: shortCode,
		Password:          password,
		Timestamp:         timestamp,
		TransactionType:   "CustomerPayBillOnline",
		Amount:            amountKES,
		PartyA:            phone,
		PartyB:            shortCode,
		PhoneNumber:       phone,
		CallBackURL:       callbackURL + "/mpesa/cashin/callback",
		AccountReference:  "Kakitu",
		TransactionDesc:   "Cash In",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshalling STK request: %w", err)
	}

	url := fmt.Sprintf("%s/mpesa/stkpush/v1/processrequest", BaseURL())
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(body))
	if err != nil {
		return "", fmt.Errorf("building STK request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling STK Push API: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading STK response: %w", err)
	}

	var stkResp STKPushResponse
	if err := json.Unmarshal(respBody, &stkResp); err != nil {
		return "", fmt.Errorf("parsing STK response (%s): %w", string(respBody), err)
	}

	// Daraja returns errorCode/errorMessage when the request is rejected outright
	if stkResp.ErrorCode != "" {
		return "", fmt.Errorf("Daraja error %s: %s (raw: %s)", stkResp.ErrorCode, stkResp.ErrorMessage, string(respBody))
	}
	if stkResp.ResponseCode != "0" {
		return "", fmt.Errorf("STK Push failed (code=%s): %s (raw: %s)", stkResp.ResponseCode, stkResp.ResponseDescription, string(respBody))
	}

	return stkResp.CheckoutRequestID, nil
}
