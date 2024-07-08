# Suave playground

Make sure you have both `reth` and `lighthouse` installed:

```
$ which reth
$ which lighthouse
```

Run the playground:

```
$ go run main.go
```

The playground runs four services:

- A `lighthouse` beacon node service.
- A `lighthouse` validator service.
- A `reth` execution client service.
- An in-memory `mev-boost-relay` service.
