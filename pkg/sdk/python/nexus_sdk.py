#!/usr/bin/env python3
"""Minimal Nexus Python SDK (UDS/TCP + msgpack)."""

from __future__ import annotations

import os
import socket
import struct
import threading
from dataclasses import dataclass
from typing import Callable, Dict, Optional

import msgpack


@dataclass
class Request:
    method: str
    payload: bytes
    headers: Optional[Dict[str, str]] = None


@dataclass
class Response:
    payload: bytes
    headers: Optional[Dict[str, str]] = None


class Client:
    def __init__(self, name: str, socket_path: Optional[str] = None, tcp_addr: Optional[str] = None):
        self.name = name
        self.socket_path = socket_path
        self.tcp_addr = tcp_addr
        self.handlers: Dict[str, Callable[[Request], Response]] = {}
        self._server_socket: Optional[socket.socket] = None
        self._stop = threading.Event()

    def handler(self, method: str):
        def decorator(func: Callable[[Request], Response]):
            self.handlers[method] = func
            return func

        return decorator

    def call(self, target_addr: str, method: str, payload: bytes, use_tcp: bool = False) -> Response:
        conn = self._dial(target_addr, use_tcp=use_tcp)
        try:
            self._send_message(conn, {"method": method, "payload": payload, "headers": {}})
            data = self._recv_message(conn)
            if data.get("headers", {}).get("error"):
                raise RuntimeError(data["headers"]["error"])
            return Response(payload=data.get("payload", b""), headers=data.get("headers"))
        finally:
            conn.close()

    def serve(self, addr: Optional[str] = None, use_tcp: bool = False):
        if addr is None:
            addr = self.tcp_addr if use_tcp else self.socket_path
        if not addr:
            raise ValueError("address is required")

        if use_tcp:
            host, port = addr.split(":")
            server = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            server.bind((host, int(port)))
        else:
            if os.path.exists(addr):
                os.remove(addr)
            os.makedirs(os.path.dirname(addr), exist_ok=True)
            server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            server.bind(addr)
        server.listen(64)
        self._server_socket = server

        try:
            while not self._stop.is_set():
                try:
                    conn, _ = server.accept()
                except OSError:
                    if self._stop.is_set():
                        break
                    raise
                threading.Thread(target=self._serve_conn, args=(conn,), daemon=True).start()
        finally:
            server.close()
            if not use_tcp and os.path.exists(addr):
                os.remove(addr)

    def close(self):
        self._stop.set()
        if self._server_socket is not None:
            self._server_socket.close()

    def _serve_conn(self, conn: socket.socket):
        unpacker = msgpack.Unpacker(raw=False)
        try:
            while True:
                chunk = conn.recv(65536)
                if not chunk:
                    return
                unpacker.feed(chunk)
                for obj in unpacker:
                    method = obj.get("method", "")
                    payload = obj.get("payload", b"")
                    headers = obj.get("headers", {}) or {}
                    handler = self.handlers.get(method)
                    if handler is None:
                        self._send_message(conn, {"method": method, "payload": b"", "headers": {"error": f"handler not found: {method}"}})
                        continue
                    resp = handler(Request(method=method, payload=payload, headers=headers))
                    self._send_message(conn, {"method": method, "payload": resp.payload, "headers": resp.headers or {}})
        finally:
            conn.close()

    def _dial(self, addr: str, use_tcp: bool) -> socket.socket:
        if use_tcp:
            host, port = addr.split(":")
            conn = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            conn.connect((host, int(port)))
            return conn
        conn = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        conn.connect(addr)
        return conn

    @staticmethod
    def _send_message(conn: socket.socket, obj: dict):
        payload = msgpack.packb(obj, use_bin_type=True)
        conn.sendall(payload)

    @staticmethod
    def _recv_message(conn: socket.socket) -> dict:
        unpacker = msgpack.Unpacker(raw=False)
        while True:
            chunk = conn.recv(65536)
            if not chunk:
                raise EOFError("connection closed")
            unpacker.feed(chunk)
            for obj in unpacker:
                return obj


def send_fd(sock: socket.socket, fd: int, metadata: bytes = b"fd"):
    anc = [(socket.SOL_SOCKET, socket.SCM_RIGHTS, struct.pack("i", fd))]
    sock.sendmsg([metadata if metadata else b"fd"], anc)


def recv_fd(sock: socket.socket):
    msg, ancdata, _, _ = sock.recvmsg(65536, socket.CMSG_LEN(struct.calcsize("i")))
    for level, ctype, data in ancdata:
        if level == socket.SOL_SOCKET and ctype == socket.SCM_RIGHTS:
            fd = struct.unpack("i", data[: struct.calcsize("i")])[0]
            return fd, msg
    raise RuntimeError("no fd in message")
