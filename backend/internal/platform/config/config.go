package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	Environment         string
	Addr                string
	DatabaseURL         string
	OIDCIssuer          string
	OIDCClientID        string
	OIDCClientSecret    string
	OIDCRedirectURL     string
	RealResearchEnabled bool
	RegistrationEnabled bool
}

func Load() (*Config, error) {
	cfg := &Config{
		Environment:         getEnv("VERA_ENV", "development"),
		Addr:                getEnv("VERA_ADDR", "127.0.0.1:8081"),
		DatabaseURL:         os.Getenv("DATABASE_URL"),
		OIDCIssuer:          os.Getenv("OIDC_ISSUER"),
		OIDCClientID:        os.Getenv("OIDC_CLIENT_ID"),
		OIDCClientSecret:    os.Getenv("OIDC_CLIENT_SECRET"),
		OIDCRedirectURL:     getEnv("OIDC_REDIRECT_URL", "http://localhost:8080/auth/callback"),
		RealResearchEnabled: getBoolEnv("VERA_REAL_RESEARCH_ENABLED", false),
		RegistrationEnabled: getBoolEnv("VERA_REGISTRATION_ENABLED", false),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.OIDCIssuer == "" {
		return nil, fmt.Errorf("OIDC_ISSUER is required")
	}
	if cfg.OIDCClientID == "" {
		return nil, fmt.Errorf("OIDC_CLIENT_ID is required")
	}
	if cfg.OIDCClientSecret == "" {
		return nil, fmt.Errorf("OIDC_CLIENT_SECRET is required")
	}

	if cfg.Environment == "production" && !cfg.RealResearchEnabled {
		return nil, fmt.Errorf("VERA_REAL_RESEARCH_ENABLED must be true in production")
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getBoolEnv(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fallback
		}
		return b
	}
	return fallback
}
