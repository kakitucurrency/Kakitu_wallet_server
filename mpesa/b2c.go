package mpesa

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/kakitucurrency/kakitu-wallet-server/utils"
)

// InitiateB2C sends KES from the Kakitu shortcode to a user's phone.
// phone must be in format 2547XXXXXXXX. amountKES is an integer string e.g. "100".
// Returns the ConversationID on success.
func InitiateB2C(token, phone, amountKES, callbackURL string) (string, error) {
	shortCode := utils.GetEnv("MPESA_SHORTCODE", "174379")
	initiatorName := utils.GetEnv("MPESA_B2C_INITIATOR", "testapi")
	securityCred := utils.GetEnv("MPESA_B2C_SECURITY_CRED", "")
	if securityCred == "" {
		return "", fmt.Errorf("MPESA_B2C_SECURITY_CRED must be set")
	}

	payload := B2CRequest{
		InitiatorName:      initiatorName,
		SecurityCredential: securityCred,
		CommandID:          "BusinessPayment",
		Amount:             amountKES,
		PartyA:             shortCode,
		PartyB:             phone,
		Remarks:            "Kakitu Cash Out",
		QueueTimeOutURL:    callbackURL + "/mpesa/cashout/callback",
		ResultURL:          callbackURL + "/mpesa/cashout/callback",
		Occasion:           "CashOut",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshalling B2C request: %w", err)
	}

	url := fmt.Sprintf("%s/mpesa/b2c/v3/paymentrequest", BaseURL())
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(body))
	if err != nil {
		return "", fmt.Errorf("building B2C request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling B2C API: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading B2C response: %w", err)
	}

	var b2cResp B2CResponse
	if err := json.Unmarshal(respBody, &b2cResp); err != nil {
		return "", fmt.Errorf("parsing B2C response: %w", err)
	}
	if b2cResp.ResponseCode != "0" {
		return "", fmt.Errorf("B2C failed: %s", b2cResp.ResponseDescription)
	}

	return b2cResp.ConversationID, nil
}
