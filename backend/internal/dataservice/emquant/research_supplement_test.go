package emquant

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

type liquiditySupplementFetcher struct {
	testFetcher
}

func (f *liquiditySupplementFetcher) CSD(code, indicators, from, to, options string) (Cube, error) {
	f.calls++
	if code != "600000.SH" || indicators != dailyIndicators || options != dailyOptions || from >= to {
		return Cube{}, errors.New("unexpected daily liquidity request")
	}
	return Cube{Codes: []string{code}, Indicators: []string{"VOLUME", "AMOUNT", "TURN"}, Dates: []string{to},
		Values: []Cell{{Type: 8, Value: int64(100)}, {Type: 11, Value: 1250.25}, {Type: 11, Value: 0.5}}}, nil
}

func TestResearchSupplementReplayRequiresRegisteredEvidenceAndCurrentModelPermission(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	if _, err := pool.Exec(ctx, `INSERT INTO providers(id,name,kind,synthetic,allow_model_processing) VALUES('choice-supplement-test','Choice test','vendor',false,true)`); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var object []byte
	s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			object = body
		case http.MethodGet:
			_, _ = w.Write(object)
		default:
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		}
	}))
	defer s3.Close()
	runtime, err := OpenArchiveRuntime(pool, testS3Client(s3.URL, "test", "test"), "test-bucket", "supplement")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := dataservice.LoadCatalog("../../../../contracts/datasets/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	brief := &BriefService{Runtime: runtime, Catalog: cat, ProviderID: "choice-supplement-test", Product: "test-product"}
	const code = "600000.SH"
	fetcher := &liquiditySupplementFetcher{}
	// Only the fetcher is substituted; collection still uses the registered
	// path and the real database and archive verification.
	runtime.client = &Client{}
	runtime.collector = Collector{Fetcher: fetcher, Store: runtime.store, Gate: testGateFunc(func(_ context.Context, fn func() error) error { return fn() })}
	manifest, err := brief.CollectResearchSupplement(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	var m liquiditySupplementManifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	raw := m.Liquidity
	var registered time.Time
	if err := pool.QueryRow(ctx, `SELECT registered_at FROM raw_objects WHERE id=$1`, raw.ID).Scan(&registered); err != nil {
		t.Fatal(err)
	}
	archiveOnly, err := OpenArchiveRuntime(pool, testS3Client(s3.URL, "test", "test"), "test-bucket", "supplement")
	if err != nil {
		t.Fatal(err)
	}
	reader := &BriefService{Runtime: archiveOnly, Catalog: cat, ProviderID: brief.ID(), Product: brief.Product}
	replay := func(manifest liquiditySupplementManifest, subject string, cutoff time.Time, use dataservice.SourceUse) (dataservice.ResearchSupplement, error) {
		body, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		var out dataservice.ResearchSupplement
		err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			var err error
			out, err = reader.ReplayResearchSupplement(ctx, tx, subject, cutoff, body, use)
			return err
		})
		return out, err
	}
	cutoff := registered.Add(time.Second)
	got, err := replay(m, code, cutoff, dataservice.SourceDisplay)
	if err != nil {
		t.Fatal(err)
	}
	if got.Evidence.ID != raw.ID || got.Evidence.DatasetID != raw.DatasetID || got.Evidence.ContentHash != raw.Source.BodySHA256 ||
		!got.Evidence.ReceivedAt.Equal(raw.Source.ReceivedAt) || got.Evidence.Locator != "$.data.Values" || len(got.Rows) != 1 {
		t.Fatalf("wrong evidence or row count: %+v", got)
	}
	row := got.Rows[0]
	if row.Date != m.To || row.VolumeShares == nil || *row.VolumeShares != "100" || row.AmountCNY == nil || *row.AmountCNY != "1250.25" || row.TurnoverRatio == nil || *row.TurnoverRatio != "0.005" || fetcher.calls != 1 {
		t.Fatalf("daily liquidity changed during replay or required another vendor call: %+v; calls=%d", row, fetcher.calls)
	}
	if _, err := replay(m, code, registered.Add(-time.Microsecond), dataservice.SourceStorage); err == nil {
		t.Fatal("accepted evidence registered after the cutoff")
	}
	if _, err := replay(m, "600001.SH", cutoff, dataservice.SourceModel); err == nil {
		t.Fatal("accepted evidence for another subject")
	}
	for name, mutate := range map[string]func(*liquiditySupplementManifest){
		"archive ID": func(x *liquiditySupplementManifest) { x.Liquidity.ID = uuid.New() },
		"product":    func(x *liquiditySupplementManifest) { x.Liquidity.Product = "another-product" },
		"dataset":    func(x *liquiditySupplementManifest) { x.Liquidity.DatasetID = "mkt.valuation_daily" },
		"hash":       func(x *liquiditySupplementManifest) { x.Liquidity.Source.BodySHA256 = digest([]byte("wrong")) },
		"date range": func(x *liquiditySupplementManifest) { x.From, x.To = "2025-01-02", "2025-02-15" },
		"method": func(x *liquiditySupplementManifest) {
			req, _ := json.Marshal(request{Method: "css", Codes: code, Indicators: dailyIndicators, From: m.From, To: m.To, Options: dailyOptions})
			x.Liquidity.Source.RequestFingerprint = digest(req)
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := m
			mutate(&bad)
			if _, err := replay(bad, code, cutoff, dataservice.SourceModel); err == nil {
				t.Fatal("accepted mismatched evidence")
			}
		})
	}
	if _, err := pool.Exec(ctx, `UPDATE providers SET allow_model_processing=false WHERE id=$1`, brief.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := replay(m, code, cutoff, dataservice.SourceDisplay); err == nil {
		t.Fatal("display accepted model evidence after provider permission was revoked")
	}
	if _, err := brief.CollectResearchSupplement(ctx, code); err == nil || fetcher.calls != 1 {
		t.Fatal("revoked provider was called for another supplement")
	}
}
