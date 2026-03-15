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
    go test -run @ -benchtime 2x -bench . ./... > /dev/null

build-possum:
    cd storage/possum/lib && cargo build
