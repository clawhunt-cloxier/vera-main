package emquant

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

// OpenBriefFromEnv opens the BriefService from environment variables and the
// independently registered security and liquidity license identities.
func OpenBriefFromEnv(ctx context.Context, pool *pgxpool.Pool, cat *dataservice.Catalog, get func(string) string) (*BriefService, error) {
	keys := []string{"EMQUANT_LIBRARY", "EMQUANT_USER", "EMQUANT_PASSWORD", "EMQUANT_ACCOUNT_SCOPE", "EMQUANT_S3_ENDPOINT", "EMQUANT_S3_REGION", "EMQUANT_S3_ACCESS_KEY", "EMQUANT_S3_SECRET_KEY", "EMQUANT_S3_BUCKET", "EMQUANT_PROVIDER_ID", "EMQUANT_PRODUCT", "EMQUANT_MIN_INTERVAL", "EMQUANT_RATE_COOLDOWN", "EMQUANT_SECURITY_LICENSE", "EMQUANT_LIQUIDITY_LICENSE"}
	for _, k := range keys {
		if get(k) == "" || strings.TrimSpace(get(k)) != get(k) {
			return nil, fmt.Errorf("missing or invalid %s", k)
		}
	}
	if !filepath.IsAbs(get("EMQUANT_LIBRARY")) {
		return nil, errors.New("EMQUANT_LIBRARY must be absolute")
	}
	endpoint, err := url.Parse(get("EMQUANT_S3_ENDPOINT"))
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("invalid EMQUANT_S3_ENDPOINT")
	}
	interval, e1 := time.ParseDuration(get("EMQUANT_MIN_INTERVAL"))
	cooldown, e2 := time.ParseDuration(get("EMQUANT_RATE_COOLDOWN"))
	if e1 != nil || e2 != nil || interval < 90*time.Millisecond || cooldown < interval || cooldown > 24*time.Hour {
		return nil, errors.New("invalid Choice rate intervals")
	}
	security, e1 := uuid.Parse(get("EMQUANT_SECURITY_LICENSE"))
	liquidity, e2 := uuid.Parse(get("EMQUANT_LIQUIDITY_LICENSE"))
	if e1 != nil || e2 != nil || security == uuid.Nil || liquidity == uuid.Nil {
		return nil, errors.New("invalid brief license identities")
	}
	credentials := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: get("EMQUANT_S3_ACCESS_KEY"), SecretAccessKey: get("EMQUANT_S3_SECRET_KEY"), Source: "environment"}, nil
	})
	object := s3.New(s3.Options{Region: get("EMQUANT_S3_REGION"), Credentials: credentials, BaseEndpoint: aws.String(get("EMQUANT_S3_ENDPOINT")), UsePathStyle: true})
	runtime, err := OpenRuntime(ctx, RuntimeConfig{Library: get("EMQUANT_LIBRARY"), Username: get("EMQUANT_USER"), Password: get("EMQUANT_PASSWORD"), Database: pool, ObjectClient: object, Bucket: get("EMQUANT_S3_BUCKET"), Prefix: get("EMQUANT_S3_PREFIX"), AccountScope: get("EMQUANT_ACCOUNT_SCOPE"), MinInterval: interval, RateLimitCooldown: cooldown})
	if err != nil {
		return nil, err
	}
	return &BriefService{Runtime: runtime, Catalog: cat, ProviderID: get("EMQUANT_PROVIDER_ID"), Product: get("EMQUANT_PRODUCT"), SecurityLicense: security, LiquidityLicense: liquidity}, nil
}
