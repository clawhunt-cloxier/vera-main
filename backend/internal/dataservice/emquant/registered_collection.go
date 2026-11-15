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

// CollectDailyLiquidity preserves the confirmed volume, amount and turnover
// observations. They are not a complete daily bar or a published record.
func (c RegisteredCollector) CollectDailyLiquidity(ctx context.Context, code, from, to string) ([]DailyLiquidity, RegisteredArchive, error) {
	start, end, err := dailyLiquidityRange(code, from, to)
	if err != nil {
		return nil, RegisteredArchive{}, err
	}
	cube, source, err := c.CollectCSD(ctx, "mkt.daily_bar", code, dailyIndicators, from, to, dailyOptions)
	if err != nil {
		return nil, RegisteredArchive{}, err
	}
	rows, err := mapDailyLiquidity(cube, source.Source, code, start, end)
	if err != nil {
		return nil, RegisteredArchive{}, err
	}
	return rows, source, nil
}

// CollectMarginObservations registers the licensed source and preserves its
// cells. Unverified units and publication times prevent publication as records.
func (c RegisteredCollector) CollectMarginObservations(ctx context.Context, code, from, to string) (MarginObservations, RegisteredArchive, error) {
	start, end, err := marginRange(code, from, to)
	if err != nil {
		return MarginObservations{}, RegisteredArchive{}, err
	}
	cube, source, err := c.CollectCSD(ctx, "flow.margin", code, marginIndicators, from, to, marginOptions)
	if err != nil {
		return MarginObservations{}, RegisteredArchive{}, err
	}
	observations, err := mapMarginObservations(cube, source.Source, code, start, end)
	if err != nil {
		return MarginObservations{}, RegisteredArchive{}, err
	}
	return observations, source, nil
}

// CollectCSS archives and registers a cross-section only after the exact
// product, security, fields and storage use are authorized. Registration does
// not establish disclosure time, revision identity or PIT suitability.
func (c RegisteredCollector) CollectCSS(ctx context.Context, dataset, codes, indicators, options string) (Cube, RegisteredArchive, error) {
	grant, err := c.storageGrant(ctx, dataset, "CSS", codes, indicators)
	if err != nil {
		return Cube{}, RegisteredArchive{}, err
	}
	cube, ref, err := c.Collector.CollectCSS(ctx, codes, indicators, options)
	if err != nil {
		return Cube{}, RegisteredArchive{}, err
	}
	expected := request{Method: "css", Codes: codes, Indicators: indicators, Options: options}
	id, err := c.Registry.registerCube(ctx, dataset, ref, &grant, &expected)
	if err != nil {
		return Cube{}, RegisteredArchive{}, err
	}
	return cube, RegisteredArchive{ID: id, DatasetID: dataset, Source: ref, LicenseID: c.LicenseID, Product: c.Product}, nil
}

// CollectStatementFields returns two archived monetary fields, not a
// complete financial statement: the source does not establish disclosure
// time or which filing/revision the returned values belong to.
func (c RegisteredCollector) CollectStatementFields(ctx context.Context, code, reportDate string, statementType int) (StatementFields, RegisteredArchive, error) {
	options, err := statementOptions(code, reportDate, statementType)
	if err != nil {
		return StatementFields{}, RegisteredArchive{}, err
	}
	cube, source, err := c.CollectCSS(ctx, "fin.statement", code, statementIndicators, options)
	if err != nil {
		return StatementFields{}, RegisteredArchive{}, err
	}
	fields, err := mapStatementFields(cube, source.Source, code, reportDate, statementType)
	if err != nil {
		return StatementFields{}, RegisteredArchive{}, err
	}
	return fields, source, nil
}

// CollectSecurityBasics preserves partial current observations only. It does
// not infer listing or trading status, historical names, or other required
// ref.security fields.
func (c RegisteredCollector) CollectSecurityBasics(ctx context.Context, code string) (SecurityBasics, RegisteredArchive, error) {
	if !validSecurityRequest(code) {
		return SecurityBasics{}, RegisteredArchive{}, errors.New("emquant: invalid security")
	}
	cube, source, err := c.CollectCSS(ctx, "ref.security", code, securityIndicators, "")
	if err != nil {
		return SecurityBasics{}, RegisteredArchive{}, err
	}
	basics, err := mapSecurityBasics(cube, source.Source, code)
	if err != nil {
		return SecurityBasics{}, RegisteredArchive{}, err
	}
	return basics, source, nil
}

// CollectIndustryLevel retains a requested classification observation without
// inventing effective intervals or treating it as a complete industry record.
func (c RegisteredCollector) CollectIndustryLevel(ctx context.Context, code, asOf string, level int) (IndustryObservation, RegisteredArchive, error) {
	options, err := industryOptions(code, asOf, level)
	if err != nil {
		return IndustryObservation{}, RegisteredArchive{}, err
	}
	cube, source, err := c.CollectCSS(ctx, "ref.industry", code, industryIndicators, options)
	if err != nil {
		return IndustryObservation{}, RegisteredArchive{}, err
	}
	observation, err := mapIndustryLevel(cube, source.Source, code, asOf, level)
	if err != nil {
		return IndustryObservation{}, RegisteredArchive{}, err
	}
	return observation, source, nil
}

// CollectDisclosureObservation preserves unverified disclosure metadata. It
// cannot associate a filing timestamp or revision with a statement value.
func (c RegisteredCollector) CollectDisclosureObservation(ctx context.Context, code, period string) (DisclosureObservation, RegisteredArchive, error) {
	options, err := disclosureOptions(code, period)
	if err != nil {
		return DisclosureObservation{}, RegisteredArchive{}, err
	}
	cube, source, err := c.CollectCSS(ctx, "fin.statement", code, disclosureIndicators, options)
	if err != nil {
		return DisclosureObservation{}, RegisteredArchive{}, err
	}
	observation, err := mapDisclosureObservation(cube, source.Source, code, period)
	if err != nil {
		return DisclosureObservation{}, RegisteredArchive{}, err
	}
	return observation, source, nil
}

// CollectOpenDates registers only returned open dates for the specified
// authorized market. An empty result does not prove that any day is closed or
// that a complete exchange calendar is available.
func (c RegisteredCollector) CollectOpenDates(ctx context.Context, market, from, to string) ([]string, RegisteredArchive, error) {
	if _, _, err := calendarRange(market, from, to); err != nil {
		return nil, RegisteredArchive{}, err
	}
	grant, err := c.storageGrant(ctx, "ref.trading_calendar", "TRADEDATES", market, "TRADEDATE")
	if err != nil {
		return nil, RegisteredArchive{}, err
	}
	days, ref, err := c.Collector.CollectOpenDates(ctx, market, from, to)
	if err != nil {
		return nil, RegisteredArchive{}, err
	}
	expected := request{Method: "tradedates", From: from, To: to, Options: "Market=" + market + ",Period=1,Order=1"}
	id, err := c.Registry.registerCube(ctx, "ref.trading_calendar", ref, &grant, &expected)
	if err != nil {
		return nil, RegisteredArchive{}, err
	}
	return days, RegisteredArchive{ID: id, DatasetID: "ref.trading_calendar", Source: ref, LicenseID: c.LicenseID, Product: c.Product}, nil
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
