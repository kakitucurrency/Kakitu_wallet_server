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

	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Do(req)
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
