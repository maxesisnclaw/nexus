from __future__ import annotations

import time
from typing import Any

import pytest

from nexus_sdk.node import Node, Request, Response
from nexus_sdk.registry import RegistryClient


def _wait_for_service(registry_addr: str, name: str, timeout: float = 2.0) -> None:
    client = RegistryClient(registry_addr)
    deadline = time.time() + timeout
    try:
        while time.time() < deadline:
            if client.lookup(name):
                return
            time.sleep(0.02)
    finally:
        client.close()
    raise TimeoutError(f"service {name} not registered in time")


def test_node_handle_serve_call_local_uds_roundtrip(
    mock_registry: object,
    registry_socket_path: str,
    socket_path_factory: Any,
) -> None:
    server = Node(name="echo", id="echo-1", uds_addr=socket_path_factory("echo.sock"), registry_addr=registry_socket_path)
    server.handle("echo", lambda req: Response(payload=req.payload))
    server.serve_async()

    _wait_for_service(registry_socket_path, "echo")

    client = Node(name="caller", id="caller-1", registry_addr=registry_socket_path)
    try:
        resp = client.call("echo", "echo", b"hello")
        assert resp.payload == b"hello"
    finally:
        client.close()
        server.close()


def test_node_context_manager(mock_registry: object, registry_socket_path: str, socket_path_factory: Any) -> None:
    with Node(name="ctx", id="ctx-1", uds_addr=socket_path_factory("ctx.sock"), registry_addr=registry_socket_path) as server:
        server.handle("ping", lambda req: Response(payload=b"pong"))
        server.serve_async()
        _wait_for_service(registry_socket_path, "ctx")

        with Node(name="caller", id="caller-ctx", registry_addr=registry_socket_path) as client:
            resp = client.call("ctx", "ping", b"")
            assert resp.payload == b"pong"

    reg = RegistryClient(registry_socket_path)
    try:
        assert reg.lookup("ctx") == []
    finally:
        reg.close()


def test_node_multiple_handlers(
    mock_registry: object,
    registry_socket_path: str,
    socket_path_factory: Any,
) -> None:
    server = Node(name="multi", id="multi-1", uds_addr=socket_path_factory("multi.sock"), registry_addr=registry_socket_path)

    def plus_one(req: Request) -> Response:
        return Response(payload=str(int(req.payload.decode()) + 1).encode())

    def plus_two(req: Request) -> Response:
        return Response(payload=str(int(req.payload.decode()) + 2).encode())

    server.handle("one", plus_one)
    server.handle("two", plus_two)
    server.serve_async()
    _wait_for_service(registry_socket_path, "multi")

    client = Node(name="caller", id="caller-multi", registry_addr=registry_socket_path)
    try:
        assert client.call("multi", "one", b"40").payload == b"41"
        assert client.call("multi", "two", b"40").payload == b"42"
    finally:
        client.close()
        server.close()


def test_call_to_nonexistent_handler_returns_error(
    mock_registry: object,
    registry_socket_path: str,
    socket_path_factory: Any,
) -> None:
    server = Node(
        name="errors",
        id="errors-1",
        uds_addr=socket_path_factory("errors.sock"),
        registry_addr=registry_socket_path,
    )
    server.handle("ok", lambda req: Response(payload=b"ok"))
    server.serve_async()
    _wait_for_service(registry_socket_path, "errors")

    client = Node(name="caller", id="caller-errors", registry_addr=registry_socket_path)
    try:
        with pytest.raises(RuntimeError, match="handler not found"):
            client.call("errors", "missing", b"x")
    finally:
        client.close()
        server.close()
