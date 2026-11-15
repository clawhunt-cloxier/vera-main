package dataservice

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type SourceUse string

const (
	SourceStorage SourceUse = "storage"
	SourceDisplay SourceUse = "display"
	SourceModel   SourceUse = "model"
)

// SourceUseRequest must name the independently configured license and the
// exact provider, product, dataset, vendor method and fields being used.
type SourceUseRequest struct {
	LicenseID  uuid.UUID
	ProviderID string
	Product    string
	DatasetID  string
	Method     string
	Fields     []string
	Codes      []string
	Use        SourceUse
}

var sourceFieldName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)
var sourceInstrumentCode = regexp.MustCompile(`^[A-Z0-9][A-Z0-9._-]{0,63}$`)

// RequireSourceUse verifies current rights using the database clock and locks
// both the license and provider until tx ends. The caller must carry out its
// protected database action in the same transaction. For later display or
// model use, callers must open a new transaction and check again; a prior
// grant or a historical snapshot never overrides a current revocation.
// Evidence references are trusted only when maintained by the control plane;
// this function cannot independently authenticate the underlying agreement.
func RequireSourceUse(ctx context.Context, tx pgx.Tx, req SourceUseRequest) error {
	if tx == nil || req.LicenseID == uuid.Nil || req.ProviderID == "" || req.Product == "" || req.DatasetID == "" || len(req.Fields) == 0 || len(req.Fields) > 64 || len(req.Codes) == 0 || len(req.Codes) > 500 {
		return errors.New("data source authorization: incomplete scope")
	}
	if req.Use != SourceStorage && req.Use != SourceDisplay && req.Use != SourceModel {
		return errors.New("data source authorization: invalid use")
	}
	if req.Method != "CSD" && req.Method != "CSS" && req.Method != "CSQSNAPSHOT" && req.Method != "TRADEDATES" && req.Method != "CFN" {
		return errors.New("data source authorization: invalid vendor method")
	}
	fields := make([]string, len(req.Fields))
	seen := make(map[string]bool, len(fields))
	for i, field := range req.Fields {
		field = strings.ToUpper(strings.TrimSpace(field))
		if !sourceFieldName.MatchString(field) || seen[field] {
			return errors.New("data source authorization: invalid or duplicate field")
		}
		seen[field] = true
		fields[i] = field
	}
	codes := make([]string, len(req.Codes))
	seenCodes := make(map[string]bool, len(codes))
	for i, code := range req.Codes {
		code = strings.ToUpper(strings.TrimSpace(code))
		if !sourceInstrumentCode.MatchString(code) || seenCodes[code] {
			return errors.New("data source authorization: invalid or duplicate instrument")
		}
		seenCodes[code] = true
		codes[i] = code
	}
	var id uuid.UUID
	err := tx.QueryRow(ctx, `SELECT l.id FROM source_licenses l JOIN providers p ON p.id=l.provider_id
		WHERE l.id=$1 AND l.provider_id=$2 FOR SHARE OF l,p`, req.LicenseID, req.ProviderID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("data source authorization: no current grant for the requested scope and use")
	}
	if err != nil {
		return fmt.Errorf("data source authorization: %w", err)
	}
	// Re-evaluate time after acquiring locks: a concurrent writer could have
	// made us wait until a grant expired, so statement start is not sufficient.
	err = tx.QueryRow(ctx, `SELECT l.id FROM source_licenses l
		JOIN providers p ON p.id=l.provider_id
		WHERE l.id=$1 AND l.provider_id=$2 AND l.product_code=$3 AND l.dataset_id=$4 AND l.vendor_method=$5
		  AND $6::text[] <@ l.field_names AND (l.instrument_scope='all' OR $8::text[] <@ l.instrument_codes) AND l.revoked_at IS NULL
		  AND l.verified_at <= clock_timestamp() AND l.valid_from <= clock_timestamp()
		  AND (l.valid_until IS NULL OR l.valid_until > clock_timestamp())
		  AND p.kind='vendor' AND p.enabled AND NOT p.synthetic
		  AND CASE $7 WHEN 'storage' THEN l.allow_storage WHEN 'display' THEN l.allow_display
		      WHEN 'model' THEN l.allow_model_processing AND p.allow_model_processing ELSE false END
		FOR SHARE OF l,p`, req.LicenseID, req.ProviderID, req.Product, req.DatasetID, req.Method, fields, string(req.Use), codes).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("data source authorization: no current grant for the requested scope and use")
	}
	if err != nil {
		return fmt.Errorf("data source authorization: %w", err)
	}
	return nil
}
