package config

import (
	"crypto/sha256"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Port          string
	TLSCert       string
	TLSKey        string
	Secret        string
	EncryptionKey []byte
	DB            DBConfig
	TBank         TBankConfig
	TInvest       TInvestConfig
	Bybit         BybitConfig
	BTCAddresses  []string
	XMRAddresses  []string
	Monero        MoneroConfig
	Steam         SteamConfig
	ManualUSDRUB  float64
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

type TInvestConfig struct {
	Token string
}

type BybitConfig struct {
	APIKey    string
	APISecret string
}

type MoneroConfig struct {
	RPCURL string
}

type SteamConfig struct {
	SteamID string
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

	var encKey []byte
	if raw := getEnv("ENCRYPTION_KEY", ""); raw != "" {
		sum := sha256.Sum256([]byte(raw))
		encKey = sum[:]
	}

	return &Config{
		Port:          getEnv("APP_PORT", "443"),
		EncryptionKey: encKey,
		TLSCert:       getEnv("TLS_CERT", ""),
		TLSKey:        getEnv("TLS_KEY", ""),
		Secret:        getEnv("APP_SECRET", "dev-secret-change-me"),
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
		TInvest: TInvestConfig{
			Token: getEnv("TINVEST_TOKEN", ""),
		},
		Bybit: BybitConfig{
			APIKey:    getEnv("BYBIT_API_KEY", ""),
			APISecret: getEnv("BYBIT_API_SECRET", ""),
		},
		BTCAddresses: btcAddresses,
		XMRAddresses: xmrAddresses,
		Monero: MoneroConfig{
			RPCURL: getEnv("MONERO_RPC_URL", ""),
		},
		Steam: SteamConfig{
			SteamID: getEnv("STEAM_ID", ""),
		},
		ManualUSDRUB: usdRub,
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
