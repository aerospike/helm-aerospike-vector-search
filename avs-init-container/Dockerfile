# syntax=docker/dockerfile:1.12.0
ARG GO_VERSION=1.23.4

FROM --platform=$BUILDPLATFORM tonistiigi/xx AS xx
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS builder

ARG TARGETOS
ARG TARGETARCH

COPY --from=xx / /

WORKDIR /app/avs-init-container
COPY . .

RUN <<-EOF
    xx-go --wrap
    cd /app/avs-init-container/cmd && OS=${TARGETOS} ARCH=${TARGETARCH} go build -o /app/avs-init-container/target/avs-init-container_${TARGETOS}_${TARGETARCH}
    xx-verify /app/avs-init-container/target/avs-init-container_${TARGETOS}_${TARGETARCH}
EOF

FROM scratch
ARG TARGETOS
ARG TARGETARCH
COPY --from=builder /app/avs-init-container/target/avs-init-container_${TARGETOS}_${TARGETARCH} /usr/bin/avs-init-container

ENTRYPOINT ["avs-init-container"]
