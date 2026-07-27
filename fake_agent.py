#!/usr/bin/env python3
"""A small fake Komari v2 agent for testing a server's agent ingestion path."""

import argparse
import json
import logging
import random
import socket
import sys
import threading
import time
from pathlib import Path
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.parse import quote
from urllib.request import Request, urlopen


DEFAULT_STATE_FILE = Path(__file__).with_name("fake-agent-client.json")
JSONRPC_VERSION = "2.0"


class FakeAgent:
    def __init__(self, server: str, client: dict[str, str], interval: float, timeout: float):
        self.server = server.rstrip("/")
        self.token = client["token"]
        self.uuid = client["uuid"]
        self.interval = interval
        self.timeout = timeout
        self.stop_event = threading.Event()
        self.ack_lock = threading.Lock()
        self.pending_ack_ids: list[str] = []

    def endpoint(self, path: str) -> str:
        return f"{self.server}{path}?token={quote(self.token, safe='')}"

    def rpc(self, method: str, params: dict[str, Any], request_id: str | None = None) -> dict[str, Any]:
        payload: dict[str, Any] = {"jsonrpc": JSONRPC_VERSION, "method": method, "params": params}
        if request_id is not None:
            payload["id"] = request_id
        data = json.dumps(payload, separators=(",", ":")).encode()
        request = Request(
            self.endpoint("/api/clients/v2/rpc"),
            data=data,
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        try:
            with urlopen(request, timeout=self.timeout) as response:
                body = response.read()
        except HTTPError as exc:
            raise RuntimeError(f"HTTP {exc.code}: {exc.read().decode(errors='replace')}") from exc
        except URLError as exc:
            raise RuntimeError(f"network error: {exc.reason}") from exc
        try:
            decoded = json.loads(body)
        except json.JSONDecodeError as exc:
            raise RuntimeError(f"invalid JSON response: {body[:200]!r}") from exc
        if decoded.get("error"):
            raise RuntimeError(f"RPC error: {decoded['error']}")
        self.handle_events(decoded.get("result", {}).get("events", []))
        return decoded

    def upload_basic_info(self) -> None:
        info = {
            "cpu_name": "Fake CPU",
            "cpu_cores": 4,
            "cpu_physical_cores": 2,
            "arch": "amd64",
            "os": "FakeAgent",
            "kernel_version": "test",
            "ipv4": "127.0.0.1",
            "ipv6": "",
            "mem_total": 8 * 1024**3,
            "swap_total": 0,
            "disk_total": 100 * 1024**3,
            "gpu_name": "",
            "virtualization": "test",
            "version": "fake-agent/1.0",
        }
        self.rpc("agent.basicInfo", {"info": info}, "basic-info")

    @staticmethod
    def report() -> dict[str, Any]:
        memory_total = 8 * 1024**3
        disk_total = 100 * 1024**3
        return {
            "cpu": {"name": "Fake CPU", "cores": 4, "arch": "amd64", "usage": round(random.uniform(8, 75), 2)},
            "ram": {"total": memory_total, "used": random.randint(memory_total // 4, memory_total * 3 // 4)},
            "swap": {"total": 0, "used": 0},
            "load": {"load1": round(random.uniform(0.05, 2.0), 2), "load5": round(random.uniform(0.05, 1.5), 2), "load15": round(random.uniform(0.05, 1.0), 2)},
            "disk": {"total": disk_total, "used": random.randint(disk_total // 5, disk_total * 4 // 5)},
            "network": {"up": random.randint(1_000, 2_000_000), "down": random.randint(1_000, 5_000_000), "totalUp": random.randint(1_000_000, 10_000_000_000), "totalDown": random.randint(1_000_000, 10_000_000_000)},
            "connections": {"tcp": random.randint(1, 100), "udp": random.randint(0, 20)},
            "uptime": int(time.monotonic()),
            "process": random.randint(30, 150),
            "message": "fake-agent",
        }

    def take_ack_ids(self) -> list[str]:
        with self.ack_lock:
            ids = self.pending_ack_ids
            self.pending_ack_ids = []
        return ids

    def handle_events(self, events: list[dict[str, Any]]) -> None:
        for event in events:
            event_id = event.get("id", "")
            method = event.get("method")
            if method != "agent.ping":
                logging.info("ignoring unsupported server event: %s", method)
                continue
            params = event.get("params") or {}
            task_id = params.get("ping_task_id")
            if not isinstance(task_id, int) or task_id <= 0:
                logging.warning("ignoring malformed ping event: %s", event)
                continue
            try:
                self.rpc("agent.pingResult", {
                    "task_id": task_id,
                    "ping_type": params.get("ping_type", "icmp"),
                    "value": random.randint(10, 100),
                    "finished_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                }, f"ping-{task_id}-{time.time_ns()}")
                logging.info("reported fake ping result for task %d", task_id)
                if event_id:
                    with self.ack_lock:
                        self.pending_ack_ids.append(event_id)
            except RuntimeError as exc:
                logging.warning("could not report ping result for task %d: %s", task_id, exc)

    def pull_loop(self) -> None:
        while not self.stop_event.is_set():
            try:
                self.rpc("agent.pull", {
                    "capabilities": ["ping"],
                    "ack_event_ids": self.take_ack_ids(),
                }, f"pull-{time.time_ns()}")
            except RuntimeError as exc:
                if not self.stop_event.is_set():
                    logging.warning("event pull failed: %s", exc)
                    self.stop_event.wait(3)

    def run(self) -> None:
        self.upload_basic_info()
        logging.info("connected as %s; terminal and exec are deliberately unsupported", self.uuid)
        threading.Thread(target=self.pull_loop, name="event-pull", daemon=True).start()
        while not self.stop_event.is_set():
            try:
                self.rpc("agent.report", {"report": self.report(), "ack_event_ids": self.take_ack_ids()}, f"report-{time.time_ns()}")
                logging.info("reported fake metrics")
            except RuntimeError as exc:
                logging.warning("metric report failed: %s", exc)
            self.stop_event.wait(self.interval)


def load_client(state_file: Path) -> dict[str, str]:
    try:
        client = json.loads(state_file.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise RuntimeError(f"no saved client at {state_file}; run with -new") from exc
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"invalid saved client file {state_file}: {exc}") from exc
    if not all(isinstance(client.get(key), str) and client[key] for key in ("server", "uuid", "token")):
        raise RuntimeError(f"saved client file {state_file} lacks server, uuid, or token")
    return client


def register_client(server: str, adkey: str, name: str, timeout: float) -> dict[str, str]:
    url = f"{server.rstrip('/')}/api/clients/register?name={quote(name, safe='')}"
    request = Request(url, data=b"{}", headers={"Authorization": f"Bearer {adkey}", "Content-Type": "application/json"}, method="POST")
    try:
        with urlopen(request, timeout=timeout) as response:
            result = json.loads(response.read())
    except HTTPError as exc:
        raise RuntimeError(f"registration failed, HTTP {exc.code}: {exc.read().decode(errors='replace')}") from exc
    except URLError as exc:
        raise RuntimeError(f"registration network error: {exc.reason}") from exc
    data = result.get("data", {})
    if result.get("status") != "success" or not data.get("uuid") or not data.get("token"):
        raise RuntimeError(f"unexpected registration response: {result}")
    return {"server": server.rstrip("/"), "uuid": data["uuid"], "token": data["token"], "name": name}


def main() -> int:
    parser = argparse.ArgumentParser(description="Generate fake Komari v2 agent reports and handle only ping events.")
    parser.add_argument("--server", required=True, help="Komari server URL, e.g. https://komari.example.com")
    parser.add_argument("--adkey", required=True, help="AutoDiscovery key; used only with -new")
    parser.add_argument("-new", action="store_true", dest="new_client", help="register and save a new fake client")
    parser.add_argument("--name", default=f"fake-{socket.gethostname()}", help="name suffix for a newly registered client")
    parser.add_argument("--state", type=Path, default=DEFAULT_STATE_FILE, help=f"local client state file (default: {DEFAULT_STATE_FILE.name})")
    parser.add_argument("--interval", type=float, default=3.0, help="metric report interval in seconds (default: 3)")
    parser.add_argument("--timeout", type=float, default=35.0, help="HTTP timeout in seconds (default: 35)")
    args = parser.parse_args()
    if args.interval <= 0 or args.timeout <= 0:
        parser.error("--interval and --timeout must be positive")

    try:
        if args.new_client:
            client = register_client(args.server, args.adkey, args.name, args.timeout)
            args.state.write_text(json.dumps(client, indent=2) + "\n", encoding="utf-8")
            logging.info("saved new client %s to %s", client["uuid"], args.state)
        else:
            client = load_client(args.state)
            if client["server"].rstrip("/") != args.server.rstrip("/"):
                raise RuntimeError(f"saved client belongs to {client['server']}; use its server or run with -new")
        FakeAgent(args.server, client, args.interval, args.timeout).run()
    except KeyboardInterrupt:
        logging.info("stopped")
    except RuntimeError as exc:
        logging.error("%s", exc)
        return 1
    return 0


if __name__ == "__main__":
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
    sys.exit(main())
