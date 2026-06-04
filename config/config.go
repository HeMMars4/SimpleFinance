package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Port           string
	Secret         string
	AdminUsername  string
	AdminPassword  string
	DB             DBConfig
	TBank          TBankConfig
	Bybit          BybitConfig
	BTCAddresses   []string
	XMRAddresses   []string
	ManualUSDRUB   float64
}

type DBConfig struct {
	Host     string
	Port     string
	User     string
	Password string
	Name     string
}

type TBankConfig struct {
	SessionID string
}

type BybitConfig struct {
	APIKey    string
	APISecret string
}

func Load() *Config {
	usdRub, _ := strconv.ParseFloat(getEnv("MANUAL_USD_RUB_RATE", "90.0"), 64)

	btcRaw := getEnv("BTC_ADDRESSES", "")
	var btcAddresses []string
	if btcRaw != "" {
		btcAddresses = strings.Split(btcRaw, ",")
	}

	xmrRaw := getEnv("XMR_ADDRESSES", "")
	var xmrAddresses []string
	if xmrRaw != "" {
		xmrAddresses = strings.Split(xmrRaw, ",")
	}

	return &Config{
		Port:          getEnv("APP_PORT", "8080"),
		Secret:        getEnv("APP_SECRET", "dev-secret-change-me"),
		AdminUsername: getEnv("ADMIN_USERNAME", "admin"),
		AdminPassword: getEnv("ADMIN_PASSWORD", "admin"),
		DB: DBConfig{
			Host:     getEnv("DB_HOST", "localhost"),
			Port:     getEnv("DB_PORT", "5432"),
			User:     getEnv("DB_USER", "finance"),
			Password: getEnv("DB_PASSWORD", "finance"),
			Name:     getEnv("DB_NAME", "simple_finance"),
		},
		TBank: TBankConfig{
			SessionID: getEnv("TBANK_SESSION_ID", ""),
		},
		Bybit: BybitConfig{
			APIKey:    getEnv("BYBIT_API_KEY", ""),
			APISecret: getEnv("BYBIT_API_SECRET", ""),
		},
		BTCAddresses: btcAddresses,
		XMRAddresses: xmrAddresses,
		ManualUSDRUB: usdRub,
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
