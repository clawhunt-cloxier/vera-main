package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
)

func demoDatabasePort(get func(string) string) (int, error) {
	raw := get("VERA_DEMO_PG_PORT")
	if raw == "" {
		return 55432, nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("invalid VERA_DEMO_PG_PORT")
	}
	return port, nil
}

func demoDatabaseURL(port int) string {
	return fmt.Sprintf("postgres://vera:vera-demo@127.0.0.1:%d/vera?sslmode=disable", port)
}

// docker exec sees the container environment, but not the defaults set by the
// running demo process. Select its database only on explicit operator request.
func quoteDatabaseEnv(args []string, get func(string) string) (func(string) string, error) {
	if len(args) == 1 && args[0] == "--demo-db" {
		if env := get("VERA_ENV"); env != "test" && env != "development" {
			return nil, errors.New("--demo-db requires test or development")
		}
		if get("DATABASE_URL") == "" {
			port, err := demoDatabasePort(get)
			if err != nil {
				return nil, err
			}
			return func(key string) string {
				if key == "DATABASE_URL" {
					return demoDatabaseURL(port)
				}
				return get(key)
			}, nil
		}
	} else if len(args) != 0 {
		return nil, errors.New("unsupported quote administration argument")
	}
	if get("DATABASE_URL") == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	return get, nil
}

type quoteStatus struct {
	Latest   bool     `json:"latest_available"`
	Realtime bool     `json:"realtime_trade"`
	Inputs   string   `json:"registration_inputs"`
	Missing  []string `json:"missing_inputs,omitempty"`
}

func emquantQuoteStatus(ctx context.Context, out io.Writer, get func(string) string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	provider, product := get("EMQUANT_PROVIDER_ID"), get("EMQUANT_PRODUCT")
	if provider == "" || product == "" {
		return errors.New("quote provider and product are required")
	}
	pool, err := db.Open(ctx, get("DATABASE_URL"))
	if err != nil {
		return errors.New("cannot connect to configured database")
	}
	defer pool.Close()
	capabilities, err := emquant.AllStockQuoteCapabilities(ctx, pool, provider, product)
	if err != nil {
		return errors.New("cannot read Choice governance records")
	}
	result := quoteStatus{Latest: capabilities.Latest, Realtime: capabilities.Realtime, Inputs: "valid"}
	for _, key := range []string{"EMQUANT_QUOTE_FIELDS", "EMQUANT_QUOTE_PRICE_EVIDENCE", "EMQUANT_QUOTE_TIME_EVIDENCE", "EMQUANT_QUOTE_MAX_AGE_MS", "EMQUANT_QUOTE_LICENSE_EVIDENCE", "EMQUANT_QUOTE_LICENSE_VERIFIED_BY", "EMQUANT_QUOTE_LICENSE_VALID_UNTIL"} {
		if get(key) == "" {
			result.Missing = append(result.Missing, key)
		}
	}
	if len(result.Missing) != 0 {
		result.Inputs = "incomplete"
	} else if _, err := readQuoteInitConfig(get); err != nil {
		result.Inputs = "invalid"
	}
	return json.NewEncoder(out).Encode(result)
}
