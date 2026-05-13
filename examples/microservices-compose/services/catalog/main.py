"""catalog service - product lookup with mock inventory."""
from __future__ import annotations

import logging
import os
import signal
import sys

import msgpack

from nexus_sdk import Node, Request, Response

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s [catalog] %(message)s",
    stream=sys.stdout,
)
log = logging.getLogger("catalog")

# In-memory catalog
CATALOG = {
    "sku-001": {"name": "Mechanical Keyboard", "unit_price": 89.0, "stock": 12},
    "sku-002": {"name": "USB-C Cable",         "unit_price": 9.5,  "stock": 100},
    "sku-003": {"name": "Coffee Beans 1kg",    "unit_price": 25.0, "stock": 3},
    "sku-empty": {"name": "Out Of Stock Item", "unit_price": 1.0,  "stock": 0},
}


def get(req: Request) -> Response:
    args = msgpack.unpackb(req.payload, raw=False)
    sku = args.get("sku", "")
    qty = int(args.get("qty", 1))
    item = CATALOG.get(sku)
    if item is None:
        out = {"sku": sku, "name": "", "unit_price": 0.0, "stock": 0, "available": False}
        log.info("get sku=%s qty=%d -> not found", sku, qty)
    else:
        available = item["stock"] >= qty and qty > 0
        out = {
            "sku": sku,
            "name": item["name"],
            "unit_price": float(item["unit_price"]),
            "stock": int(item["stock"]),
            "available": available,
        }
        log.info("get sku=%s qty=%d -> available=%s", sku, qty, available)
    return Response(payload=msgpack.packb(out, use_bin_type=True))


def main() -> int:
    registry_addr = os.environ.get("NEXUS_REGISTRY", "/run/nexus/registry.sock")
    uds = os.environ.get("NEXUS_UDS", "/run/nexus/svc/catalog.sock")
    os.makedirs(os.path.dirname(uds), exist_ok=True)
    if os.path.exists(uds):
        os.unlink(uds)

    node = Node(name="catalog", id="catalog-1", uds_addr=uds, registry_addr=registry_addr)
    node.handle("get", get)

    def _shutdown(signum, frame):
        log.info("signal %d received, shutting down", signum)
        node.close()

    signal.signal(signal.SIGTERM, _shutdown)
    signal.signal(signal.SIGINT, _shutdown)

    log.info("starting on %s registry=%s", uds, registry_addr)
    node.serve()
    log.info("exited")
    return 0


if __name__ == "__main__":
    sys.exit(main())
