module github.com/varrcan/koreader-sync-board/server

go 1.27.3

// У fb2cng module path «fbc», поэтому require по GitHub-пути не проходит.
replace fbc => github.com/varrcan/fb2cng v1.9.6-0.20261109161749-204a0895aecf

require (
	fbc v0.0.0
	github.com/coreos/go-oidc/v3 v3.21.0
	github.com/go-jose/go-jose/v4 v4.1.9
	github.com/oapi-codegen/runtime v1.8.0
	go.uber.org/zap v1.28.0
	golang.org/x/crypto v0.58.0
	golang.org/x/image v0.47.0
	golang.org/x/oauth2 v0.37.0
	golang.org/x/sync v0.24.0
	golang.org/x/text v0.43.0
	modernc.org/sqlite v1.61.0
)

require (
	github.com/apapsch/go-jsonmerge/v2 v2.0.0 // indirect
	github.com/beevik/etree v1.8.1 // indirect
	github.com/disintegration/imaging v1.6.2 // indirect
	github.com/dprotaso/go-yit v0.0.0-20220510233725-9ba8df137936 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/gabriel-vasile/mimetype v1.4.16 // indirect
	github.com/getkin/kin-openapi v0.142.0 // indirect
	github.com/go-openapi/jsonpointer v0.23.1 // indirect
	github.com/go-openapi/swag/jsonname v0.26.0 // indirect
	github.com/go-playground/locales v0.14.2 // indirect
	github.com/go-playground/universal-translator v0.18.2 // indirect
	github.com/go-playground/validator/v10 v10.30.5 // indirect
	github.com/go-task/slim-sprig/v3 v3.0.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gosimple/slug v1.15.0 // indirect
	github.com/gosimple/unidecode v1.0.1 // indirect
	github.com/h2non/filetype v1.1.3 // indirect
	github.com/hidez8891/zip v1.11.0 // indirect
	github.com/leodido/go-urn v1.5.0 // indirect
	github.com/maruel/natural v1.3.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/neurosnap/sentences v1.1.2 // indirect
	github.com/oapi-codegen/oapi-codegen/v2 v2.8.1 // indirect
	github.com/oasdiff/yaml v0.1.1 // indirect
	github.com/oasdiff/yaml3 v0.0.14 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/rupor-github/gencfg v1.0.19 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	github.com/speakeasy-api/jsonpath v0.6.3 // indirect
	github.com/speakeasy-api/openapi v1.24.0 // indirect
	github.com/srwiley/oksvg v0.0.0-20221011165216-be6e8873101c // indirect
	github.com/srwiley/rasterx v0.0.0-20220730225603-2ab79fcdd4ef // indirect
	github.com/tdewolff/parse/v2 v2.8.19 // indirect
	github.com/vmware-labs/yaml-jsonpath v0.3.2 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/term v0.47.0 // indirect
	golang.org/x/tools v0.52.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	modernc.org/libc v1.79.0 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

tool github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen
