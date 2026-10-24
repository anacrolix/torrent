export CGO_LDFLAGS := env_var_or_default('CGO_LDFLAGS', '') + ' -Lstorage/possum/lib/target/debug'

check:
    go test -run @ -failfast ./... > /dev/null

act:
    act -j test --env-file .empty.env

export GOPPROF := env("GOPPROF", "http")

test-short *args: build-possum
    GOPPROF='{{GOPPROF}}' go test -race -failfast -short {{ args }} ./...

test *args: build-possum
    go test -race {{ args }} ./...
    benchmark_log="$(mktemp)"; \
        trap 'rm -f "$benchmark_log"' 0; \
        if ! go test -run @ -benchtime 2x -bench . ./... > "$benchmark_log" 2>&1; then \
            cat "$benchmark_log"; \
            exit 1; \
        fi

build-possum:
    cd storage/possum/lib && cargo build
