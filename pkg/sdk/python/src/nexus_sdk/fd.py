"""File descriptor passing helpers for Unix domain sockets."""

from __future__ import annotations

import socket
import struct
from typing import Tuple


def send_fd(sock: socket.socket, fd: int, metadata: bytes = b"fd") -> None:
    """Send ``fd`` over ``sock`` using SCM_RIGHTS.

    Args:
        sock: Connected Unix-domain socket.
        fd: File descriptor to send.
        metadata: Small payload sent with ancillary data.

    Raises:
        ValueError: If ``fd`` is invalid.
        RuntimeError: If sending ancillary data fails.
    """
    if fd < 0:
        raise ValueError(f"invalid file descriptor: {fd}")
    payload = metadata or b"fd"
    anc = [(socket.SOL_SOCKET, socket.SCM_RIGHTS, struct.pack("i", fd))]
    try:
        sent = sock.sendmsg([payload], anc)
    except OSError as exc:
        raise RuntimeError(f"sendmsg failed: {exc}") from exc
    if sent <= 0:
        raise RuntimeError("sendmsg sent no data")


def recv_fd(sock: socket.socket) -> Tuple[int, bytes]:
    """Receive one file descriptor from ``sock``.

    Returns:
        A tuple of ``(fd, metadata)``.

    Raises:
        RuntimeError: If no descriptor is found or recvmsg fails.
    """
    cmsg_space = socket.CMSG_SPACE(struct.calcsize("i"))
    try:
        msg, ancdata, _flags, _addr = sock.recvmsg(65536, cmsg_space)
    except OSError as exc:
        raise RuntimeError(f"recvmsg failed: {exc}") from exc

    for level, ctype, data in ancdata:
        if level == socket.SOL_SOCKET and ctype == socket.SCM_RIGHTS:
            if len(data) < struct.calcsize("i"):
                raise RuntimeError("invalid SCM_RIGHTS payload")
            fd = struct.unpack("i", data[: struct.calcsize("i")])[0]
            return fd, msg

    raise RuntimeError("no file descriptor in ancillary data")
