"""Connection pool implementation for Nexus SDK."""

from __future__ import annotations

import socket
import threading
from collections import defaultdict


class ConnectionPool:
    """Thread-safe connection pool for RPC calls."""

    def __init__(self, max_idle: int = 8):
        if max_idle <= 0:
            raise ValueError("max_idle must be > 0")
        self._max_idle = max_idle
        self._lock = threading.Lock()
        self._idle: dict[tuple[bool, str], list[socket.socket]] = defaultdict(list)

    def get(self, addr: str, use_tcp: bool = False) -> socket.socket:
        """Get an active connection for ``addr``."""
        key = (use_tcp, addr)
        with self._lock:
            bucket = self._idle.get(key)
            while bucket:
                conn = bucket.pop()
                if conn.fileno() != -1:
                    return conn
        return self._dial(addr, use_tcp=use_tcp)

    def put(self, addr: str, sock: socket.socket, use_tcp: bool = False) -> None:
        """Return a connection to the idle pool."""
        if sock.fileno() == -1:
            return
        key = (use_tcp, addr)
        with self._lock:
            bucket = self._idle[key]
            if len(bucket) >= self._max_idle:
                try:
                    sock.close()
                except OSError:
                    pass
                return
            bucket.append(sock)

    def close_all(self) -> None:
        """Close all pooled idle connections."""
        with self._lock:
            all_conns = [conn for bucket in self._idle.values() for conn in bucket]
            self._idle.clear()
        for conn in all_conns:
            try:
                conn.close()
            except OSError:
                pass

    @staticmethod
    def _dial(addr: str, *, use_tcp: bool) -> socket.socket:
        if use_tcp:
            host, port_text = addr.rsplit(":", 1)
            return socket.create_connection((host, int(port_text)), timeout=2.0)
        conn = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        conn.connect(addr)
        return conn
