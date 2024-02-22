# On macOS, docker does not support IPv6.

FROM alpine

RUN apk add go fuse bash

RUN go install github.com/anacrolix/godo@v1
RUN echo "$HOME"
ENV PATH="/root/go/bin:$PATH"

WORKDIR /src

COPY . .

ARG GOCACHE=/root/.cache/go-build
ARG GOMODCACHE=/root/go/pkg/mod

RUN --mount=type=cache,target=$GOCACHE \
	--mount=type=cache,target=$GOMODCACHE \
	go test -failfast ./...

# Can't use fuse inside Docker? Asks for modprobe fuse.

# RUN --mount=type=cache,target=$GOCACHE \
# 	--mount=type=cache,target=$GOMODCACHE \
# 	./fs/test.sh
