package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
)

// Explicit administrative provisioning, never called from API/startup. Evidence
// references describe verified account scope and the user's processing grant.
func emquantInitBrief(ctx context.Context, out io.Writer, get func(string) string) error {
	for _, k := range []string{"DATABASE_URL", "EMQUANT_PROVIDER_ID", "EMQUANT_PRODUCT", "EMQUANT_BRIEF_CODES", "EMQUANT_SECURITY_LICENSE", "EMQUANT_LIQUIDITY_LICENSE", "EMQUANT_LICENSE_EVIDENCE", "EMQUANT_LICENSE_VERIFIED_BY", "EMQUANT_LICENSE_VALID_UNTIL"} {
		if strings.TrimSpace(get(k)) == "" {
			return fmt.Errorf("missing %s", k)
		}
	}
	until, err := time.Parse(time.RFC3339, get("EMQUANT_LICENSE_VALID_UNTIL"))
	if err != nil || !until.After(time.Now()) {
		return errors.New("invalid license expiry")
	}
	codes := strings.Split(get("EMQUANT_BRIEF_CODES"), ",")
	pool, err := db.Open(ctx, get("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO providers(id,name,kind,allow_model_processing) VALUES($1,'Choice / 东方财富','vendor',true) ON CONFLICT DO NOTHING`, get("EMQUANT_PROVIDER_ID")); err != nil {
			return err
		}
		var eligible bool
		if err := tx.QueryRow(ctx, `SELECT kind='vendor' AND enabled AND NOT synthetic AND allow_model_processing FROM providers WHERE id=$1`, get("EMQUANT_PROVIDER_ID")).Scan(&eligible); err != nil {
			return err
		}
		if !eligible {
			return errors.New("existing provider is disabled, synthetic or model use revoked")
		}
		for _, x := range []struct {
			key, dataset, method string
			fields               []string
		}{
			{"EMQUANT_SECURITY_LICENSE", "ref.security", "CSS", []string{"NAME", "LISTDATE", "DELISTDATE"}},
			{"EMQUANT_LIQUIDITY_LICENSE", "mkt.daily_bar", "CSD", []string{"VOLUME", "AMOUNT", "TURN"}},
		} {
			id, err := uuid.Parse(get(x.key))
			if err != nil || id == uuid.Nil {
				return errors.New("invalid license id")
			}
			_, err = tx.Exec(ctx, `INSERT INTO source_licenses(id,provider_id,product_code,dataset_id,vendor_method,field_names,instrument_scope,instrument_codes,evidence_ref,verified_by,verified_at,valid_from,valid_until,allow_storage,allow_display,allow_model_processing)
   VALUES($1,$2,$3,$4,$5,$6,'listed',$7,$8,$9,clock_timestamp(),clock_timestamp(),$10,true,true,true) ON CONFLICT DO NOTHING`, id, get("EMQUANT_PROVIDER_ID"), get("EMQUANT_PRODUCT"), x.dataset, x.method, x.fields, codes, get("EMQUANT_LICENSE_EVIDENCE"), get("EMQUANT_LICENSE_VERIFIED_BY"), until)
			if err != nil {
				return err
			}
			var same bool
			err = tx.QueryRow(ctx, `SELECT provider_id=$2 AND product_code=$3 AND dataset_id=$4 AND vendor_method=$5 AND field_names=$6 AND instrument_scope='listed' AND instrument_codes=$7 AND evidence_ref=$8 AND valid_until=$9 AND revoked_at IS NULL AND allow_storage AND allow_display AND allow_model_processing FROM source_licenses WHERE id=$1`, id, get("EMQUANT_PROVIDER_ID"), get("EMQUANT_PRODUCT"), x.dataset, x.method, x.fields, codes, get("EMQUANT_LICENSE_EVIDENCE"), until).Scan(&same)
			if err != nil {
				return err
			}
			if !same {
				return errors.New("existing license conflicts; use a new reviewed identity")
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "Brief source licenses registered; field semantics and realtime capability are not inferred.")
	return err
}
