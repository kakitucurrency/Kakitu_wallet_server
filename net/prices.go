package net

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kakitucurrency/kakitu-wallet-server/config"
	"github.com/kakitucurrency/kakitu-wallet-server/database"
	"github.com/kakitucurrency/kakitu-wallet-server/models"
	"k8s.io/klog/v2"
)

const (
	// PriceStalenessThreshold is the maximum age of price data before it is
	// considered stale and a warning is emitted.
	PriceStalenessThreshold = 30 * time.Minute

	// PriceMaxReasonable is a sanity-check upper bound for any single currency
	// price of 1 KSHS. Since 1 KSHS is pegged to 1 KES (roughly 0.007 USD),
	// no legitimate price should exceed 1,000,000 in any currency.
	PriceMaxReasonable = 1_000_000.0
)

// priceState tracks when prices were last successfully updated.
var priceState = struct {
	mu          sync.RWMutex
	lastUpdated time.Time
}{
	lastUpdated: time.Time{}, // zero value = never updated
}

// SetPriceLastUpdated records the time of the most recent successful price update.
func SetPriceLastUpdated(t time.Time) {
	priceState.mu.Lock()
	defer priceState.mu.Unlock()
	priceState.lastUpdated = t
}

// GetPriceLastUpdated returns the time of the last successful price update.
func GetPriceLastUpdated() time.Time {
	priceState.mu.RLock()
	defer priceState.mu.RUnlock()
	return priceState.lastUpdated
}

// IsPriceStale returns true if price data is older than PriceStalenessThreshold
// or has never been updated.
func IsPriceStale() bool {
	last := GetPriceLastUpdated()
	if last.IsZero() {
		return true
	}
	return time.Since(last) > PriceStalenessThreshold
}

// validatePrice checks that a price value is positive and within a reasonable range.
func validatePrice(value float64, label string) error {
	if value <= 0 {
		return fmt.Errorf("price %s is non-positive: %f", label, value)
	}
	if value > PriceMaxReasonable {
		return fmt.Errorf("price %s exceeds reasonable maximum (%f > %f)", label, value, PriceMaxReasonable)
	}
	return nil
}

// KES is included alongside the standard currency list
var CurrencyList = []string{
	"ARS", "AUD", "BRL", "BTC", "CAD", "CHF", "CLP", "CNY", "CZK", "DKK", "EUR", "GBP",
	"HKD", "HUF", "IDR", "ILS", "INR", "JPY", "KES", "KRW", "MXN", "MYR", "NOK", "NZD",
	"PHP", "PKR", "PLN", "RUB", "SEK", "SGD", "THB", "TRY", "TWD", "USD", "ZAR", "SAR",
	"AED", "KWD", "UAH",
}

// Base request
func MakeGetRequest(url string) ([]byte, error) {
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		klog.Errorf("Error making request %s", err)
		return nil, err
	}
	resp, err := Client.Do(request)
	if err != nil {
		klog.Errorf("Error making GET request %s", err)
		return nil, err
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		klog.Errorf("Error decoding response body %s", err)
		return nil, err
	}
	return body, nil
}

func UpdateDolarTodayPrice() error {
	data := url.Values{}
	data.Set("action", "dt_currency_calculator_handler")
	data.Set("amount", "1")

	request, err := http.NewRequest(http.MethodPost, config.DOLARTODAY_URL, strings.NewReader(data.Encode()))
	if err != nil {
		klog.Errorf("Error creating request %s", err)
		return err
	}
	request.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	response, err := Client.Do(request)
	if err != nil {
		klog.Errorf("Error making dolar today request: %s", err)
		return err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		klog.Errorf("Error reading response body: %s", err)
		return err
	}

	var dolarTodayResp map[string]string
	err = json.Unmarshal(body, &dolarTodayResp)
	if err != nil {
		klog.Errorf("Error unmarshalling response: %s", err)
		return err
	}

	bitcoinValueStr, ok := dolarTodayResp["Dólar Bitcoin"]
	if !ok || bitcoinValueStr == "" {
		klog.Errorf("Invalid or missing 'Dólar Bitcoin' in response")
		return errors.New("invalid response data")
	}

	re := regexp.MustCompile(`\d+\.\d+`)
	match := re.FindString(bitcoinValueStr)
	if match == "" {
		klog.Errorf("No numeric value found in 'Dólar Bitcoin' response")
		return errors.New("no numeric value found")
	}

	fmt.Printf("DolarToday USD-VES: %s\n", match)
	database.GetRedisDB().Hset("prices", "dolartoday:usd-ves", match)
	return nil
}

func UpdateDolarSiPrice() error {
	rawResp, err := MakeGetRequest(config.DOLARSI_URL)
	if err != nil {
		klog.Errorf("Error making dolar si request %s", err)
		return err
	}
	var dolarsiResponse models.DolarsiResponse
	err = json.Unmarshal(rawResp, &dolarsiResponse)
	if err != nil {
		klog.Errorf("Error unmarshalling response %s", err)
		return err
	}

	if len(dolarsiResponse) < 2 {
		return errors.New("DolarSi response unexpected length")
	} else if dolarsiResponse[1].Casa.Venta == "" {
		return errors.New("DolarSi response price was empty")
	}
	price_ars := strings.ReplaceAll(dolarsiResponse[1].Casa.Venta, ".", "")
	price_ars = strings.ReplaceAll(price_ars, ",", ".")
	fmt.Printf("DolarSi USD-ARS: %s\n", price_ars)
	database.GetRedisDB().Hset("prices", "dolarsi:usd-ars", price_ars)
	return nil
}

