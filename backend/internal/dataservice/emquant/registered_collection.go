package emquant

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

// RegisteredArchive ties an archived result to the registry identity. It is
// not a published research record or proof of data-product permissions.
type RegisteredArchive struct {
	ID        uuid.UUID
	DatasetID string
	Source    ArchiveRef
	LicenseID uuid.UUID
	Product   string
}

// RegisteredCollector exposes data only after both object storage and the
// database registry have accepted it. A failed database write can leave an
// orphaned object, which must not be treated as a published research record.
type RegisteredCollector struct {
	Collector Collector
	Registry  ArchiveRegistry
	LicenseID uuid.UUID
	Product   string
}

func (c RegisteredCollector) validate(dataset string) error {
	if err := c.validateConfig(); err != nil {
		return err
	}
	if _, ok := c.Registry.Catalog.Dataset(dataset); !ok {
		return errors.New("emquant: collection dataset is not in catalog")
	}
	return nil
}

func (c RegisteredCollector) validateConfig() error {
	store, ok := c.Collector.Store.(*S3RawStore)
	if c.Collector.Gate == nil || c.Registry.Pool == nil || c.Registry.Store == nil || c.Registry.Catalog == nil || !ok || store != c.Registry.Store || c.Registry.ProviderID == "" || len(c.Registry.ProviderID) > 128 || strings.TrimSpace(c.Registry.ProviderID) != c.Registry.ProviderID || c.LicenseID == uuid.Nil || c.Product == "" || len(c.Product) > 128 || strings.TrimSpace(c.Product) != c.Product {
		return errors.New("emquant: registered collector is not configured")
	}
	return nil
}

func (c RegisteredCollector) storageGrant(ctx context.Context, dataset, method, codes, indicators string) (dataservice.SourceUseRequest, error) {
	if err := c.validate(dataset); err != nil {
		return dataservice.SourceUseRequest{}, err
	}
	if err := validateIndicators(indicators); err != nil {
		return dataservice.SourceUseRequest{}, err
	}
	fields := strings.Split(indicators, ",")
	for i := range fields {
		fields[i] = strings.ToUpper(strings.TrimSpace(fields[i]))
	}
	instruments := strings.Split(codes, ",")
	for i := range instruments {
		instruments[i] = strings.ToUpper(strings.TrimSpace(instruments[i]))
	}
	grant := dataservice.SourceUseRequest{LicenseID: c.LicenseID, ProviderID: c.Registry.ProviderID, Product: c.Product, DatasetID: dataset, Method: method, Fields: fields, Codes: instruments, Use: dataservice.SourceStorage}
	if err := pgx.BeginFunc(ctx, c.Registry.Pool, func(tx pgx.Tx) error { return dataservice.RequireSourceUse(ctx, tx, grant) }); err != nil {
		return dataservice.SourceUseRequest{}, err
	}
	return grant, nil
}

func (c RegisteredCollector) CollectCSD(ctx context.Context, dataset, codes, indicators, from, to, options string) (Cube, RegisteredArchive, error) {
	grant, err := c.storageGrant(ctx, dataset, "CSD", codes, indicators)
	if err != nil {
		return Cube{}, RegisteredArchive{}, err
	}
	cube, ref, err := c.Collector.CollectCSD(ctx, codes, indicators, from, to, options)
	if err != nil {
		return Cube{}, RegisteredArchive{}, err
	}
	expected := request{Method: "csd", Codes: codes, Indicators: indicators, From: from, To: to, Options: options}
	id, err := c.Registry.registerCube(ctx, dataset, ref, &grant, &expected)
	if err != nil {
		return Cube{}, RegisteredArchive{}, err
	}
	return cube, RegisteredArchive{ID: id, DatasetID: dataset, Source: ref, LicenseID: c.LicenseID, Product: c.Product}, nil
}

func (c RegisteredCollector) CollectQuote(ctx context.Context, dataset, codes, indicators, options string) (Quote, RegisteredArchive, error) {
	if dataset != "mkt.intraday_quote" && dataset != "mkt.intraday_index_quote" {
		return Quote{}, RegisteredArchive{}, errors.New("emquant: quote requires an intraday dataset")
	}
	grant, err := c.storageGrant(ctx, dataset, "CSQSNAPSHOT", codes, indicators)
	if err != nil {
		return Quote{}, RegisteredArchive{}, err
	}
	quote, ref, err := c.Collector.CollectQuote(ctx, codes, indicators, options)
	if err != nil {
		return Quote{}, RegisteredArchive{}, err
	}
	expected := request{Method: "csqsnapshot", Codes: codes, Indicators: indicators, Options: options}
	id, err := c.Registry.registerCube(ctx, dataset, ref, &grant, &expected)
	if err != nil {
		return Quote{}, RegisteredArchive{}, err
	}
	return quote, RegisteredArchive{ID: id, DatasetID: dataset, Source: ref, LicenseID: c.LicenseID, Product: c.Product}, nil
}
