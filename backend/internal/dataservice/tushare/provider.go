package tushare

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgxpool"

	ds "github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
)

// Provider is a configured source. Collect builds a fresh observation, which
// can only be published after all its required archives have been written.
type Provider struct {
	source     Source
	client     *client
	codes      []string
	allowModel bool
}

var instrument = regexp.MustCompile(`^[0-9]{6}\.(SH|SZ|BJ)$`)

func Open(pool *pgxpool.Pool, get func(string) string) (*Provider, error) {
	for _, k := range []string{"TUSHARE_TOKEN", "TUSHARE_S3_ENDPOINT", "TUSHARE_S3_REGION", "TUSHARE_S3_ACCESS_KEY", "TUSHARE_S3_SECRET_KEY", "TUSHARE_S3_BUCKET", "TUSHARE_SUBJECTS"} {
		if get(k) == "" || strings.TrimSpace(get(k)) != get(k) {
			return nil, fmt.Errorf("missing or invalid %s", k)
		}
	}
	endpoint, err := url.Parse(get("TUSHARE_S3_ENDPOINT"))
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("invalid TUSHARE_S3_ENDPOINT")
	}
	codes := strings.Split(get("TUSHARE_SUBJECTS"), ",")
	for i, code := range codes {
		codes[i] = strings.ToUpper(strings.TrimSpace(code))
		if !instrument.MatchString(codes[i]) {
			return nil, errors.New("TUSHARE_SUBJECTS expects exchange-qualified A-share codes")
		}
	}
	slices.Sort(codes)
	codes = slices.Compact(codes)
	interval := 500 * time.Millisecond
	if v := get("TUSHARE_MIN_INTERVAL"); v != "" {
		interval, err = time.ParseDuration(v)
		if err != nil || interval < 100*time.Millisecond || interval > time.Minute {
			return nil, errors.New("invalid TUSHARE_MIN_INTERVAL")
		}
	}
	credentials := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: get("TUSHARE_S3_ACCESS_KEY"), SecretAccessKey: get("TUSHARE_S3_SECRET_KEY")}, nil
	})
	object := s3.New(s3.Options{Region: get("TUSHARE_S3_REGION"), Credentials: credentials, BaseEndpoint: aws.String(endpoint.String()), UsePathStyle: true})
	archive, err := emquant.NewBinaryRawStore(object, get("TUSHARE_S3_BUCKET"), "tushare")
	if err != nil {
		return nil, err
	}
	// Serialize all TuShare calls from this deployment, including different
	// processes using the same account. No account secret enters the gate.
	gate := emquant.PostgreSQLRateGate{Pool: pool, Scope: "tushare-pro", MinInterval: interval, RateLimitCooldown: time.Minute}
	httpClient := &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	c := &client{token: get("TUSHARE_TOKEN"), http: httpClient, archive: archive, gate: func(ctx context.Context, f func() error) error {
		err := gate.Do(ctx, func() error {
			e := f()
			var pe *ds.ProviderError
			if errors.As(e, &pe) && pe.Class == ds.ErrRateLimited {
				return errors.Join(e, emquant.ErrRateLimited)
			}
			return e
		})
		return err
	}}
	return &Provider{source: c, client: c, codes: codes, allowModel: get("TUSHARE_ALLOW_MODEL_PROCESSING") == "true"}, nil
}

// Probe checks the configured account and first registered security without publishing or archiving data.
func (p *Provider) Probe(ctx context.Context) error {
	return p.client.probeSecurity(ctx, p.codes[0])
}

func (*Provider) ID() string                   { return "tushare" }
func (*Provider) Name() string                 { return "TuShare Pro" }
func (*Provider) Synthetic() bool              { return false }
func (p *Provider) AllowModelProcessing() bool { return p.allowModel }
func (*Provider) Basis() map[string]string {
	return map[string]string{"mkt.valuation_daily": marketValuationBasis, "flow.order_size": marketOrderBasis}
}
func (*Provider) Now() time.Time           { return time.Now().UTC() }
func (p *Provider) SubjectCodes() []string { return slices.Clone(p.codes) }

var errUnprepared = errors.New("tushare: use the archived collection and publication operation")

func (*Provider) Entities(context.Context) ([]ds.Entity, error)         { return nil, errUnprepared }
func (*Provider) Records(context.Context) ([]ds.Record, error)          { return nil, errUnprepared }
func (*Provider) Capabilities(context.Context) ([]ds.Capability, error) { return nil, errUnprepared }

type observation struct {
	*Provider
	batch Batch
}

