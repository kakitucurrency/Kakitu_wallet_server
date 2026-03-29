package mpesa

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/kakitucurrency/kakitu-wallet-server/database"
	"github.com/kakitucurrency/kakitu-wallet-server/utils"
)

const redisTokenKey = "mpesa:oauth_token"

// BaseURL returns the Daraja base URL based on MPESA_ENVIRONMENT env var.
func BaseURL() string {
	if utils.GetEnv("MPESA_ENVIRONMENT", "sandbox") == "production" {
		return "https://api.safaricom.co.ke"
	}
	return "https://sandbox.safaricom.co.ke"
}

// GetToken returns a valid Daraja OAuth token, using Redis cache when possible.
func GetToken() (string, error) {
	cached, err := database.GetRedisDB().Get(redisTokenKey)
	if err == nil && cached != "" {
		return cached, nil
	}

	consumerKey := utils.GetEnv("MPESA_CONSUMER_KEY", "")
	consumerSecret := utils.GetEnv("MPESA_CONSUMER_SECRET", "")
	if consumerKey == "" || consumerSecret == "" {
		return "", fmt.Errorf("MPESA_CONSUMER_KEY and MPESA_CONSUMER_SECRET must be set")
	}

	url := fmt.Sprintf("%s/oauth/v1/generate?grant_type=client_credentials", BaseURL())
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("building auth request: %w", err)
	}
	creds := base64.StdEncoding.EncodeToString([]byte(consumerKey + ":" + consumerSecret))
	req.Header.Set("Authorization", "Basic "+creds)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling Daraja auth: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading auth response: %w", err)
	}

	var authResp authResponse
	if err := json.Unmarshal(body, &authResp); err != nil {
		return "", fmt.Errorf("parsing auth response: %w", err)
	}
	if authResp.AccessToken == "" {
		return "", fmt.Errorf("empty access token from Daraja: %s", string(body))
	}

	expiresIn, err := strconv.Atoi(authResp.ExpiresIn)
	if err != nil {
		expiresIn = 3600
	}
	ttl := time.Duration(expiresIn-60) * time.Second
	_ = database.GetRedisDB().Set(redisTokenKey, authResp.AccessToken, ttl)

	return authResp.AccessToken, nil
}
