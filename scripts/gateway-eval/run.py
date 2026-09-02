#!/usr/bin/env python3
"""Measure the gateway against the per-command surface with a REAL agent.

FPL30 step measure-agents, the gate. Cheap in tokens is not the same as usable,
so this runs the same tasks against both surfaces through `claude -p` and
records what actually happened: tokens, turns, cost, and whether the answer was
right.

--strict-mcp-config is what makes the comparison honest. Without it the run
inherits every MCP server configured on this machine, and the agent could answer
from a server that is not under test.
"""
import argparse, json, os, subprocess, sys, time

HERE = os.path.dirname(os.path.abspath(__file__))
BIN = os.environ.get("RUNOS_EVAL_BIN", "runos")

SURFACES = {
    "gateway": {"mcpServers": {"runos": {
        "command": BIN, "args": ["mcp", "serve", "gateway", "--mode=ro"]}}},
    "per-command": {"mcpServers": {"runos": {
        "command": BIN, "args": ["mcp", "serve", "read"]}}},
}


def run_task(surface, cfg, task, model, timeout):
    cmd = [
        "claude", "-p", task["prompt"],
        "--output-format", "json",
        "--mcp-config", json.dumps(cfg),
        "--strict-mcp-config",
        "--permission-mode", "bypassPermissions",
        "--model", model,
    ]
    t0 = time.time()
    try:
        p = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        return {"surface": surface, "task": task["id"], "error": "timeout",
                "wall_s": round(time.time() - t0, 1)}
    wall = round(time.time() - t0, 1)
    if p.returncode != 0:
        return {"surface": surface, "task": task["id"], "error": "exit %d" % p.returncode,
                "stderr": p.stderr[-400:], "wall_s": wall}
    try:
        out = json.loads(p.stdout)
    except json.JSONDecodeError:
        return {"surface": surface, "task": task["id"], "error": "unparseable output",
                "raw": p.stdout[-400:], "wall_s": wall}

    text = (out.get("result") or "")
    usage = out.get("usage") or {}
    # A hit is generous on purpose: we are measuring whether the agent got
    # there at all, not how it worded the answer.
    hit = any(e.lower() in text.lower() for e in task["expect_any"])
    return {
        "surface": surface,
        "task": task["id"],
        "ok": hit,
        "turns": out.get("num_turns"),
        "in_tok": usage.get("input_tokens"),
        "out_tok": usage.get("output_tokens"),
        "cache_read": usage.get("cache_read_input_tokens"),
        "cache_write": usage.get("cache_creation_input_tokens"),
        "cost_usd": out.get("total_cost_usd"),
        "wall_s": wall,
        "answer": text.strip()[:160],
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--model", default="claude-sonnet-5")
    ap.add_argument("--timeout", type=int, default=300)
    ap.add_argument("--surfaces", default="gateway,per-command")
    ap.add_argument("--task", help="run one task by id")
    ap.add_argument("--out", default=os.path.join(HERE, "results.json"))
    a = ap.parse_args()

    tasks = json.load(open(os.path.join(HERE, "tasks.json")))
    if a.task:
        tasks = [t for t in tasks if t["id"] == a.task]
        if not tasks:
            sys.exit("no such task")

    results = []
    for surface in a.surfaces.split(","):
        cfg = SURFACES[surface]
        for t in tasks:
            print("  %-12s %-16s ..." % (surface, t["id"]), end="", flush=True)
            r = run_task(surface, cfg, t, a.model, a.timeout)
            results.append(r)
            if "error" in r:
                print(" ERROR: %s" % r["error"])
            else:
                print(" %s  turns=%-3s in=%-7s cache_r=%-8s %.1fs" % (
                    "ok " if r["ok"] else "MISS", r["turns"], r["in_tok"], r["cache_read"], r["wall_s"]))
    json.dump(results, open(a.out, "w"), indent=1)
    report(results)


def report(results):
    print("\n" + "=" * 74)
    by = {}
    for r in results:
        by.setdefault(r["surface"], []).append(r)
    # cache_read is dominated by the HOST's own context (system prompt, its
    # own tools, skills), not by the server under test. The signal is the
    # DELTA between surfaces, so that is what gets reported.
    print("%-13s %5s %6s %10s %12s %9s %8s" % (
        "SURFACE", "ok", "turns", "input", "cache read", "cost $", "wall s"))
    print("-" * 74)
    for s, rows in by.items():
        good = [r for r in rows if "error" not in r]
        if not good:
            print("%-13s all runs errored" % s); continue
        print("%-13s %2d/%-2d %6.1f %10d %12d %9.4f %8.1f" % (
            s,
            sum(1 for r in good if r["ok"]), len(good),
            sum(r["turns"] or 0 for r in good) / len(good),
            sum(r["in_tok"] or 0 for r in good),
            sum(r["cache_read"] or 0 for r in good),
            sum(r["cost_usd"] or 0 for r in good),
            sum(r["wall_s"] for r in good)))
    if len(by) == 2:
        (a_s, a_r), (b_s, b_r) = list(by.items())
        ga = [r for r in a_r if "error" not in r]
        gb = [r for r in b_r if "error" not in r]
        if ga and gb:
            da = sum(r["cache_read"] or 0 for r in ga) / len(ga)
            db = sum(r["cache_read"] or 0 for r in gb) / len(gb)
            print("\nDELTA, mean cache read per task: %s %d vs %s %d  ->  %+d (%.0f%%)" % (
                a_s, da, b_s, db, da - db, (da - db) / db * 100 if db else 0))
    print("\nPer task:")
    ids = []
    for r in results:
        if r["task"] not in ids: ids.append(r["task"])
    for tid in ids:
        line = "  %-16s" % tid
        for s in by:
            m = [r for r in by[s] if r["task"] == tid]
            if not m: continue
            r = m[0]
            if "error" in r:
                line += "  %s: ERROR" % s
            else:
                line += "  %s: %s turns=%s cache_r=%s" % (
                    s, "ok" if r["ok"] else "MISS", r["turns"], r["cache_read"])
        print(line)


if __name__ == "__main__":
    main()
