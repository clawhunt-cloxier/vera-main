package emquant

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

// MappedRecord keeps provenance alongside a canonical payload until the data
// service has a persistent lineage model. It must not be flattened into the
// current records table, which cannot retain these fields. AvailableAt is a
// lower bound; the eventual database writer must use its actual commit time.
type MappedRecord struct {
	Record         dataservice.Record
	Source         ArchiveRef // includes request fingerprint and archive timestamps
	RawRef         string
	RawSHA256      string
	RawLocator     string
	MappingVersion string
}

// MapOpenDates maps confirmed exchange trading days only. It verifies the
// archived request, content and observation time before creating records.
// Missing days are not mapped to closed days, and no coverage is declared.
func MapOpenDates(body []byte, ref ArchiveRef, market, from, to string) ([]MappedRecord, error) {
	if market != "CNSESH" && market != "CNSESZ" {
		return nil, errors.New("emquant: unsupported calendar market")
	}
	if ref.BodyRef == "" || ref.ReceivedAt.IsZero() || ref.ArchivedAt.IsZero() || ref.ArchivedAt.Before(ref.ReceivedAt) {
		return nil, errors.New("emquant: missing archive provenance")
	}
	start, err := time.Parse("2006-01-02", from)
	if err != nil {
		return nil, errors.New("emquant: invalid calendar start")
	}
	end, err := time.Parse("2006-01-02", to)
	if err != nil || end.Before(start) || end.Sub(start) > 3660*24*time.Hour {
		return nil, errors.New("emquant: invalid calendar end")
	}
	req, err := json.Marshal(request{Method: "tradedates", From: from, To: to, Options: "Market=" + market + ",Period=1,Order=1"})
	if err != nil || ref.RequestFingerprint != digest(req) {
		return nil, errors.New("emquant: calendar request provenance mismatch")
	}
	cube, observed, err := DecodeArchive(body, ref.BodySHA256)
	if err != nil {
		return nil, err
	}
	var envelope archiveBody
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Format != "emquant-sdk-decoded-v2" || envelope.RequestFingerprint != ref.RequestFingerprint {
		return nil, errors.New("emquant: calendar archive is not bound to request")
	}
	if !observed.Equal(ref.ReceivedAt) {
		return nil, errors.New("emquant: calendar observation time mismatch")
	}
	dates, err := openDates(cube, start, end)
	if err != nil {
		return nil, err
	}
	out := make([]MappedRecord, 0, len(dates))
	for i, day := range dates {
		period, _ := time.Parse("2006-01-02", day)
		out = append(out, MappedRecord{
			Record: dataservice.Record{DatasetID: "ref.trading_calendar", RecordKey: day, Version: 1, Period: &period,
				AvailableAt: ref.ArchivedAt.UTC(), Origin: "exchange", SourceTier: "L2", Basis: "Choice tradedates; market=" + market,
				Payload: map[string]any{"trade_date": day, "is_open": true}},
			Source: ref, RawRef: ref.BodyRef, RawSHA256: ref.BodySHA256, RawLocator: fmt.Sprintf("$.data.Values[%d]", i), MappingVersion: "emquant-tradedates-v1",
		})
	}
	return out, nil
}
