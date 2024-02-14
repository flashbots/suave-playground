#!/bin/bash

# Mev-boost relay housekeeper
docker run -d -e GENESIS_FORK_VERSION=0x42424242 -e BELLATRIX_FORK_VERSION=0x02000000 -e \
    CAPELLA_FORK_VERSION=0x03000000 -e DENEB_FORK_VERSION=0x04000000 \
    -e GENESIS_VALIDATORS_ROOT=0x740cb032a0da660447055fdb161b5e285f36dbc4b1cea2b49a15e3d6196aa6ed -e SEC_PER_SLOT=3 -e LOG_LEVEL=debug -e DB_TABLE_PREFIX=custom \
    flashbots/mev-boost-relay:latest \
    housekeeper --network custom --db postgres://postgres:postgres@host.docker.internal:5432/postgres?sslmode=disable --redis-uri host.docker.internal:6379 \
    --beacon-uris http://host.docker.internal:8000

# Mev-boost relay API
docker run -d -p 3000:3000 -e GENESIS_FORK_VERSION=0x42424242 -e BELLATRIX_FORK_VERSION=0x02000000 -e \
    CAPELLA_FORK_VERSION=0x03000000 -e DENEB_FORK_VERSION=0x04000000 \
    -e GENESIS_VALIDATORS_ROOT=0x740cb032a0da660447055fdb161b5e285f36dbc4b1cea2b49a15e3d6196aa6ed -e SEC_PER_SLOT=3 -e LOG_LEVEL=debug -e DB_TABLE_PREFIX=custom \
    flashbots/mev-boost-relay:latest \
    api --network custom --db postgres://postgres:postgres@host.docker.internal:5432/postgres?sslmode=disable --redis-uri host.docker.internal:6379 \
    --beacon-uris http://host.docker.internal:8000 \
    --secret-key 0x607a11b45a7219cc61a3d9c5fd08c7eebd602a6a19a977f8d3771d5711a550f2 \
    --listen-addr 0.0.0.0:3000

# Mev-boost website
docker run -d -p 3001:3001 -e GENESIS_FORK_VERSION=0x42424242 -e BELLATRIX_FORK_VERSION=0x02000000 -e \
    CAPELLA_FORK_VERSION=0x03000000 -e DENEB_FORK_VERSION=0x04000000 \
    -e GENESIS_VALIDATORS_ROOT=0x740cb032a0da660447055fdb161b5e285f36dbc4b1cea2b49a15e3d6196aa6ed -e SEC_PER_SLOT=3 -e LOG_LEVEL=debug -e DB_TABLE_PREFIX=custom \
    flashbots/mev-boost-relay:latest \
    website --network custom --db postgres://postgres:postgres@host.docker.internal:5432/postgres?sslmode=disable --redis-uri host.docker.internal:6379 \
    --listen-addr 0.0.0.0:3001
