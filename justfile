check:
    go test -run @ -failfast ./... > /dev/null

act:
    act -j test --matrix go-version:'1.24' --env-file .empty.env

test *args: build-possum
    CGO_LDFLAGS="-L$(realpath storage/possum/lib/target/debug)" go test -race -failfast {{ args }} ./...

build-possum:
    cd storage/possum/lib && cargo build
