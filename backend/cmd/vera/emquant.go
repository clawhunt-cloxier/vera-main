package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/audit"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
)

const emquantHelp = `vera emquant quote --entity UUID --code CODE --license UUID --policy UUID
vera emquant publish --candidate UUID
vera emquant init-brief （仅依据已核实范围显式登记真实成交简报许可）

从已登记并验证的 Choice 产品采集一只证券行情，归档并发布候选；不创建研究快照。
数据库、SDK、账号、对象存储、产品及限流配置通过环境变量提供，详见 docs/EMQUANT_INTEGRATION.md。
`

type emquantEnv struct {
	database, library, user, password, scope, endpoint, region, access, secret, bucket, prefix, provider, product, contracts string
	interval, cooldown                                                                                                       time.Duration
}

func (emquantEnv) String() string   { return "emquantEnv{redacted}" }
func (emquantEnv) GoString() string { return "emquantEnv{redacted}" }

func readArchiveEnv(get func(string) string) (emquantEnv, error) {
	var c emquantEnv
	fields := []struct {
		name   string
		target *string
	}{
		{"DATABASE_URL", &c.database}, {"EMQUANT_S3_ENDPOINT", &c.endpoint}, {"EMQUANT_S3_REGION", &c.region},
		{"EMQUANT_S3_ACCESS_KEY", &c.access}, {"EMQUANT_S3_SECRET_KEY", &c.secret}, {"EMQUANT_S3_BUCKET", &c.bucket},
	}
	for _, field := range fields {
		*field.target = get(field.name)
		if *field.target == "" || strings.TrimSpace(*field.target) != *field.target {
			return emquantEnv{}, fmt.Errorf("missing or invalid %s", field.name)
		}
	}
	c.prefix = get("EMQUANT_S3_PREFIX")
	endpoint, err := url.Parse(c.endpoint)
	if err != nil || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return emquantEnv{}, errors.New("invalid EMQUANT_S3_ENDPOINT")
	}
	return c, nil
}

func readEmquantEnv(get func(string) string) (emquantEnv, error) {
	c, err := readArchiveEnv(get)
	if err != nil {
		return emquantEnv{}, err
	}
	fields := []struct {
		name   string
		target *string
	}{
		{"EMQUANT_LIBRARY", &c.library}, {"EMQUANT_USER", &c.user}, {"EMQUANT_PASSWORD", &c.password}, {"EMQUANT_ACCOUNT_SCOPE", &c.scope},
		{"EMQUANT_PROVIDER_ID", &c.provider}, {"EMQUANT_PRODUCT", &c.product}, {"VERA_CONTRACTS_DIR", &c.contracts},
	}
	for _, field := range fields {
		*field.target = get(field.name)
		if *field.target == "" || strings.TrimSpace(*field.target) != *field.target {
			return emquantEnv{}, fmt.Errorf("missing or invalid %s", field.name)
		}
	}
	if !filepath.IsAbs(c.library) {
		return emquantEnv{}, errors.New("EMQUANT_LIBRARY must be absolute")
	}
	c.interval, err = time.ParseDuration(get("EMQUANT_MIN_INTERVAL"))
	if err != nil || c.interval < 90*time.Millisecond || c.interval > 24*time.Hour {
		return emquantEnv{}, errors.New("invalid EMQUANT_MIN_INTERVAL")
	}
	c.cooldown, err = time.ParseDuration(get("EMQUANT_RATE_COOLDOWN"))
	if err != nil || c.cooldown < c.interval || c.cooldown > 24*time.Hour {
		return emquantEnv{}, errors.New("invalid EMQUANT_RATE_COOLDOWN")
	}
	return c, nil
}

func emquantObjectClient(c emquantEnv) *s3.Client {
	credentials := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: c.access, SecretAccessKey: c.secret, Source: "emquant-environment"}, nil
	})
	return s3.New(s3.Options{Region: c.region, Credentials: credentials, BaseEndpoint: aws.String(c.endpoint), UsePathStyle: true})
}

