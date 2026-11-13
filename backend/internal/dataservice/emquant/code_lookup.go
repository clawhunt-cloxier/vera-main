package emquant

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// CEC completion may return multiple suffixes. Restrict inputs to short
// numeric stock codes and return the vendor table without selecting a match.
const stockCodeOptions = "ReturnType=1,SecuType=1,SecuMarket=1"

func validateStockCodeQuery(codes string) error {
	parts := strings.Split(codes, ",")
	if len(parts) == 0 || len(parts) > 32 {
		return errors.New("emquant: invalid stock code query")
	}
	seen := make(map[string]bool, len(parts))
	for _, part := range parts {
		if len(part) != 6 || seen[part] {
			return errors.New("emquant: invalid or duplicate stock code")
		}
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				return errors.New("emquant: invalid stock code")
			}
		}
		seen[part] = true
	}
	return nil
}

type codeFetcher interface {
	CheckStockCodes(codes string) (Table, error)
}

type tableArchiveBody struct {
	Format             string    `json:"format"`
	RequestFingerprint string    `json:"request_fingerprint"`
	ReceivedAt         time.Time `json:"received_at"`
	Data               Table     `json:"data"`
}

func validateTable(table Table) error {
	if table.Rows < 0 || table.Rows > 1000000 || len(table.Columns) > 1000000 || int64(table.Rows)*int64(len(table.Columns)) != int64(len(table.Values)) || len(table.Values) > 1000000 {
		return fmt.Errorf("%w: invalid table dimensions", ErrShape)
	}
	return nil
}

// CollectStockCodeCandidates returns the untrusted vendor table only after it
// has been archived. It does not choose a candidate or add an entity to the
// catalog; the caller must validate permissions, market and ambiguity.
func (c Collector) CollectStockCodeCandidates(ctx context.Context, codes string) (Table, ArchiveRef, error) {
	f, ok := c.Fetcher.(codeFetcher)
	if !ok || c.Store == nil {
		return Table{}, ArchiveRef{}, errors.New("emquant: code lookup and raw store are required")
	}
	if err := validateStockCodeQuery(codes); err != nil {
		return Table{}, ArchiveRef{}, err
	}
	var table Table
	err := c.callVendor(ctx, func() error {
		var e error
		table, e = f.CheckStockCodes(codes)
		return e
	})
	if err != nil {
		return Table{}, ArchiveRef{}, err
	}
	if err := validateTable(table); err != nil {
		return Table{}, ArchiveRef{}, err
	}
	received := time.Now().UTC()
	req, err := json.Marshal(request{Method: "cec", Codes: codes, Options: stockCodeOptions})
	if err != nil {
		return Table{}, ArchiveRef{}, err
	}
	fingerprint := digest(req)
	body, err := json.Marshal(tableArchiveBody{Format: "emquant-sdk-table-v1", RequestFingerprint: fingerprint, ReceivedAt: received, Data: table})
	if err != nil || len(body) > maxArchiveSize {
		return Table{}, ArchiveRef{}, errors.New("emquant: invalid code lookup archive")
	}
	hash := digest(body)
	ref, err := c.Store.Put(ctx, hash, body)
	if err != nil {
		return Table{}, ArchiveRef{}, fmt.Errorf("emquant: code lookup archive failed: %w", err)
	}
	if ref == "" {
		return Table{}, ArchiveRef{}, errors.New("emquant: code lookup archive returned empty reference")
	}
	if err := ctx.Err(); err != nil {
		return Table{}, ArchiveRef{}, err
	}
	archived := time.Now().UTC()
	if archived.Before(received) {
		return Table{}, ArchiveRef{}, errors.New("emquant: invalid archive time")
	}
	return table, ArchiveRef{RequestFingerprint: fingerprint, BodySHA256: hash, BodyRef: ref, ReceivedAt: received, ArchivedAt: archived}, nil
}

// DecodeTableArchive validates an SDK table archive without converting nulls,
// integer precision, or ambiguous code candidates to fabricated entities.
func DecodeTableArchive(body []byte, expectedHash string) (Table, time.Time, string, error) {
	if len(body) > maxArchiveSize || len(expectedHash) != 64 || digest(body) != expectedHash {
		return Table{}, time.Time{}, "", errors.New("emquant: invalid table archive hash or size")
	}
	var raw tableArchiveBody
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return Table{}, time.Time{}, "", fmt.Errorf("emquant: invalid table archive: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return Table{}, time.Time{}, "", errors.New("emquant: trailing table archive content")
	}
	hash, err := hex.DecodeString(raw.RequestFingerprint)
	if raw.Format != "emquant-sdk-table-v1" || raw.ReceivedAt.IsZero() || err != nil || len(hash) != 32 || raw.RequestFingerprint != strings.ToLower(raw.RequestFingerprint) {
		return Table{}, time.Time{}, "", errors.New("emquant: invalid table archive metadata")
	}
	if err := validateTable(raw.Data); err != nil {
		return Table{}, time.Time{}, "", err
	}
	if err := restoreArchiveCells(raw.Data.Values); err != nil {
		return Table{}, time.Time{}, "", err
	}
	return raw.Data, raw.ReceivedAt, raw.RequestFingerprint, nil
}
