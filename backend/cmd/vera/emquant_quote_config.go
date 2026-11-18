package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/audit"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
)

type quoteInitConfig struct {
	ProviderID      string
	Product         string
	LicenseID       uuid.UUID
	PolicyID        uuid.UUID
	Codes           []string
	Fields          []string
	StatusIndicator string
	StatusEvidence  string
	StatusMapping   map[string]string
	TimeEvidence    string
	PriceEvidence   string
	LatencyMode     string
	LatencyEvidence string
	MaxAge          time.Duration
	Evidence        string
	VerifiedBy      string
	ValidUntil      time.Time
}

func emquantInitQuote(ctx context.Context, out io.Writer, get func(string) string) error {
	c, err := readQuoteInitConfig(get)
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, get("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}

	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO providers(id,name,kind,allow_model_processing)
			VALUES($1,'Choice / 东方财富','vendor',true) ON CONFLICT DO NOTHING`, c.ProviderID); err != nil {
			return err
		}
		var eligible bool
		if err := tx.QueryRow(ctx, `SELECT kind='vendor' AND enabled AND NOT synthetic AND allow_model_processing
			FROM providers WHERE id=$1`, c.ProviderID).Scan(&eligible); err != nil {
			return err
		}
		if !eligible {
			return errors.New("existing provider is disabled, synthetic or model use revoked")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO source_licenses
			(id,provider_id,product_code,dataset_id,vendor_method,field_names,instrument_scope,instrument_codes,
			 evidence_ref,verified_by,verified_at,valid_from,valid_until,allow_storage,allow_display,allow_model_processing)
			VALUES($1,$2,$3,'mkt.intraday_quote','CSQSNAPSHOT',$4,'listed',$5,$6,$7,clock_timestamp(),clock_timestamp(),$8,true,true,true)
			ON CONFLICT DO NOTHING`, c.LicenseID, c.ProviderID, c.Product, c.Fields, c.Codes, c.Evidence, c.VerifiedBy, c.ValidUntil); err != nil {
			return err
		}
		var same bool
		if err := tx.QueryRow(ctx, `SELECT provider_id=$2 AND product_code=$3 AND dataset_id='mkt.intraday_quote'
			AND vendor_method='CSQSNAPSHOT' AND field_names=$4 AND instrument_scope='listed' AND instrument_codes=$5
			AND evidence_ref=$6 AND verified_by=$7 AND valid_until=$8 AND revoked_at IS NULL
			AND allow_storage AND allow_display AND allow_model_processing
			FROM source_licenses WHERE id=$1`, c.LicenseID, c.ProviderID, c.Product, c.Fields, c.Codes, c.Evidence, c.VerifiedBy, c.ValidUntil).Scan(&same); err != nil {
			return err
		}
		if !same {
			return errors.New("existing quote license conflicts; use a new reviewed identity")
		}

		if _, err := tx.Exec(ctx, `INSERT INTO quote_mapping_policies
			(id,source_license_id,dataset_id,mapping_name,mapping_version,time_convention,time_evidence_ref,
			 price_unit,price_evidence_ref,latency_mode,latency_evidence_ref,max_age_ms,verified_by,verified_at,valid_from,valid_until,
			 status_indicator,status_evidence_ref,status_mapping)
			VALUES($1,$2,'mkt.intraday_quote','emquant-quote',1,'hhmmss-asia-shanghai',$3,'CNY',$4,$5,$6,$7,$8,clock_timestamp(),clock_timestamp(),$9,$10,$11,$12)
			ON CONFLICT DO NOTHING`, c.PolicyID, c.LicenseID, c.TimeEvidence, c.PriceEvidence, c.LatencyMode, nullableString(c.LatencyEvidence), c.MaxAge.Milliseconds(), c.VerifiedBy, c.ValidUntil, nullableString(c.StatusIndicator), nullableString(c.StatusEvidence), nullableJSON(c.StatusMapping)); err != nil {
			return err
		}
		var policySame bool
		if err := tx.QueryRow(ctx, `SELECT source_license_id=$2 AND dataset_id='mkt.intraday_quote' AND mapping_name='emquant-quote'
			AND mapping_version=1 AND time_convention='hhmmss-asia-shanghai' AND time_evidence_ref=$3 AND price_unit='CNY'
			AND price_evidence_ref=$4 AND latency_mode=$5 AND latency_evidence_ref IS NOT DISTINCT FROM $6
			AND max_age_ms=$7 AND verified_by=$8 AND valid_until=$9 AND revoked_at IS NULL
			AND status_indicator IS NOT DISTINCT FROM $10 AND status_evidence_ref IS NOT DISTINCT FROM $11
			AND status_mapping IS NOT DISTINCT FROM $12
			FROM quote_mapping_policies WHERE id=$1`, c.PolicyID, c.LicenseID, c.TimeEvidence, c.PriceEvidence, c.LatencyMode,
			nullableString(c.LatencyEvidence), c.MaxAge.Milliseconds(), c.VerifiedBy, c.ValidUntil, nullableString(c.StatusIndicator), nullableString(c.StatusEvidence), nullableJSON(c.StatusMapping)).Scan(&policySame); err != nil {
			return err
		}
		if !policySame {
			return errors.New("existing quote mapping policy conflicts; use a new reviewed identity")
		}
		return nil
	})

	status := "success"
	if err != nil {
		status = "failure"
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	auditErr := pgx.BeginFunc(auditCtx, pool, func(tx pgx.Tx) error {
		return audit.Record(auditCtx, tx, nil, "vera-emquant", "emquant.quote.init", "policy:"+c.PolicyID.String(), status, map[string]any{
			"provider": c.ProviderID, "license_id": c.LicenseID.String(), "codes": c.Codes, "latency_mode": c.LatencyMode,
		})
	})
	if auditErr != nil {
		return errors.New("quote configuration may have completed; audit recording failed")
	}
	if err != nil {
		return fmt.Errorf("quote configuration failed: %w", err)
	}
	_, err = fmt.Fprintf(out, "license_id=%s\npolicy_id=%s\nlatency_mode=%s\nmax_age_ms=%d\n", c.LicenseID, c.PolicyID, c.LatencyMode, c.MaxAge.Milliseconds())
	return err
}

