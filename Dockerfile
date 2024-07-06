FROM sigp/lcli:v4.6.0 as lcli
FROM sigp/lighthouse:v5.2.1 as lighthouse
FROM ghcr.io/paradigmxyz/reth:v0.2.0-beta.9 as reth

# Build mev-boost-relay
FROM golang:1.21 as mev-boost-relay
WORKDIR /mev-boost-relay
COPY ./mev-boost-relay/ .
RUN go build -o /bin/mev-boost-relay main.go

FROM ubuntu:22.04
COPY --from=lcli /usr/local/bin/lcli /usr/local/bin/lcli
COPY --from=lighthouse /usr/local/bin/lighthouse /usr/local/bin/lighthouse
COPY --from=mev-boost-relay /bin/mev-boost-relay /usr/local/bin/mev-boost-relay
COPY --from=reth /usr/local/bin/reth /usr/local/bin/reth

COPY ./ ./
RUN chmod +x /entrypoint.sh
RUN apt update && apt install -y curl jq

ENTRYPOINT ["/entrypoint.sh"]
