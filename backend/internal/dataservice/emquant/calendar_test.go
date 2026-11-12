package emquant

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

func TestCollectOpenDatesChecksResponseAndPreservesArchive(t *testing.T) {
	f := &testFetcher{cube: Cube{Codes: []string{""}, Indicators: []string{"TRADEDATE"}, Dates: []string{"2026/11/9", "2026/11/10"}, Values: []Cell{{Type: 13, Value: "2026/11/9"}, {Type: 13, Value: "2026/11/10"}}}}
	s := &testRawStore{ref: "object://test/calendar"}
	c := Collector{Fetcher: f, Store: s}
	days, ref, err := c.CollectOpenDates(context.Background(), "CNSESH", "2026-11-09", "2026-11-15")
	if err != nil || len(days) != 2 || days[0] != "2026-11-09" || days[1] != "2026-11-10" || ref.BodySHA256 != digest(s.body) {
		t.Fatalf("calendar %v %+v %v", days, ref, err)
	}
	cube, _, err := DecodeArchive(s.body, ref.BodySHA256)
	if err != nil {
		t.Fatal(err)
	}
	start, _ := time.Parse("2006-01-02", "2026-11-09")
	end, _ := time.Parse("2006-01-02", "2026-11-15")
	if _, err = openDates(cube, start, end); err != nil {
		t.Fatal(err)
	}
	records, err := MapOpenDates(s.body, ref, "CNSESH", "2026-11-09", "2026-11-15")
	if err != nil || len(records) != 2 {
		t.Fatalf("mapping: %v", err)
	}
	catalog, err := dataservice.LoadCatalog("../../../../contracts/datasets/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	for i, mapped := range records {
		if err := catalog.Validate(mapped.Record); err != nil {
			t.Fatal(err)
		}
		if mapped.Record.Payload["is_open"] != true || mapped.RawRef != ref.BodyRef || mapped.RawSHA256 != ref.BodySHA256 || mapped.RawLocator != fmt.Sprintf("$.data.Values[%d]", i) || !mapped.Record.AvailableAt.Equal(ref.ReceivedAt) {
			t.Fatalf("invalid provenance: %+v", mapped)
		}
	}
	if _, err = MapOpenDates(s.body, ref, "CNSESZ", "2026-11-09", "2026-11-15"); err == nil {
		t.Fatal("mismatched market accepted")
	}
	forged := ref
	req, _ := json.Marshal(request{Method: "tradedates", From: "2026-11-09", To: "2026-11-15", Options: "Market=CNSESZ,Period=1,Order=1"})
	forged.RequestFingerprint = digest(req)
	if _, err = MapOpenDates(s.body, forged, "CNSESZ", "2026-11-09", "2026-11-15"); err == nil {
		t.Fatal("archive reused for another market")
	}
	calls := f.calls
	if _, _, err = c.CollectOpenDates(context.Background(), "UNKNOWN", "2026-11-09", "2026-11-15"); err == nil || f.calls != calls {
		t.Fatal("unsupported market reached SDK")
	}
	f.cube.Values[1].Value = "2026/11/9"
	if days, _, err = c.CollectOpenDates(context.Background(), "CNSESH", "2026-11-09", "2026-11-15"); err == nil || days != nil {
		t.Fatal("mismatched dates accepted")
	}
}
