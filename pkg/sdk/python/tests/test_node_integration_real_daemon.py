from __future__ import annotations

import shutil
import subprocess
import sys
import tempfile
import textwrap
import time
from pathlib import Path
from typing import Any

import pytest

import nexus_sdk.node as node_module
from nexus_sdk.node import Node, Response
from nexus_sdk.registry import RegistryClient


def _repo_root() -> Path:
    return Path(__file__).resolve().parents[4]


def _wait_for_socket(path: Path, timeout: float = 5.0) -> None:
    deadline = time.time() + timeout
    while time.time() < deadline:
        if path.exists():
            return
        time.sleep(0.02)
    raise TimeoutError(f"socket not ready: {path}")


def _wait_for_lookup(registry_addr: str, service: str, timeout: float = 5.0) -> list[dict[str, Any]]:
    client = RegistryClient(registry_addr)
    deadline = time.time() + timeout
    try:
        while time.time() < deadline:
            instances = client.lookup(service)
            if instances:
                return instances
            time.sleep(0.05)
    finally:
        client.close()
    raise TimeoutError(f"service not found in registry: {service}")


def _write_config(path: Path, socket_path: Path) -> None:
    path.write_text(
        textwrap.dedent(
            f"""
            [daemon]
            socket = "{socket_path}"
            health_interval = "100ms"
            shutdown_grace = "500ms"
            """
        ).strip()
        + "\n",
        encoding="utf-8",
    )


def _start_daemon(config_path: Path) -> subprocess.Popen[str]:
    proc = subprocess.Popen(
        ["go", "run", "./cmd/nexusd", "-config", str(config_path)],
        cwd=_repo_root(),
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    return proc


def _stop_daemon(proc: subprocess.Popen[str], timeout: float = 5.0) -> None:
    if proc.poll() is not None:
        return
    proc.terminate()
    try:
        proc.wait(timeout=timeout)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait(timeout=timeout)


def _go_call_helper_path() -> Path:
    repo = _repo_root()
    helper_dir = Path(tempfile.mkdtemp(prefix=".nexus-go-call-", dir=repo))
    helper_path = helper_dir / "main.go"
    helper_path.write_text(
        textwrap.dedent(
            """
            package main

            import (
                "fmt"
                "os"
                "time"

                "github.com/maxesisn/nexus/pkg/sdk"
            )

            func main() {
                if len(os.Args) != 5 {
                    fmt.Fprintf(os.Stderr, "usage: <socket> <service> <method> <payload>\\n")
                    os.Exit(2)
                }
                node, err := sdk.New(sdk.Config{
                    Name:         "go-caller",
                    ID:           "go-caller-1",
                    RegistryAddr: os.Args[1],
                    RequestTimeout: 2 * time.Second,
                    CallRetries:  4,
                    RetryBackoff: 50 * time.Millisecond,
                })
                if err != nil {
                    fmt.Fprintf(os.Stderr, "new sdk node: %v\\n", err)
                    os.Exit(1)
                }
                defer node.Close()

                resp, err := node.Call(os.Args[2], os.Args[3], []byte(os.Args[4]))
                if err != nil {
                    fmt.Fprintf(os.Stderr, "call failed: %v\\n", err)
                    os.Exit(1)
                }
                _, _ = os.Stdout.Write(resp.Payload)
            }
            """
        ).strip()
        + "\n",
        encoding="utf-8",
    )
    return helper_path


def _run_go_call(helper_path: Path, socket_path: Path, payload: str) -> str:
    proc = subprocess.run(
        ["go", "run", str(helper_path), str(socket_path), "py-echo", "echo", payload],
        cwd=_repo_root(),
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        check=False,
        timeout=20,
    )
    if proc.returncode != 0:
        raise RuntimeError(f"go caller failed: {proc.stderr.strip()}")
    return proc.stdout


def test_python_node_real_daemon_lifecycle_and_go_interop(
    monkeypatch: Any,
    tmp_path: Path,
) -> None:
    if sys.platform != "linux":
        pytest.skip("real daemon integration test is Linux-only")
    if shutil.which("go") is None:
        pytest.skip("go toolchain not available")

    monkeypatch.setattr(node_module, "_HEARTBEAT_INTERVAL_SECONDS", 0.1)
    monkeypatch.setattr(node_module, "_HEARTBEAT_FAILURES_BEFORE_RECOVERY", 1)

    socket_path = tmp_path / "registry.sock"
    config_path = tmp_path / "nexus.toml"
    _write_config(config_path, socket_path)

    helper_path = _go_call_helper_path()
    daemon = _start_daemon(config_path)
    try:
        _wait_for_socket(socket_path)

        node = Node(
            name="py-echo",
            id="py-echo-1",
            uds_addr=str(tmp_path / "py-echo.sock"),
            registry_addr=str(socket_path),
            capabilities=["python"],
        )
        node.handle("echo", lambda req: Response(payload=req.payload + b"!"))
        node.serve_async()
        try:
            _wait_for_lookup(str(socket_path), "py-echo")

            first = _run_go_call(helper_path, socket_path, "hello")
            assert first == "hello!"

            _stop_daemon(daemon)
            daemon = _start_daemon(config_path)
            _wait_for_socket(socket_path)

            _wait_for_lookup(str(socket_path), "py-echo", timeout=8.0)
            second = _run_go_call(helper_path, socket_path, "again")
            assert second == "again!"
        finally:
            node.close()
    finally:
        _stop_daemon(daemon)
        shutil.rmtree(helper_path.parent, ignore_errors=True)
