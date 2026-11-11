module github.com/clawhunt-cloxier/vera-main/backend

go 1.27.3

require (
	github.com/coreos/go-oidc/v3 v3.21.0
	github.com/fergusstrange/embedded-postgres v1.34.0
	github.com/go-jose/go-jose/v4 v4.1.9
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.12.0
	github.com/pressly/goose/v3 v3.29.0
	github.com/riverqueue/river v0.51.1
	github.com/riverqueue/river/riverdriver/riverpgxv5 v0.51.1
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3
	github.com/shopspring/decimal v1.5.0
	github.com/tdewolff/canvas v0.0.0-20260923214215-09804640d00c
	golang.org/x/oauth2 v0.37.0
)

require (
	codeberg.org/go-pdf/fpdf v0.12.0 // indirect
	github.com/BurntSushi/freetype-go v0.0.0-20160129220410-b763ddbfe298 // indirect
	github.com/BurntSushi/graphics-go v0.0.0-20160129215708-b43f31a4a966 // indirect
	github.com/BurntSushi/xgb v0.0.0-20210121224620-deaf085860bc // indirect
	github.com/BurntSushi/xgbutil v0.0.0-20190907113008-ad855c713046 // indirect
	github.com/ByteArena/poly2tri-go v0.0.0-20170716161910-d102ad91854f // indirect
	github.com/andybalholm/brotli v1.2.6 // indirect
	github.com/benoitkugler/textlayout v0.3.2 // indirect
	github.com/benoitkugler/textprocessing v0.0.6 // indirect
	github.com/go-fonts/latin-modern v0.3.3 // indirect
	github.com/go-text/typesetting v0.3.4 // indirect
	github.com/golang/freetype v0.0.0-20170609003504-e2365dfdc4a0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	github.com/lib/pq v1.10.9 // indirect
	github.com/mfridman/interpolate v0.0.2 // indirect
	github.com/riverqueue/river/riverdriver v0.51.1 // indirect
	github.com/riverqueue/river/rivershared v0.51.1 // indirect
	github.com/riverqueue/river/rivertype v0.51.1 // indirect
	github.com/sethvargo/go-retry v0.5.0 // indirect
	github.com/srwiley/rasterx v0.0.0-20220730225603-2ab79fcdd4ef // indirect
	github.com/srwiley/scanx v0.0.0-20190309010443-e94503791388 // indirect
	github.com/tdewolff/font v0.0.0-20260822205238-d0d2f004b117 // indirect
	github.com/tdewolff/minify/v2 v2.24.17 // indirect
	github.com/tdewolff/parse/v2 v2.8.16 // indirect
	github.com/tidwall/gjson v1.20.0 // indirect
	github.com/tidwall/match v1.2.0 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	github.com/xi2/xz v0.0.0-20171230120015-48954b6210f8 // indirect
	github.com/yuin/goldmark v1.8.5 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/image v0.45.0 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	modernc.org/knuth v0.6.0 // indirect
	modernc.org/token v1.1.0 // indirect
	star-tex.org/x/tex v0.7.1 // indirect
)

replace github.com/tdewolff/font => ./third_party/tdewolff-font