func readQuoteInitConfig(get func(string) string) (quoteInitConfig, error) {
	var c quoteInitConfig
	required := func(key string) (string, error) {
		v := get(key)
		if v == "" || strings.TrimSpace(v) != v {
			return "", fmt.Errorf("missing or invalid %s", key)
		}
		return v, nil
	}
	var err error
	if c.ProviderID, err = required("EMQUANT_PROVIDER_ID"); err != nil {
		return quoteInitConfig{}, err
	}
	if c.Product, err = required("EMQUANT_PRODUCT"); err != nil {
		return quoteInitConfig{}, err
	}
	if c.Evidence, err = required("EMQUANT_QUOTE_LICENSE_EVIDENCE"); err != nil {
		return quoteInitConfig{}, err
	}
	if c.VerifiedBy, err = required("EMQUANT_QUOTE_LICENSE_VERIFIED_BY"); err != nil {
		return quoteInitConfig{}, err
	}
	if c.TimeEvidence, err = required("EMQUANT_QUOTE_TIME_EVIDENCE"); err != nil {
		return quoteInitConfig{}, err
	}
	if c.PriceEvidence, err = required("EMQUANT_QUOTE_PRICE_EVIDENCE"); err != nil {
		return quoteInitConfig{}, err
	}
	licenseText, err := required("EMQUANT_QUOTE_LICENSE_ID")
	if err != nil {
		return quoteInitConfig{}, err
	}
	if c.LicenseID, err = uuid.Parse(licenseText); err != nil || c.LicenseID == uuid.Nil {
		return quoteInitConfig{}, errors.New("invalid EMQUANT_QUOTE_LICENSE_ID")
	}
	policyText, err := required("EMQUANT_QUOTE_POLICY_ID")
	if err != nil {
		return quoteInitConfig{}, err
	}
	if c.PolicyID, err = uuid.Parse(policyText); err != nil || c.PolicyID == uuid.Nil {
		return quoteInitConfig{}, errors.New("invalid EMQUANT_QUOTE_POLICY_ID")
	}
	c.Codes, err = csvUpper(get, "EMQUANT_QUOTE_CODES")
	if err != nil {
		return quoteInitConfig{}, err
	}
	fields, err := csvUpper(get, "EMQUANT_QUOTE_FIELDS")
	if err != nil {
		return quoteInitConfig{}, err
	}
	statusIndicator := strings.TrimSpace(get("EMQUANT_QUOTE_STATUS_INDICATOR"))
	statusEvidence := strings.TrimSpace(get("EMQUANT_QUOTE_STATUS_EVIDENCE"))
	statusMapping := map[string]string{}
	if raw := strings.TrimSpace(get("EMQUANT_QUOTE_STATUS_MAPPING")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &statusMapping); err != nil || statusMapping == nil {
			return quoteInitConfig{}, errors.New("invalid EMQUANT_QUOTE_STATUS_MAPPING")
		}
	}
	if statusIndicator == "" && (statusEvidence != "" || len(statusMapping) != 0) {
		return quoteInitConfig{}, errors.New("quote status evidence and mapping require EMQUANT_QUOTE_STATUS_INDICATOR")
	}
	if statusIndicator != "" {
		if statusEvidence == "" {
			return quoteInitConfig{}, errors.New("EMQUANT_QUOTE_STATUS_EVIDENCE is required with status indicator")
		}
	}
	policyFields, err := emquant.QuotePolicyFields(emquant.QuoteMappingPolicy{StatusIndicator: statusIndicator, StatusEvidence: statusEvidence, StatusMapping: statusMapping})
	if err != nil {
		return quoteInitConfig{}, err
	}
	if strings.Join(fields, ",") != strings.Join(policyFields, ",") {
		return quoteInitConfig{}, fmt.Errorf("EMQUANT_QUOTE_FIELDS must be %s", strings.Join(policyFields, ","))
	}
	c.Fields, c.StatusIndicator, c.StatusEvidence, c.StatusMapping = policyFields, statusIndicator, statusEvidence, statusMapping
	c.LatencyMode = strings.TrimSpace(get("EMQUANT_QUOTE_LATENCY_MODE"))
	if c.LatencyMode != "real_time" && c.LatencyMode != "delayed" && c.LatencyMode != "unknown" {
		return quoteInitConfig{}, errors.New("EMQUANT_QUOTE_LATENCY_MODE must be real_time, delayed or unknown")
	}
	c.LatencyEvidence = strings.TrimSpace(get("EMQUANT_QUOTE_LATENCY_EVIDENCE"))
	if c.LatencyMode != "unknown" && c.LatencyEvidence == "" {
		return quoteInitConfig{}, errors.New("EMQUANT_QUOTE_LATENCY_EVIDENCE is required unless latency is unknown")
	}
	if c.LatencyMode == "real_time" {
		continuous := false
		for _, value := range c.StatusMapping {
			continuous = continuous || value == "continuous"
		}
		if c.StatusIndicator == "" || !continuous {
			return quoteInitConfig{}, errors.New("real_time latency requires a verified status indicator with a continuous mapping")
		}
	}
	maxAgeText, err := required("EMQUANT_QUOTE_MAX_AGE_MS")
	if err != nil {
		return quoteInitConfig{}, err
	}
	maxAgeMS, err := strconv.ParseInt(maxAgeText, 10, 64)
	if err != nil || maxAgeMS < 1 || maxAgeMS > 86400000 {
		return quoteInitConfig{}, errors.New("EMQUANT_QUOTE_MAX_AGE_MS must be between 1 and 86400000")
	}
	c.MaxAge = time.Duration(maxAgeMS) * time.Millisecond
	validUntilText, err := required("EMQUANT_QUOTE_LICENSE_VALID_UNTIL")
	if err != nil {
		return quoteInitConfig{}, err
	}
	c.ValidUntil, err = time.Parse(time.RFC3339, validUntilText)
	if err != nil || !c.ValidUntil.After(time.Now()) {
		return quoteInitConfig{}, errors.New("invalid or expired EMQUANT_QUOTE_LICENSE_VALID_UNTIL")
	}
	return c, nil
}

func csvUpper(get func(string) string, key string) ([]string, error) {
	raw := get(key)
	if raw == "" || strings.TrimSpace(raw) != raw {
		return nil, fmt.Errorf("missing or invalid %s", key)
	}
	seen := map[string]struct{}{}
	values := make([]string, 0)
	for _, part := range strings.Split(raw, ",") {
		value := strings.ToUpper(strings.TrimSpace(part))
		if value == "" || strings.ContainsAny(value, "\x00,\n\r\t") {
			return nil, fmt.Errorf("invalid %s", key)
		}
		if _, ok := seen[value]; ok {
			return nil, fmt.Errorf("duplicate value in %s", key)
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("missing or invalid %s", key)
	}
	return values, nil
}

func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func nullableJSON(v map[string]string) any {
	if len(v) == 0 {
		return nil
	}
	b, _ := json.Marshal(v)
	return b
}