// UpdateKshsCoingeckoPrices updates KSHS prices using the coingecko:kshs-* Redis key prefix.
// When KSHS is listed on CoinGecko, KSHS_CG_URL in config/coingecko.go will return live data.
// Until then, prices are seeded via the KES exchange rate (1 KSHS = 1 KES).
func UpdateKshsCoingeckoPrices() error {
	klog.Info("Updating KSHS prices\n")
	rawResp, err := MakeGetRequest(config.KSHS_CG_URL)
	if err != nil {
		// KSHS not on CoinGecko yet — seed with KES-pegged prices instead
		klog.Warningf("CoinGecko KSHS not available (%v), seeding KES-pegged prices", err)
		return seedKshsPricesFromKes()
	}
	var cgResp models.CoingeckoResponse
	if err := json.Unmarshal(rawResp, &cgResp); err != nil {
		klog.Errorf("Error unmarshalling coingecko response %v", err)
		return seedKshsPricesFromKes()
	}

	for _, currency := range CurrencyList {
		data_name := strings.ToLower(currency)
		if val, ok := cgResp.MarketData.CurrentPrice[data_name]; ok {
			if err := validatePrice(val, fmt.Sprintf("KSHS-%s", currency)); err != nil {
				klog.Warningf("Skipping invalid CoinGecko price: %v", err)
				continue
			}
			fmt.Printf("Coingecko KSHS-%s: %f\n", currency, val)
			database.GetRedisDB().Hset("prices", "coingecko:kshs-"+data_name, val)
		}
	}

	usdPrice, err := database.GetRedisDB().Hget("prices", "coingecko:kshs-usd")
	if err != nil {
		return err
	}
	usdPriceFloat, err := strconv.ParseFloat(usdPrice, 64)
	if err != nil {
		return err
	}

	// VES conversion
	bolivarPrice, err := database.GetRedisDB().Hget("prices", "dolartoday:usd-ves")
	if err == nil {
		if bolivarPriceFloat, err := strconv.ParseFloat(bolivarPrice, 64); err == nil {
			convertedves := usdPriceFloat * bolivarPriceFloat
			database.GetRedisDB().Hset("prices", "coingecko:kshs-ves", convertedves)
			fmt.Printf("Coingecko KSHS-VES: %f\n", convertedves)
		}
	}

	// ARS conversion
	arsPrice, err := database.GetRedisDB().Hget("prices", "dolarsi:usd-ars")
	if err == nil {
		if arsPriceFloat, err := strconv.ParseFloat(arsPrice, 64); err == nil {
			convertedars := usdPriceFloat * arsPriceFloat
			database.GetRedisDB().Hset("prices", "coingecko:kshs-ars", convertedars)
			fmt.Printf("Coingecko KSHS-ARS: %f\n", convertedars)
		}
	}

	// KES is always 1:1 with KSHS regardless of CoinGecko data
	database.GetRedisDB().Hset("prices", "coingecko:kshs-kes", 1.0)

	// Mark prices as freshly updated.
	SetPriceLastUpdated(time.Now())
	if IsPriceStale() {
		klog.Warning("Price data is stale after CoinGecko update — check clock or update logic")
	}

	return nil
}

// seedKshsPricesFromKes seeds Redis price keys assuming 1 KSHS = 1 KES.
// Fetches current KES/USD rate from exchangerate-api and derives all fiat prices.
func seedKshsPricesFromKes() error {
	rateUrl := config.DEFAULT_KES_EXCHANGE_RATE_URL
	body, err := MakeGetRequest(rateUrl)
	if err != nil {
		klog.Errorf("Error fetching KES exchange rate: %v", err)
		return err
	}
	var rateResp struct {
		Rates map[string]float64 `json:"rates"`
	}
	if err := json.Unmarshal(body, &rateResp); err != nil {
		klog.Errorf("Error parsing KES exchange rate response: %v", err)
		return err
	}
	// KES per 1 USD
	kesPerUsd, ok := rateResp.Rates["KES"]
	if !ok || kesPerUsd == 0 {
		return errors.New("KES rate not found in exchange rate response")
	}
	// 1 KSHS = 1 KES, so 1 KSHS = (1/kesPerUsd) USD
	kshsUsd := 1.0 / kesPerUsd

	for _, currency := range CurrencyList {
		ccy := strings.ToUpper(currency)
		if rate, ok := rateResp.Rates[ccy]; ok && rate != 0 {
			// price of 1 KSHS in `currency` = (1 KSHS in USD) * (currency per USD)
			price := kshsUsd * rate
			if err := validatePrice(price, fmt.Sprintf("KSHS-%s", ccy)); err != nil {
				klog.Warningf("Skipping invalid seeded price: %v", err)
				continue
			}
			data_name := strings.ToLower(currency)
			database.GetRedisDB().Hset("prices", "coingecko:kshs-"+data_name, price)
			fmt.Printf("KSHS-%s (KES-pegged): %f\n", ccy, price)
		}
	}
	// KES itself is always 1
	database.GetRedisDB().Hset("prices", "coingecko:kshs-kes", 1.0)
	fmt.Printf("KSHS-KES: 1.000000 (pegged)\n")

	// Mark prices as freshly updated.
	SetPriceLastUpdated(time.Now())
	return nil
}
