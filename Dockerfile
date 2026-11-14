FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS builder

ARG TARGETARCH
ARG TARGETOS

WORKDIR /app

# Compile natively on the build host, including the C ABI used by plugins.
RUN apt-get update && apt-get install -y --no-install-recommends build-essential git \
    && case "$(go env GOARCH):${TARGETARCH}" in \
         amd64:arm64) apt-get install -y --no-install-recommends gcc-aarch64-linux-gnu ;; \
         arm64:amd64) apt-get install -y --no-install-recommends gcc-x86-64-linux-gnu ;; \
         amd64:amd64|arm64:arm64) ;; \
         *) echo "Unsupported build/target architecture" >&2; exit 1 ;; \
       esac \
    && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./

RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

RUN case "$(go env GOARCH):${TARGETARCH}" in \
      amd64:arm64) export CC=aarch64-linux-gnu-gcc ;; \
      arm64:amd64) export CC=x86_64-linux-gnu-gcc ;; \
    esac \
    && CGO_ENABLED=1 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -buildvcs=false -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" -o ./CLIProxyAPI ./cmd/server/

FROM debian:bookworm

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

LABEL org.opencontainers.image.title="CLIProxyAPI Codex credential identity fork" \
      org.opencontainers.image.source="https://github.com/jinshenganyuci/CLIProxyAPI" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}"

RUN apt-get update && apt-get install -y --no-install-recommends tzdata ca-certificates && rm -rf /var/lib/apt/lists/*

RUN mkdir /CLIProxyAPI

COPY --from=builder ./app/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI

COPY config.example.yaml /CLIProxyAPI/config.example.yaml

# Ship the matching management UI with the backend so /management.html never
# drifts to an upstream panel that does not understand this fork's API.
COPY static/management.html /CLIProxyAPI/static/management.html

WORKDIR /CLIProxyAPI

EXPOSE 8317

ENV TZ=Asia/Shanghai
ENV MANAGEMENT_STATIC_PATH=/CLIProxyAPI/static/management.html
ENV MANAGEMENT_STATIC_IMMUTABLE=true

RUN cp /usr/share/zoneinfo/${TZ} /etc/localtime && echo "${TZ}" > /etc/timezone

CMD ["./CLIProxyAPI"]