func emquantCommand(ctx context.Context, args []string, out io.Writer, get func(string) string) error {
	if len(args) == 0 || args[0] == "help" {
		_, err := io.WriteString(out, emquantHelp)
		return err
	}
	if args[0] == "init-brief" {
		return emquantInitBrief(ctx, out, get)
	}
	if args[0] == "publish" {
		return emquantPublish(ctx, args[1:], out, get)
	}
	if args[0] != "quote" {
		return errors.New("unknown emquant command")
	}
	fs := flag.NewFlagSet("emquant quote", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	entityText := fs.String("entity", "", "")
	code := fs.String("code", "", "")
	licenseText := fs.String("license", "", "")
	policyText := fs.String("policy", "", "")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		return errors.New("invalid emquant quote arguments")
	}
	entity, e1 := uuid.Parse(*entityText)
	license, e2 := uuid.Parse(*licenseText)
	policy, e3 := uuid.Parse(*policyText)
	if e1 != nil || e2 != nil || e3 != nil || entity == uuid.Nil || license == uuid.Nil || policy == uuid.Nil || *code == "" || strings.TrimSpace(*code) != *code || strings.ToUpper(*code) != *code || strings.ContainsAny(*code, ",\x00") {
		return errors.New("invalid quote entity, code, license or policy")
	}
	c, err := readEmquantEnv(get)
	if err != nil {
		return err
	}
	cat, err := dataservice.LoadCatalog(filepath.Join(c.contracts, "datasets", "catalog.json"))
	if err != nil {
		return errors.New("cannot load dataset catalog")
	}
	pool, err := db.Open(ctx, c.database)
	if err != nil {
		return errors.New("cannot connect to configured database")
	}
	defer pool.Close()
	runtime, err := emquant.OpenRuntime(ctx, emquant.RuntimeConfig{Library: c.library, Username: c.user, Password: c.password, Database: pool,
		ObjectClient: emquantObjectClient(c), Bucket: c.bucket, Prefix: c.prefix, AccountScope: c.scope, MinInterval: c.interval, RateLimitCooldown: c.cooldown})
	var result emquant.QuoteIngestResult
	if err == nil {
		result, err = runtime.CollectAndPublishQuote(ctx, cat, c.provider, c.product, license, policy, entity, *code)
		err = errors.Join(err, runtime.Close())
	}
	status := "success"
	if err != nil {
		status = "failure"
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	auditErr := pgx.BeginFunc(auditCtx, pool, func(tx pgx.Tx) error {
		return audit.Record(auditCtx, tx, nil, "vera-emquant", "emquant.quote.collect", "entity:"+entity.String(), status, map[string]any{"provider": c.provider, "license_id": license.String(), "policy_id": policy.String()})
	})
	if result.StageID != uuid.Nil {
		fmt.Fprintf(out, "candidate_id=%s\n", result.StageID)
	}
	if auditErr != nil {
		return errors.New("quote operation may have completed; audit recording failed")
	}
	if err != nil {
		return fmt.Errorf("quote collection failed: %w", err)
	}
	_, err = fmt.Fprintf(out, "available_at=%s\n", result.AvailableAt.UTC().Format(time.RFC3339Nano))
	return err
}

func emquantPublish(ctx context.Context, args []string, out io.Writer, get func(string) string) error {
	fs := flag.NewFlagSet("emquant publish", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	candidateText := fs.String("candidate", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errors.New("invalid emquant publish arguments")
	}
	candidate, err := uuid.Parse(*candidateText)
	if err != nil || candidate == uuid.Nil {
		return errors.New("invalid quote candidate")
	}
	c, err := readArchiveEnv(get)
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, c.database)
	if err != nil {
		return errors.New("cannot connect to configured database")
	}
	defer pool.Close()
	runtime, err := emquant.OpenArchiveRuntime(pool, emquantObjectClient(c), c.bucket, c.prefix)
	var available time.Time
	if err == nil {
		available, err = runtime.PublishStagedQuote(ctx, candidate)
	}
	status := "success"
	if err != nil {
		status = "failure"
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	auditErr := pgx.BeginFunc(auditCtx, pool, func(tx pgx.Tx) error {
		return audit.Record(auditCtx, tx, nil, "vera-emquant", "emquant.quote.publish", "candidate:"+candidate.String(), status, nil)
	})
	if auditErr != nil {
		return errors.New("publication may have completed; audit recording failed")
	}
	if err != nil {
		return fmt.Errorf("quote publication failed: %w", err)
	}
	_, err = fmt.Fprintf(out, "candidate_id=%s\navailable_at=%s\n", candidate, available.UTC().Format(time.RFC3339Nano))
	return err
}
