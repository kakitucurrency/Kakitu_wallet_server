package net

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/kakitucurrency/kakitu-wallet-server/database"
	"github.com/kakitucurrency/kakitu-wallet-server/utils/mocks"
	"github.com/stretchr/testify/assert"
)

func init() {
	// Mock HTTP client
	Client = &mocks.MockClient{}
}

func TestDolarSiPrice(t *testing.T) {
	// Mock redis client
	os.Setenv("MOCK_REDIS", "true")
	defer os.Unsetenv("MOCK_REDIS")
	// Simulate response
	mocks.GetDoFunc = func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
			},
			Body: mocks.DolarSiResponse,
		}, nil
	}

	err := UpdateDolarSiPrice()
	assert.Equal(t, nil, err)
	dolarSi, err := database.GetRedisDB().Hget("prices", "dolarsi:usd-ars")
	assert.Equal(t, nil, err)
	assert.Equal(t, "290.00", dolarSi)
}

func TestUpdateKshsPrice(t *testing.T) {
	// Mock redis client
	os.Setenv("MOCK_REDIS", "true")
	defer os.Unsetenv("MOCK_REDIS")
	// Simulate response
	mocks.GetDoFunc = func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
			},
			Body: mocks.KshsCoingeckoResponse,
		}, nil
	}

	database.GetRedisDB().Hset("prices", "dolarsi:usd-ars", "290.00")
	database.GetRedisDB().Hset("prices", "dolartoday:usd-ves", "8.15")

	err := UpdateKshsCoingeckoPrices()
	assert.Equal(t, nil, err)

	for _, v := range CurrencyList {
		price, err := database.GetRedisDB().Hget("prices", fmt.Sprintf("coingecko:kshs-%s", strings.ToLower(v)))
		assert.Equal(t, nil, err)
		switch v {
		case "ARS":
			assert.Equal(t, "260.84224", price)
		case "AUD":
			assert.Equal(t, "1.31", price)
		case "BRL":
			assert.Equal(t, "4.67", price)
		case "BTC":
			assert.Equal(t, "0.00004494", price)
		case "CAD":
			assert.Equal(t, "1.18", price)
		case "CHF":
			assert.Equal(t, "0.877073", price)
		case "CLP":
			assert.Equal(t, "806.45", price)
		case "CNY":
			assert.Equal(t, "6.2", price)
		case "CZK":
			assert.Equal(t, "21.92", price)
		case "DKK":
			assert.Equal(t, "6.65", price)
		case "EUR":
			assert.Equal(t, "0.894673", price)
		case "GBP":
			assert.Equal(t, "0.773667", price)
		case "HKD":
			assert.Equal(t, "7.06", price)
		case "HUF":
			assert.Equal(t, "358.53", price)
		case "IDR":
			assert.Equal(t, "13360.1", price)
		case "ILS":
			assert.Equal(t, "2.99", price)
		case "INR":
			assert.Equal(t, "71.49", price)
		case "JPY":
			assert.Equal(t, "124.76", price)
		case "KRW":
			assert.Equal(t, "1206.55", price)
		case "MXN":
			assert.Equal(t, "18.09", price)
		case "MYR":
			assert.Equal(t, "4.03", price)
		case "NOK":
			assert.Equal(t, "8.93", price)
		case "NZD":
			assert.Equal(t, "1.47", price)
		case "PHP":
			assert.Equal(t, "50.57", price)
		case "PKR":
			assert.Equal(t, "198.02", price)
		case "PLN":
			assert.Equal(t, "4.23", price)
		case "RUB":
			assert.Equal(t, "54.42", price)
		case "SEK":
			assert.Equal(t, "9.58", price)
		case "SGD":
			assert.Equal(t, "1.26", price)
		case "THB":
			assert.Equal(t, "32.89", price)
		case "TRY":
			assert.Equal(t, "16.37", price)
		case "TWD":
			assert.Equal(t, "27.35", price)
		case "USD":
			assert.Equal(t, "0.899456", price)
		case "ZAR":
			assert.Equal(t, "15.38", price)
		case "SAR":
			assert.Equal(t, "3.38", price)
		case "AED":
			assert.Equal(t, "3.3", price)
		case "KWD":
			assert.Equal(t, "0.27726", price)
		case "UAH":
			assert.Equal(t, "33.17", price)
		case "VES":
			assert.Equal(t, "7.330566", price)
		case "KES":
			assert.Equal(t, "1", price)
		}
	}
}

