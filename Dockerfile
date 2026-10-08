FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder
LABEL maintainer="nekohasekai <contact-git@sekai.icu>"
COPY . /go/src/github.com/swysgh/mill-box
WORKDIR /go/src/github.com/swysgh/mill-box
ARG TARGETOS TARGETARCH
ARG GOPROXY=""
ENV GOPROXY ${GOPROXY}
ENV CGO_ENABLED=0
ENV GOOS=$TARGETOS
ENV GOARCH=$TARGETARCH
RUN set -ex \
    && apk add git build-base \
    && export COMMIT=$(git rev-parse --short HEAD) \
    && export VERSION=$(go run ./cmd/internal/read_tag) \
    && export TAGS=$(cat release/DEFAULT_BUILD_TAGS_OTHERS) \
    && export LDFLAGS_SHARED=$(cat release/LDFLAGS) \
    && go build -v -trimpath -tags "$TAGS" \
        -o /go/bin/mill-box \
        -ldflags "-X \"github.com/swysgh/mill-box/constant.Version=$VERSION\" $LDFLAGS_SHARED -s -w -buildid=" \
        ./cmd/mill-box
FROM --platform=$TARGETPLATFORM alpine AS dist
LABEL maintainer="nekohasekai <contact-git@sekai.icu>"
RUN set -ex \
    && apk add --no-cache --upgrade bash tzdata ca-certificates nftables
COPY --from=builder /go/bin/mill-box /usr/local/bin/mill-box
ENTRYPOINT ["mill-box"]
