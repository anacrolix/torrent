check:
    go test -run @ -failfast ./... > /dev/null

act:
    act -j test --matrix go-version:'1.24' --env-file .empty.env

GOPPROF := env("GOPPROF", "http")

test-short: build-possum
    GOPPROF='{{GOPPROF}}' go test -race -failfast -short ./...

test *args: build-possum
    GOPPROF='{{GOPPROF}}' go test -race -failfast {{ args }} -benchtime 2x -bench . ./...

build-possum:
    cd storage/possum/lib && cargo build
