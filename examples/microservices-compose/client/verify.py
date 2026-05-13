"""End-to-end verification for the nexus-demo microservice ecosystem.

Exits 0 on full success, non-zero on any assertion failure.
"""
from __future__ import annotations

import os
import sys
import time
from typing import Any

import msgpack
import requests
from nexus_sdk import RegistryClient

GATEWAY = os.environ.get("GATEWAY_URL", "http://gateway:8080")
REGISTRY = os.environ.get("NEXUS_REGISTRY", "/run/nexus/registry.sock")
EXPECTED_SERVICES = ["auth", "catalog", "orders", "payment", "notifier"]
# gateway intentionally not in this list — it is a pure client.


def heading(s: str) -> None:
    print()
    print("=" * 70)
    print(f"  {s}")
    print("=" * 70)


def wait_for_registry(timeout: float = 30.0) -> RegistryClient:
    deadline = time.time() + timeout
    last_err: Exception | None = None
    while time.time() < deadline:
        if os.path.exists(REGISTRY):
            try:
                cli = RegistryClient(REGISTRY)
                cli.lookup("__probe__")  # trips a round trip; empty list is fine
                return cli
            except Exception as exc:
                last_err = exc
        time.sleep(0.3)
    raise SystemExit(f"registry not ready at {REGISTRY}: {last_err}")


def wait_for_services(cli: RegistryClient, timeout: float = 30.0) -> dict[str, list[Any]]:
    deadline = time.time() + timeout
    while time.time() < deadline:
        snapshot: dict[str, list[Any]] = {}
        ok = True
        for name in EXPECTED_SERVICES:
            inst = cli.lookup(name)
            snapshot[name] = inst
            if not inst:
                ok = False
        if ok:
            return snapshot
        time.sleep(0.5)
    raise SystemExit(f"some services missing in registry after {timeout}s")


def wait_for_gateway(timeout: float = 30.0) -> None:
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            r = requests.get(f"{GATEWAY}/healthz", timeout=2)
            if r.status_code == 200:
                return
        except requests.RequestException:
            pass
        time.sleep(0.3)
    raise SystemExit(f"gateway not healthy at {GATEWAY}")


def post_checkout(body: dict) -> tuple[int, dict]:
    r = requests.post(f"{GATEWAY}/checkout", json=body, timeout=10)
    try:
        data = r.json()
    except Exception:
        data = {"raw": r.text}
    return r.status_code, data


def main() -> int:
    heading("step 1: wait for registry & services")
    cli = wait_for_registry()
    snapshot = wait_for_services(cli)
    for name, inst in snapshot.items():
        ids = [i["id"] if isinstance(i, dict) else getattr(i, "id", "?") for i in inst]
        print(f"  ✓ {name:<10} -> {ids}")

    heading("step 2: wait for gateway HTTP")
    wait_for_gateway()
    print("  ✓ gateway /healthz returned 200")

    heading("step 3: happy path — alice buys keyboard")
    status, body = post_checkout({"token": "token-alice", "sku": "sku-001", "qty": 2})
    print(f"  status={status} body={body}")
    assert status == 200, f"expected 200, got {status}"
    assert body["user_id"] == "u-alice", body
    assert body["product_name"] == "Mechanical Keyboard", body
    assert body["total"] == 178.0, body
    assert body["order_id"].startswith("ord-"), body
    assert body["txn_id"].startswith("txn-"), body
    print("  ✓ happy path passed")

    heading("step 4: happy path — bob buys cables")
    status, body = post_checkout({"token": "token-bob", "sku": "sku-002", "qty": 5})
    print(f"  status={status} body={body}")
    assert status == 200, body
    assert body["user_id"] == "u-bob", body
    assert body["total"] == 47.5, body
    print("  ✓ second happy path passed")

    heading("step 5: invalid token rejected at auth stage")
    status, body = post_checkout({"token": "token-mallory", "sku": "sku-001", "qty": 1})
    print(f"  status={status} body={body}")
    assert status == 401, f"expected 401, got {status}"
    assert body.get("stage") == "auth", body
    print("  ✓ unauthorized rejected")

    heading("step 6: out-of-stock rejected at catalog stage")
    status, body = post_checkout({"token": "token-alice", "sku": "sku-empty", "qty": 1})
    print(f"  status={status} body={body}")
    assert status == 409, body
    assert body.get("stage") == "catalog", body
    print("  ✓ out-of-stock rejected")

    heading("step 7: unknown sku rejected at catalog stage")
    status, body = post_checkout({"token": "token-alice", "sku": "sku-ghost", "qty": 1})
    print(f"  status={status} body={body}")
    assert status == 409, body
    print("  ✓ unknown sku rejected")

    heading("step 8: verify notifier received messages via RPC")
    # use the registry client to call notifier.stats directly
    from nexus_sdk import Node
    probe = Node(name="probe", id="probe-1", registry_addr=REGISTRY)
    try:
        resp = probe.call("notifier", "stats", msgpack.packb({}, use_bin_type=True))
        stats = msgpack.unpackb(resp.payload, raw=False)
        print(f"  notifier stats: {stats}")
        assert stats["count"] >= 2, f"expected >=2 notifications, got {stats}"
    finally:
        probe.close()
    print("  ✓ notifier saw the messages")

    print()
    print("=" * 70)
    print("  ALL VERIFICATIONS PASSED ✅")
    print("=" * 70)
    return 0


if __name__ == "__main__":
    sys.exit(main())
