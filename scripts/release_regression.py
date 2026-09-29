#!/usr/bin/env python3
"""Live, read-only RunOS release regression across CLI, API, MCP and Kubernetes.

The Console and mutating journeys in the companion runbook require a human/agent
to drive the UI and dispose of owned test resources through RunOS.
"""

import argparse
import json
import os
import subprocess
import sys
import urllib.error
import urllib.request
from datetime import datetime, timezone
from pathlib import Path


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--cli", default="runos", help="Path to the exact candidate CLI binary")
    p.add_argument("--api", default="https://api.dev.runos.com")
    p.add_argument("--account", required=True)
    p.add_argument("--other-account", help="Second dev account for a primary-token isolation check")
    p.add_argument("--vm-cluster", required=True)
    p.add_argument("--gpu-cluster", required=True)
    p.add_argument("--patch-cluster", required=True)
    p.add_argument("--cli-version", required=True)
    p.add_argument("--manifest", required=True)
    p.add_argument("--dev-kubeconfig", required=True, help="Foreman dev deployment kubeconfig")
    p.add_argument("--conductor", required=True)
    p.add_argument("--console", required=True)
    p.add_argument("--nodeward", required=True)
    p.add_argument("--api-key-file", help="0600 Foreman materialized credential, never committed")
    p.add_argument("--other-api-key-file", help="Second dev account PAT for isolation checks")
    p.add_argument("--report", required=True, help="Write a secret-free JSON report here")
    a = p.parse_args()
    if a.api != "https://api.dev.runos.com":
        p.error("This release runner only permits the dev API")
    if not any(x in a.cli_version for x in ("-rc.", "dev-")):
        p.error("Supply a dev candidate CLI version")
    env = dict(os.environ, RUNOS_API_URL=a.api)
    checks = []

    def check(name, fn):
        try:
            detail = fn()
            checks.append({"name": name, "result": "PASS", "detail": str(detail)[:500]})
            print("PASS", name)
        except Exception as exc:
            checks.append({"name": name, "result": "FAIL", "detail": str(exc)[:500]})
            print("FAIL", name, str(exc)[:180], file=sys.stderr)

    def need(value, reason):
        if not value:
            raise AssertionError(reason)
        return value

    def cli(*parts):
        cp = subprocess.run([a.cli, *parts], text=True, capture_output=True, env=env, timeout=45)
        need(cp.returncode == 0, f"CLI exit {cp.returncode}: {cp.stderr[-250:]}")
        return json.loads(cp.stdout)

    def api(path, key=None):
        headers = {"Authorization": "Bearer " + key} if key else {}
        req = urllib.request.Request(a.api + path, headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=20) as response:
                return response.status, json.load(response)
        except urllib.error.HTTPError as exc:
            return exc.code, json.loads(exc.read().decode())

    def token(path):
        if not path:
            return None
        for line in Path(path).read_text().splitlines():
            if "runos_pat_" in line:
                return line[line.index("runos_pat_"):].split()[0].strip('"\'')
        raise ValueError("Credential file has no RunOS PAT")

    key, other_key = token(a.api_key_file), token(a.other_api_key_file)
    check("candidate CLI version", lambda: need(a.cli_version in subprocess.check_output([a.cli, "--version"], text=True).strip(), "wrong CLI version"))
    check("dev URL pinned", lambda: need(cli("config", "get", "-j").get("api-url") == a.api, "CLI points elsewhere"))
    check("exact manifest", lambda: need(cli("manifest", "show", "-j")["version"].startswith(a.manifest), "manifest mismatch"))
    modules = None
    def modules_check():
        nonlocal modules
        modules = cli("account", "modules", "-j")["modules"]
        enabled = {x["key"] for x in modules if x["enabled"]}
        need({"virt", "provider", "capacity"} <= enabled, "release modules missing")
        return sorted(enabled)
    check("release modules enabled", modules_check)
    versions = None
    def versions_check():
        nonlocal versions
        versions = {x["version"]: x for x in cli("clusters", "k8s-versions", "-j")}
        need(versions["1.33"].get("retired"), "1.33 not retired")
        need(versions["1.35"].get("isDefault"), "1.35 not default")
        need(versions["1.36"]["status"] == "offered", "1.36 missing")
        return "1.33 retired; 1.35 default; 1.36 offered"
    check("Kubernetes version policy", versions_check)
    clusters = None
    def clusters_check():
        nonlocal clusters
        clusters = cli("clusters", "list", "-j")["clusters"]
        have = {x["cid"] for x in clusters}
        need({a.vm_cluster, a.gpu_cluster, a.patch_cluster} <= have, "test cluster missing")
        return f"{len(clusters)} clusters"
    check("cluster inventory", clusters_check)
    patch = None
    def patch_check():
        nonlocal patch
        patch = cli("clusters", "k8s-patch-status", "--cid", a.patch_cluster, "-j")
        need(patch["complete"] and patch["upgradeState"] == "normal", "patch incomplete")
        need(all(x["state"] == "up_to_date" and not x["leftCordoned"] for x in patch["nodes"]), "node needs patch or is cordoned")
        need(patch["cilium"]["state"] == "up_to_date", "Cilium needs patch")
        return f"{len(patch['nodes'])} nodes at {patch['targetPatch']}; Cilium {patch['cilium']['target']}"
    check("Kubernetes patch roll settled", patch_check)
    virt = None
    def virt_check():
        nonlocal virt
        virt = cli("virt", "status", "--cid", a.vm_cluster, "-j")
        need(virt["ready"] and virt["kubevirt"]["phase"] == "Deployed" and virt["cdi"]["phase"] == "Deployed", "virt not ready")
        return f"KubeVirt {virt['kubevirt']['observedVersion']}; CDI {virt['cdi']['observedVersion']}"
    check("VM host virtualization ready", virt_check)
    check("VM create choices", lambda: need(cli("vms", "create-options", "--cid", a.vm_cluster, "-j")["storageGroups"], "no VM disk pool"))
    capacity = None
    def capacity_check():
        nonlocal capacity
        capacity = cli("capacity", "status", "--cid", a.gpu_cluster, "-j")
        need(capacity["enabled"] and not capacity["unreadable"], "capacity not healthy")
        need(capacity["priorityClasses"]["installed"], "priority classes missing")
        return "capacity enabled with service tiers present"
    check("GPU Capacity cluster setup", capacity_check)
    check("GPU engine choices", lambda: need(cli("gpu", "operator-config", "--cid", a.gpu_cluster, "-j")["engineVersions"], "no vLLM engines"))
    check("VM group inventory", lambda: need(cli("vm-groups", "list", "--cid", a.vm_cluster, "-j")["groups"], "no VM groups"))
    check("service inventory", lambda: need(cli("services", "list", "--cid", a.vm_cluster, "-j")["services"], "no services"))

    # These reads follow the release journeys even when the disposable fixtures
    # for a mutating round have already been removed. They check the user-facing
    # shapes and safety metadata, not merely that an endpoint answered 200.
    for label, command, field in (
        ("VM cluster node inventory", ("nodes", "list", "--cid", a.vm_cluster, "-j"), "nodes"),
        ("GPU cluster node inventory", ("nodes", "list", "--cid", a.gpu_cluster, "-j"), "nodes"),
        ("app inventory", ("apps", "list", "--cid", a.vm_cluster, "-j"), "apps"),
        ("VM inventory", ("vms", "list", "--cid", a.vm_cluster, "-j"), "vms"),
        ("VM networks", ("vm-networks", "list", "--cid", a.vm_cluster, "-j"), "networks"),
        ("VM address blocks", ("vm-address-blocks", "list", "--cid", a.vm_cluster, "-j"), "blocks"),
        ("VM images", ("vm-images", "list", "--cid", a.vm_cluster, "-j"), "images"),
        ("bare-metal provider inventory", ("provider", "servers", "-j"), "servers"),
    ):
        check(label, lambda command=command, field=field: need(isinstance(cli(*command).get(field), list), f"{field} is not a list"))

    def desired_versions():
        value = cli("clusters", "desired-versions", "show", "--cid", a.patch_cluster, "-j")
        need(value.get("desiredK8sVersion") in versions, "desired minor absent from matrix")
        need(all(value.get(x) for x in ("ciliumVersion", "containerdVersion", "helmVersion")), "component pin missing")
        return value["desiredK8sVersion"]
    check("cluster component pins", desired_versions)

    def backup_candidates():
        value = cli("backups", "candidates", "--cid", a.vm_cluster, "-j")
        need(isinstance(value, list), "backup candidates is not a list")
        return f"{len(value)} candidates"
    check("backup candidates", backup_candidates)
    check("backup destinations", lambda: need(isinstance(cli("backups", "destinations", "--cid", a.vm_cluster, "-j"), list), "destinations is not a list"))

    version_types = (
        "cert-manager", "clickhouse", "grafana", "harbor", "kafka", "langfuse",
        "litellm", "minio", "mysql", "netbird-client", "netbird-server", "ollama",
        "postgresql", "prometheus", "rabbitmq", "traefik", "umami", "valkey",
        "vector", "vllm",
    )
    for service_type in version_types:
        def picker(service_type=service_type):
            value = cli("service-info", "versions", service_type, "-j")
            options = value.get("options")
            need(isinstance(options, list) and options, "empty version picker")
            current = [x for x in options if not x.get("retired")]
            need(current, "no current version")
            need(sum(bool(x.get("isDefault")) for x in options) == 1, "picker must have one default")
            need(not any(x.get("isDefault") and x.get("retired") for x in options), "retired default")
            need(all(x.get("value") and x.get("label") for x in options), "version option lacks value/label")
            return f"{len(current)} current, {len(options)-len(current)} retired"
        check(f"{service_type} version policy", picker)

    if key:
        def api_clusters():
            status, body = api(f"/{a.account}/clusters", key)
            need(status == 200, f"HTTP {status}")
            need({x["cid"] for x in body["clusters"]} == {x["cid"] for x in clusters}, "API/CLI clusters differ")
            return "API and CLI cluster lists agree"
        check("API/CLI cluster parity", api_clusters)
        def api_modules():
            status, body = api(f"/{a.account}/modules", key)
            need(status == 200, f"HTTP {status}")
            need({x["key"] for x in body["modules"] if x["enabled"]} == {x["key"] for x in modules if x["enabled"]}, "API/CLI modules differ")
            return "API and CLI modules agree"
        check("API/CLI module parity", api_modules)
        check("anonymous account refused", lambda: need(api(f"/{a.account}/clusters")[0] == 401, "anonymous access allowed"))
        if a.other_account:
            check("primary token refused on other account", lambda: need(
                api(f"/{a.other_account}/clusters", key)[0] == 403,
                "primary account token crossed the account boundary"))
        if other_key:
            check("cross-account read refused", lambda: need(api(f"/{a.account}/clusters", other_key)[0] == 403, "other account can read"))
    else:
        check("API credentials supplied", lambda: need(False, "pass --api-key-file"))

    mcp = subprocess.Popen([a.cli, "mcp", "serve", "read"], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                           stderr=subprocess.DEVNULL, text=True, env=env)
    def rpc(i, method, params=None):
        mcp.stdin.write(json.dumps({"jsonrpc": "2.0", "id": i, "method": method, "params": params or {}}) + "\n")
        mcp.stdin.flush()
        return json.loads(mcp.stdout.readline())
    try:
        check("MCP candidate bootstrap", lambda: need(rpc(1, "initialize", {"protocolVersion": "2025-03-26", "capabilities": {}, "clientInfo": {"name": "release-regression", "version": "1"}})["result"]["serverInfo"]["version"] == a.cli_version, "MCP binary mismatch"))
        def mcp_call(i, name, arguments):
            result = rpc(i, "tools/call", {"name": name, "arguments": arguments})["result"]
            need(not result.get("isError"), f"MCP {name} failed")
            value = json.loads(result["content"][0]["text"])
            return value
        check("MCP bootstrap", lambda: need(mcp_call(2, "mcp_bootstrap", {})["cliUpdate"]["localVersion"] == a.cli_version, "MCP bootstrap mismatch"))
        check("MCP/CLI clusters parity", lambda: need({x["cid"] for x in mcp_call(3, "clusters_list", {})["clusters"]} == {x["cid"] for x in clusters}, "MCP clusters differ"))
        check("MCP/CLI modules parity", lambda: need({x["key"] for x in mcp_call(4, "account_modules", {})["modules"] if x["enabled"]} == {x["key"] for x in modules if x["enabled"]}, "MCP modules differ"))
        check("MCP/CLI capacity parity", lambda: need(mcp_call(5, "capacity_status", {"cid": a.gpu_cluster})["priorityClasses"] == capacity["priorityClasses"], "MCP capacity differs"))
    finally:
        mcp.terminate()
        mcp.wait(timeout=5)

    # This kubeconfig reaches the platform's dev namespace without a personal VPN.
    # The patch-cluster's kubeconfig uses a private VPN address; its patch state is
    # checked above through RunOS while this read proves the exact serving images.
    def deployment(name, version):
        out = subprocess.check_output(["kubectl", "--kubeconfig", a.dev_kubeconfig, "-n", "runos-dev",
                                       "get", "deployment", name, "-o", "json"], text=True, timeout=30)
        obj = json.loads(out)
        images = [x["image"] for x in obj["spec"]["template"]["spec"]["containers"]]
        need(any(version in x for x in images), f"{name} image mismatch: {images}")
        need(obj["status"].get("readyReplicas", 0) == obj["spec"]["replicas"], f"{name} replicas not Ready")
        return f"{obj['status']['readyReplicas']} Ready; {version}"
    check("direct Kubernetes Conductor image", lambda: deployment("conductor", a.conductor))
    check("direct Kubernetes Console image", lambda: deployment("console", a.console))
    check("direct Kubernetes Nodeward image", lambda: deployment("nodeward", a.nodeward))

    report = {"createdAt": datetime.now(timezone.utc).isoformat(), "api": a.api,
              "candidate": {"cli": a.cli_version, "manifest": a.manifest,
                            "conductor": a.conductor, "console": a.console, "nodeward": a.nodeward},
              "clusters": {"vm": a.vm_cluster, "gpu": a.gpu_cluster, "patch": a.patch_cluster},
              "checks": checks, "passed": sum(x["result"] == "PASS" for x in checks),
              "failed": sum(x["result"] == "FAIL" for x in checks)}
    Path(a.report).write_text(json.dumps(report, indent=2) + "\n")
    print(f"{report['passed']} passed, {report['failed']} failed; {a.report}")
    return 1 if report["failed"] else 0


if __name__ == "__main__":
    sys.exit(main())
