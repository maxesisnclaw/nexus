"""auth service - validates Bearer tokens.

Hardcoded tokens for demo:
    'token-alice'  -> user_id 'u-alice'
    'token-bob'    -> user_id 'u-bob'
Anything else is invalid.
"""
from __future__ import annotations

import logging
import os
import signal
import sys

import msgpack

from nexus_sdk import Node, Request, Response

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s [auth] %(message)s",
    stream=sys.stdout,
)
log = logging.getLogger("auth")

TOKENS = {
    "token-alice": "u-alice",
    "token-bob": "u-bob",
}


def verify(req: Request) -> Response:
    args = msgpack.unpackb(req.payload, raw=False)
    token = args.get("token", "")
    user_id = TOKENS.get(token)
    valid = user_id is not None
    log.info("verify token=%r valid=%s user_id=%s", token, valid, user_id)
    out = {"valid": valid, "user_id": user_id or ""}
    return Response(payload=msgpack.packb(out, use_bin_type=True))


def main() -> int:
    registry_addr = os.environ.get("NEXUS_REGISTRY", "/run/nexus/registry.sock")
    uds = os.environ.get("NEXUS_UDS", "/run/nexus/svc/auth.sock")
    os.makedirs(os.path.dirname(uds), exist_ok=True)
    if os.path.exists(uds):
        os.unlink(uds)

    node = Node(name="auth", id="auth-1", uds_addr=uds, registry_addr=registry_addr)
    node.handle("verify", verify)

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