func (o observation) Entities(context.Context) ([]ds.Entity, error) { return o.batch.Entities, nil }
func (o observation) Records(context.Context) ([]ds.Record, error)  { return o.batch.Records, nil }
func (o observation) Capabilities(context.Context) ([]ds.Capability, error) {
	caps := []ds.Capability{}
	seen := map[[2]string]bool{}
	for _, r := range o.batch.Records {
		k := [2]string{r.DatasetID, r.EntityCode}
		if !seen[k] {
			seen[k] = true
			caps = append(caps, ds.Capability{DatasetID: r.DatasetID, EntityCode: r.EntityCode})
		}
	}
	return caps, nil
}

func (p *Provider) Collect(ctx context.Context, code string, end time.Time) (ds.Provider, []string, error) {
	if !slices.Contains(p.codes, code) {
		return nil, nil, errors.New("tushare: unregistered research subject")
	}
	batch, err := collectMarket(ctx, p.source, code, end)
	if err != nil {
		return nil, nil, err
	}
	for _, subject := range append([]string{code}, batch.Peers...) {
		filings, err := collectFilings(ctx, p.source, subject, end, subject != code)
		if err != nil {
			var pe *ds.ProviderError
			if !errors.As(err, &pe) || pe.Class != ds.ErrNotCovered {
				return nil, nil, err
			}
			batch.Notes = append(batch.Notes, subject+" 财务或公告接口权限不足")
		}
		batch.Records = append(batch.Records, filings.Records...)
		batch.Notes = append(batch.Notes, filings.Notes...)
	}
	for _, entity := range batch.Entities {
		if entity.Code == code {
			news, err := collectNews(ctx, p.source, entity, end)
			if err != nil {
				var pe *ds.ProviderError
				if !errors.As(err, &pe) || pe.Class != ds.ErrNotCovered {
					return nil, nil, err
				}
				batch.Notes = append(batch.Notes, "账号未开通新闻通讯")
			} else {
				batch.Records = append(batch.Records, news...)
			}
		}
	}
	// A configured target may also appear in another configured target's peer
	// set. Publishing peer metadata must not turn its research entry off.
	for i := range batch.Entities {
		entity := &batch.Entities[i]
		if entity.Kind == "stock" && slices.Contains(p.codes, entity.Code) {
			entity.Attrs["tushare_research_subject"] = true
		}
	}
	return observation{p, batch}, batch.Notes, nil
}

// major_news does not supply a security identifier. Attach only articles
// whose title explicitly names this security, preserving the media attribution.
func collectNews(ctx context.Context, src Source, entity ds.Entity, end time.Time) ([]ds.Record, error) {
	loc := time.FixedZone("Asia/Shanghai", 8*60*60)
	const layout = "2006-01-02 15:04:05"
	end = end.In(loc)
	begin := end.AddDate(0, 0, -30)
	if utf8.RuneCountInString(entity.Name) < 2 {
		return nil, nil
	}
	var out []ds.Record
	for from := begin; from.Before(end); {
		until := from.AddDate(0, 0, 5)
		if until.After(end) {
			until = end
		}
		rows, err := src.Rows(ctx, "major_news", map[string]string{"start_date": from.Format(layout), "end_date": until.Format(layout)}, []string{"title", "content", "pub_time", "src"}, 400)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			v := row.Values
			if !strings.Contains(v["title"], entity.Name) || v["content"] == "" || v["src"] == "" {
				continue
			}
			at, err := time.ParseInLocation(layout, v["pub_time"], loc)
			if err != nil {
				return nil, classified(ds.ErrSchemaChanged, "major_news")
			}
			if at.After(end) || at.Before(begin) {
				continue
			}
			id := fmt.Sprintf("%x", sha256.Sum256([]byte(v["src"]+"|"+v["pub_time"]+"|"+v["title"])))
			out = append(out, ds.Record{DatasetID: "news.item", EntityCode: entity.Code, RecordKey: "tushare:news:" + entity.Code + ":" + id, PublishedAt: &at, Period: &at, AvailableAt: row.ReceivedAt, Origin: "media", SourceTier: "L3", Basis: "TuShare 新闻通讯，标题含证券全称或简称", MappingVersion: 1, RawRef: row.RawRef, RawSHA256: row.RawSHA256, RawLocator: row.RawLocator, Payload: map[string]any{"news_id": id, "title": v["title"], "source": v["src"], "published_at": at.Format(time.RFC3339), "body": v["content"]}})
		}
		from = until.Add(time.Second)
	}
	return out, nil
}
