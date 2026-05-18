module github.com/ContinuumApp/continuum-plugin-local-ebooks

go 1.26.0

replace github.com/ContinuumApp/continuum-plugin-sdk => /opt/continuum_plugins/continuum-plugin-sdk

require (
	github.com/ContinuumApp/continuum-plugin-sdk v0.3.8
	github.com/hashicorp/go-hclog v1.6.3
	github.com/jackc/pgx/v5 v5.9.2
	github.com/ledongthuc/pdf v0.0.0-20250511090121-5959a4027728
	golang.org/x/crypto v0.51.0
	golang.org/x/time v0.15.0
	google.golang.org/grpc v1.81.1
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/fatih/color v1.19.0 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/hashicorp/go-plugin v1.8.0 // indirect
	github.com/hashicorp/yamux v0.1.2 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.22 // indirect
	github.com/oklog/run v1.2.0 // indirect
	golang.org/x/net v0.54.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.44.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260511170946-3700d4141b60 // indirect
)
