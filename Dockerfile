# On macOS, docker does not support IPv6.

FROM alpine

RUN apk add go fuse bash rustup git gcc musl-dev g++
RUN rustup-init -y --profile minimal
ENV PATH="/root/.cargo/bin:$PATH"

WORKDIR /src

COPY . .
RUN git clone https://github.com/anacrolix/possum

WORKDIR possum

RUN git submodule update --init --recursive
RUN --mount=type=cache,target=/root/.cargo/registry \
	--mount=type=cache,target=/root/.cargo/git \
	--mount=type=cache,target=/src/possum/target \
	cargo build

WORKDIR ..

ARG GOCACHE=/root/.cache/go-build
ARG GOMODCACHE=/root/go/pkg/mod

RUN --mount=type=cache,target=$GOCACHE \
	--mount=type=cache,target=$GOMODCACHE \
	CGO_LDFLAGS=possum/target/debug/libpossum.a \
	go test -failfast ./...

# # Can't use fuse inside Docker? Asks for modprobe fuse.

# RUN go install github.com/anacrolix/godo@v1
# RUN echo "$HOME"
# ENV PATH="/root/go/bin:$PATH"
# # RUN --mount=type=cache,target=$GOCACHE \
# # 	--mount=type=cache,target=$GOMODCACHE \
# # 	./fs/test.sh
