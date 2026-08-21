# /// script
# requires-python = ">=3.10"
# dependencies = []
# ///
"""Build an idempotent CoreDNS ConfigMap patch for localtest.me."""

from __future__ import annotations

import copy
import json
import re
import sys
from pathlib import Path

BEGIN = "# BEGIN saas-accounts-gateway"
END = "# END saas-accounts-gateway"
HOSTS = ("auth.localtest.me", "datahub.localtest.me", "chat.localtest.me")


def strip_marked(text: str) -> str:
    lines = text.splitlines()
    output: list[str] = []
    managed = False
    starts = 0
    ends = 0
    for line in lines:
        marker = line.strip()
        if marker == BEGIN:
            if managed:
                raise SystemExit("nested managed CoreDNS block")
            managed = True
            starts += 1
            continue
        if marker == END:
            if not managed:
                raise SystemExit("managed CoreDNS end marker without start")
            managed = False
            ends += 1
            continue
        if not managed:
            output.append(line)
    if managed or starts != ends:
        raise SystemExit("unterminated managed CoreDNS block")
    rendered = "\n".join(output)
    if text.endswith("\n") and rendered:
        rendered += "\n"
    return rendered


def transform(ip: str, data: dict[str, str]) -> tuple[str, dict[str, str]]:
    if not isinstance(data.get("Corefile"), str):
        raise SystemExit("CoreDNS ConfigMap has no data.Corefile")

    updated = copy.deepcopy(data)
    corefile = strip_marked(updated["Corefile"])
    nodehosts_plugins = re.findall(
        r"(?m)^[ \t]*hosts[ \t]+/etc/coredns/NodeHosts(?:[ \t]*\{|[ \t]*$)",
        corefile,
    )
    if len(nodehosts_plugins) > 1:
        raise SystemExit("multiple CoreDNS NodeHosts plugins found")

    if len(nodehosts_plugins) == 1:
        mode = "NodeHosts"
        current_hosts = strip_marked(updated.get("NodeHosts", ""))
        if current_hosts and not current_hosts.endswith("\n"):
            current_hosts += "\n"
        records = "\n".join(f"{ip} {host}" for host in HOSTS)
        updated["NodeHosts"] = f"{current_hosts}{BEGIN}\n{records}\n{END}\n"
        updated["Corefile"] = corefile
        return mode, updated

    mode = "inline Corefile hosts"
    if re.findall(r"(?m)^[ \t]*hosts(?:[ \t]|\{)", corefile):
        raise SystemExit(
            "CoreDNS has a hosts plugin not managed by this demo; "
            "refusing to add a second one"
        )
    server = re.search(r"(?m)^([ \t]*)\.:53[ \t]*\{[ \t]*\n", corefile)
    if not server:
        raise SystemExit("CoreDNS Corefile has no .:53 server block")
    indent = server.group(1) + "    "
    records = "\n".join(f"{indent}    {ip} {host}" for host in HOSTS)
    block = (
        f"{indent}{BEGIN}\n"
        f"{indent}hosts {{\n"
        f"{records}\n"
        f"{indent}    fallthrough\n"
        f"{indent}}}\n"
        f"{indent}{END}\n"
    )
    updated["Corefile"] = corefile[: server.end()] + block + corefile[server.end() :]
    if "NodeHosts" in updated:
        updated["NodeHosts"] = strip_marked(updated["NodeHosts"])
    return mode, updated


def main() -> None:
    if len(sys.argv) != 6:
        raise SystemExit(
            "usage: coredns_transform.py GATEWAY_IP CURRENT PATCH ROLLBACK STATUS"
        )
    ip, current_path, patch_path, rollback_path, status_path = sys.argv[1:]
    obj = json.loads(Path(current_path).read_text())
    data = obj.get("data")
    if not isinstance(data, dict):
        raise SystemExit("CoreDNS ConfigMap has no data object")

    mode, updated = transform(ip, data)
    changed = updated != data
    Path(patch_path).write_text(json.dumps({"data": updated}, separators=(",", ":")))

    rollback_data = copy.deepcopy(data)
    for key in updated:
        if key not in data:
            rollback_data[key] = None
    Path(rollback_path).write_text(
        json.dumps({"data": rollback_data}, separators=(",", ":"))
    )
    Path(status_path).write_text(
        f"{mode}\n{'changed' if changed else 'unchanged'}\n"
    )


if __name__ == "__main__":
    main()
