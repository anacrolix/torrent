module github.com/james-lawrence/torrent

go 1.23.0

toolchain go1.24.0

require (
	bazil.org/fuse v0.0.0-20230120002735-62a210ff1fd5
	filippo.io/edwards25519 v1.1.0
	github.com/RoaringBitmap/roaring v1.9.4
	github.com/alexflint/go-arg v1.5.1
	github.com/anacrolix/args v0.5.1-0.20220509024600-c3b77d0b61ac
	github.com/anacrolix/bargle/v2 v2.0.0-20240909020204-5265698a6040
	github.com/anacrolix/chansync v0.6.0
	github.com/anacrolix/envpprof v1.3.0
	github.com/anacrolix/go-libutp v1.3.2
	github.com/anacrolix/gostdapp v0.1.0
	github.com/anacrolix/log v0.15.3-0.20240627045001-cd912c641d83
	github.com/anacrolix/missinggo v1.3.0
	github.com/anacrolix/missinggo/v2 v2.8.0
	github.com/anacrolix/multiless v0.4.0
	github.com/anacrolix/publicip v0.3.1
	github.com/anacrolix/stm v0.5.0
	github.com/anacrolix/tagflag v1.4.0
	github.com/anacrolix/torrent v1.58.1
	github.com/anacrolix/upnp v0.1.4
	github.com/anacrolix/utp v0.2.0
	github.com/benbjohnson/immutable v0.4.3
	github.com/bits-and-blooms/bloom/v3 v3.7.0
	github.com/bradfitz/iter v0.0.0-20191230175014-e8f45d346db8
	github.com/davecgh/go-spew v1.1.1
	github.com/docopt/docopt-go v0.0.0-20180111231733-ee0de3bc6815
	github.com/dustin/go-humanize v1.0.1
	github.com/edsrzf/mmap-go v1.2.0
	github.com/frankban/quicktest v1.14.6
	github.com/fsnotify/fsnotify v1.8.0
	github.com/google/btree v1.1.3
	github.com/google/go-cmp v0.6.0
	github.com/gosuri/uiprogress v0.0.1
	github.com/multiformats/go-base36 v0.2.0
	github.com/pkg/errors v0.9.1
	github.com/stretchr/testify v1.10.0
	go.etcd.io/bbolt v1.4.0
	golang.org/x/net v0.35.0
	golang.org/x/time v0.10.0
	golang.org/x/xerrors v0.0.0-20240903120638-7835f813f4da
)

require (
	github.com/anacrolix/backtrace v0.0.0-20221205112523-22a61db8f82e // indirect
	github.com/cenkalti/backoff/v4 v4.1.3 // indirect
	github.com/go-logr/logr v1.2.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-ole/go-ole v1.2.6 // indirect
	github.com/golang/protobuf v1.5.3 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.12.0 // indirect
	github.com/honeycombio/honeycomb-opentelemetry-go v0.3.0 // indirect
	github.com/honeycombio/opentelemetry-go-contrib/launcher v0.0.0-20221031150637-a3c60ed98d54 // indirect
	github.com/klauspost/cpuid/v2 v2.2.3 // indirect
	github.com/lufia/plan9stats v0.0.0-20220913051719-115f729f3c8c // indirect
	github.com/minio/sha256-simd v1.0.0 // indirect
	github.com/mr-tron/base58 v1.2.0 // indirect
	github.com/multiformats/go-multihash v0.2.3 // indirect
	github.com/multiformats/go-varint v0.0.6 // indirect
	github.com/power-devops/perfstat v0.0.0-20220216144756-c35f1ee13d7c // indirect
	github.com/sethvargo/go-envconfig v0.8.2 // indirect
	github.com/shirou/gopsutil/v3 v3.22.9 // indirect
	github.com/spaolacci/murmur3 v1.1.0 // indirect
	github.com/tklauser/go-sysconf v0.3.10 // indirect
	github.com/tklauser/numcpus v0.5.0 // indirect
	github.com/yusufpapurcu/wmi v1.2.2 // indirect
	go.opentelemetry.io/contrib/instrumentation/host v0.36.4 // indirect
	go.opentelemetry.io/contrib/instrumentation/runtime v0.36.4 // indirect
	go.opentelemetry.io/contrib/propagators/b3 v1.11.1 // indirect
	go.opentelemetry.io/contrib/propagators/ot v1.11.1 // indirect
	go.opentelemetry.io/otel v1.11.1 // indirect
	go.opentelemetry.io/otel/exporters/otlp/internal/retry v1.11.1 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric v0.33.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc v0.33.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp v0.33.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.11.1 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.11.1 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.11.1 // indirect
	go.opentelemetry.io/otel/exporters/stdout/stdouttrace v1.11.1 // indirect
	go.opentelemetry.io/otel/metric v0.33.0 // indirect
	go.opentelemetry.io/otel/sdk v1.11.1 // indirect
	go.opentelemetry.io/otel/sdk/metric v0.33.0 // indirect
	go.opentelemetry.io/otel/trace v1.11.1 // indirect
	go.opentelemetry.io/proto/otlp v0.19.0 // indirect
	go.uber.org/atomic v1.10.0 // indirect
	go.uber.org/multierr v1.8.0 // indirect
	golang.org/x/crypto v0.33.0 // indirect
	golang.org/x/sync v0.11.0 // indirect
	golang.org/x/text v0.22.0 // indirect
	google.golang.org/genproto v0.0.0-20230410155749-daa745c078e1 // indirect
	google.golang.org/grpc v1.56.3 // indirect
	google.golang.org/protobuf v1.33.0 // indirect
	lukechampine.com/blake3 v1.1.6 // indirect
)

require (
	github.com/alecthomas/atomic v0.1.0-alpha2 // indirect
	github.com/alexflint/go-scalar v1.2.0 // indirect
	github.com/anacrolix/generics v0.0.3-0.20240902042256-7fb2702ef0ca
	github.com/anacrolix/missinggo/perf v1.0.0 // indirect
	github.com/anacrolix/mmsg v1.0.1 // indirect
	github.com/anacrolix/sync v0.5.1
	github.com/bits-and-blooms/bitset v1.12.0 // indirect
	github.com/gosuri/uilive v0.0.4 // indirect
	github.com/huandu/xstrings v1.5.0 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mschoch/smat v0.2.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/rogpeppe/go-internal v1.11.0 // indirect
	github.com/rs/dnscache v0.0.0-20230804202142-fc85eb664529
	github.com/ryszard/goskiplist v0.0.0-20150312221310-2dfbae5fcf46 // indirect
	golang.org/x/exp v0.0.0-20250218142911-aa4b98e5adaa
	golang.org/x/sys v0.30.0
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
