"""notifier service - records 'sent' notifications in memory."""
from __future__ import annotations

import logging
import os
import signal
import sys
import threading

import msgpack

from nexus_sdk import Node, Request, Response

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s [notifier] %(message)s",
    stream=sys.stdout,
)
log = logging.getLogger("notifier")

_sent_lock = threading.Lock()
_sent: list[dict] = []


def send(req: Request) -> Response:
    args = msgpack.unpackb(req.payload, raw=False)
    rec = {
        "user_id": args.get("user_id", ""),
        "channel": args.get("channel", "email"),
        "message": args.get("message", ""),
    }
    with _sent_lock:
        _sent.append(rec)
        idx = len(_sent)
    log.info("send #%d user=%s channel=%s msg=%r",
             idx, rec["user_id"], rec["channel"], rec["message"])
    return Response(payload=msgpack.packb({"ok": True, "seq": idx}, use_bin_type=True))


def stats(req: Request) -> Response:
    with _sent_lock:
        out = {"count": len(_sent), "last": _sent[-1] if _sent else None}
    return Response(payload=msgpack.packb(out, use_bin_type=True))


def main() -> int:
    registry_addr = os.environ.get("NEXUS_REGISTRY", "/run/nexus/registry.sock")
    uds = os.environ.get("NEXUS_UDS", "/run/nexus/svc/notifier.sock")
    os.makedirs(os.path.dirname(uds), exist_ok=True)
    if os.path.exists(uds):
        os.unlink(uds)

    node = Node(name="notifier", id="notifier-1", uds_addr=uds, registry_addr=registry_addr)
    node.handle("send", send)
    node.handle("stats", stats)

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
