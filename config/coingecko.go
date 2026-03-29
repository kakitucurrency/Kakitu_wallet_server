package config

// KSHS_CG_URL will be updated once Kakitu (KSHS) is listed on CoinGecko.
// Until then the price update cron is effectively a no-op for live CoinGecko data.
const KSHS_CG_URL = "https://api.coingecko.com/api/v3/coins/kshs?localization=false&tickers=false&market_data=true&community_data=false&developer_data=false&sparkline=false"

// Exchange rate feeds used for VES/ARS conversions
const DOLARTODAY_URL = "https://dolartoday.com/wp-admin/admin-ajax.php"
const DOLARSI_URL = "https://www.dolarsi.com/api/api.php?type=valoresprincipales"

// ExchangeRateAPI for KES/USD (fallback until KSHS is on CoinGecko)
// Set KES_EXCHANGE_RATE_URL env var to override
const DEFAULT_KES_EXCHANGE_RATE_URL = "https://api.exchangerate-api.com/v4/latest/USD"
