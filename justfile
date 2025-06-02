check:
    go test -run @ -failfast ./... > /dev/null

act:
    act -j test --matrix go-version:'1.24' --env-file .empty.env

test: build-possum
    go test -race ./...

build-possum:
    cd storage/possum/lib && cargo build
