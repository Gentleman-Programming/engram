FROM --platform=$BUILDPLATFORM golang:1.25.10-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# VERSION is injected via ldflags so `engram version` reports the release tag.
# Defaults to "dev" for local builds; the release workflow passes the git tag.
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/engram ./cmd/engram

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
RUN adduser -D -u 10001 engram
USER engram
WORKDIR /home/engram
COPY --from=builder /out/engram /usr/local/bin/engram

EXPOSE 18080
ENTRYPOINT ["engram"]
CMD ["cloud", "serve"]
