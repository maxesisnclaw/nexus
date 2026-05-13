# microservices-compose — cross-language nexus E2E

A six-service e-commerce checkout pipeline, mixing Go and Python, all
communicating exclusively through nexus over a shared UDS volume. The
intent is to prove that a real microservice topology works end-to-end on
nexus without TCP and without daemon-managed processes — every service is
just a regular container that registers itself with the nexus daemon
over the control socket.

## Topology

```
HTTP POST /checkout
        │
        ▼
   gateway   (Go)     HTTP entrypoint, msgpack-encoded RPC fan-out
        │
        ├──► auth.verify       (Python) Bearer token validation
        ├──► catalog.get       (Python) product & inventory lookup
        ├──► orders.create     (Go)     order id + total calculation
        ├──► payment.charge    (Go)     mock charge → txn id
        └──► notifier.send     (Python) outbound notification
        │
        ▼
   HTTP 200 { order_id, total, txn_id, ... }
```

All seven services share the `nexus-run` named volume mounted at
`/run/nexus`. The nexus daemon listens on `/run/nexus/registry.sock`;
each service exposes a UDS at `/run/nexus/svc/<name>.sock`. Discovery
and routing is handled by nexus.

## Run

```bash
# from the repo root
cd examples/microservices-compose
docker compose build
docker compose up -d nexusd auth catalog notifier orders payment gateway

# run the end-to-end verifier (exit 0 = all checks passed)
docker compose run --rm client

# inspect logs
docker compose logs --tail=20 gateway auth catalog orders payment notifier
```

The `client` container hits `gateway:8080` over HTTP and additionally
makes a direct nexus RPC call to `notifier.stats` to confirm every leg of
the pipeline actually ran.

## RPC schema (msgpack maps)

| Service / Method   | Input                                                    | Output                                                              |
|--------------------|----------------------------------------------------------|---------------------------------------------------------------------|
| `auth.verify`      | `{token: str}`                                           | `{valid: bool, user_id: str}`                                       |
| `catalog.get`      | `{sku: str, qty: int}`                                   | `{sku, name, unit_price: float, stock: int, available: bool}`       |
| `orders.create`    | `{user_id, sku, qty, unit_price}`                        | `{order_id, total: float}`                                          |
| `payment.charge`   | `{order_id, user_id, amount}`                            | `{ok: bool, txn_id: str}`                                           |
| `notifier.send`    | `{user_id, channel, message}`                            | `{ok: bool, seq: int}`                                              |
| `notifier.stats`   | `{}`                                                     | `{count: int, last: {...} | null}`                                  |

## Notes

* Go services build out of this repository directly. The build context
  is the repo root; `services/go/go.mod` uses a `replace` directive
  pointing at `../../..` so the demo always builds against the local
  nexus source.
* Python services install `nexus-rpc-sdk` from PyPI. The
  cross-namespace UDS fix landed in v0.5.2.
* The compose UDS-only deployment relies on the
  `IsUDSReachable`-based router (added in nexus v1.0.3) so that any two
  containers sharing the `nexus-run` volume can talk over UDS even
  though they have different hostnames.
