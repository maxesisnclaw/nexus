"""Transport helpers for Nexus RPC/control messages."""

from __future__ import annotations

import socket
import struct
from typing import Any

import msgpack

MAX_MESSAGE_SIZE = 64 * 1024 * 1024


def _recv_exact(sock: socket.socket, size: int) -> bytes:
    """Receive exactly ``size`` bytes from ``sock`` or raise EOFError."""
    data = bytearray(size)
    view = memoryview(data)
    read = 0
    while read < size:
        n = sock.recv_into(view[read:])
        if n == 0:
            raise EOFError("connection closed")
        read += n
    return bytes(data)


def send_message(sock: socket.socket, msg: dict[str, Any]) -> None:
    """Encode and send a length-prefixed msgpack message."""
    data = msgpack.packb(msg, use_bin_type=True)
    if len(data) > MAX_MESSAGE_SIZE:
        raise ValueError(f"message too large: {len(data)}")
    sock.sendall(struct.pack(">I", len(data)))
    sock.sendall(data)


def recv_message(sock: socket.socket) -> dict[str, Any]:
    """Read a length-prefixed msgpack message."""
    header = _recv_exact(sock, 4)
    length = struct.unpack(">I", header)[0]
    if length > MAX_MESSAGE_SIZE:
        raise ValueError(f"message too large: {length}")
    data = _recv_exact(sock, length)
    obj = msgpack.unpackb(data, raw=False)
    if not isinstance(obj, dict):
        raise ValueError("decoded message is not a map")
    return obj
