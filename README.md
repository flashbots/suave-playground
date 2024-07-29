# Builder Playground

The builder playground is a tool to quickly deploy a local end-to-end environment to test an Ethereum L1 builder.

Run the playground:

```
$ go run main.go
```

The command:

- Installs `reth` and `lighthouse` in `$HOME/.playground`.
- Creates a beacon chain genesis.
- Deploys a beacon chain with 1 proposer and 80 validators.
- Starts the beacon chain and validator.
- Starts the reth client.
- Start an in-memory mev-boost-relay.
