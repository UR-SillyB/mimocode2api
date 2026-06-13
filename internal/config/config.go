package config

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Port             int
	BindHost         string
	APIKey           string
	UpstreamBase     string
	BootstrapPath    string
	ChatPath         string
	Fingerprint      string // Single fingerprint (legacy, takes precedence over count)
	FingerprintCount int    // Number of random fingerprints to generate (default 5)
	ProxyURL         string // Explicit proxy address, e.g. http://127.0.0.1:7890
	ProxyEnabled     bool   // Use HTTP_PROXY/HTTPS_PROXY from environment
	Debug            bool
	ModelAliases     []string // Vision model aliases exposed to clients
}

func Load() *Config {
	base := strings.TrimRight(getEnv("MIMO_FREE_BASE_URL", "https://api.xiaomimimo.com"), "/")
	apiKey := os.Getenv("API_KEY")
	if apiKey == "" {
		apiKey = generateAPIKey()
	}

	return &Config{
		Port:             getEnvInt("MIMO2API_PORT", 10000),
		BindHost:         getEnv("BIND_HOST", "0.0.0.0"),
		APIKey:           apiKey,
		UpstreamBase:     base,
		BootstrapPath:    base + "/api/free-ai/bootstrap",
		ChatPath:         base + "/api/free-ai/openai/chat",
		Fingerprint:      os.Getenv("MIMO_FINGERPRINT"),
		FingerprintCount: getEnvInt("MIMO_FINGERPRINT_COUNT", 5),
		ProxyURL:         os.Getenv("MIMO_PROXY_URL"),
		ProxyEnabled:     getEnvBool("MIMO_PROXY_ENABLED", false),
		Debug:            getEnvBool("MIMO2API_DEBUG", false),
		ModelAliases:     parseModelAliases(getEnv("MODEL_ALIASES", "gpt-4o,gpt-4o-mini")),
	}
}

func generateAPIKey() string {
	b := make([]byte, 32)
	rand.Read(b)
	return "sk-" + hex.EncodeToString(b)
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	v := strings.ToLower(os.Getenv(key))
	if v == "1" || v == "true" || v == "yes" {
		return true
	}
	if v == "0" || v == "false" || v == "no" {
		return false
	}
	return fallback
}

func parseModelAliases(s string) []string {
	if s == "" {
		return nil
	}
	var aliases []string
	for _, name := range strings.Split(s, ",") {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			aliases = append(aliases, trimmed)
		}
	}
	return aliases
}