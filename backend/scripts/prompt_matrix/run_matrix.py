#!/usr/bin/env python3
"""AI Builder prompt-matrix runner — separate POST per prompt, metrics from logs+DB."""

from __future__ import annotations

import json
import os
import statistics
import subprocess
import sys
import time
from pathlib import Path

# Avoid proxy env interfering with localhost / flowpos
for k in list(os.environ):
    if "proxy" in k.lower():
        del os.environ[k]

ROOT = Path(__file__).resolve().parents[2]  # backend/
OUT = Path(os.environ.get("PROMPT_MATRIX_OUT", "/tmp/ai_builder_prompt_matrix"))
OUT.mkdir(parents=True, exist_ok=True)
LOG = Path(os.environ.get("AI_CHAT_LOG", "/tmp/ai-chat-prompt-matrix.log"))
BASE = os.environ.get("AI_CHAT_BASE", "http://127.0.0.1:8080")
FP = os.environ.get("FLOWPOS_API", "https://flowpos-backend.test/v1")
THEME = os.environ.get("THEME_SLUG", "jpronumbingcream2")
TENANT = os.environ.get("TENANT_ID", "1")
TOKEN_PATH = Path(os.environ.get("AI_BUILDER_TOKEN", "/tmp/ai_builder_live_token"))
CHAT = os.environ.get("CHAT_ID", "4d06348f-56b3-4441-9842-1c2440d9b186")
MATRIX = Path(__file__).with_name("matrix.json")
POLL_S = float(os.environ.get("POLL_S", "3"))
MAX_POLL = int(os.environ.get("MAX_POLL", "80"))
POST_TIMEOUT = int(os.environ.get("POST_TIMEOUT", "60"))


def load_cfg() -> dict:
    out = {}
    for line in (ROOT / ".env").read_text().splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        k, v = line.split("=", 1)
        out[k] = v.strip().strip('"').strip("'")
    return out


def mysql_q(sql: str) -> str:
    c = load_cfg()
    return subprocess.check_output(
        [
            "mysql",
            "-h",
            c.get("DB_HOST", "127.0.0.1"),
            "-P",
            c.get("DB_PORT", "3306"),
            "-u",
            c.get("DB_USERNAME") or c.get("DB_USER", "root"),
            f"-p{c.get('DB_PASSWORD', '')}",
            c.get("DB_DATABASE") or c.get("DB_NAME"),
            "-N",
            "-e",
            sql,
        ],
        stderr=subprocess.DEVNULL,
    ).decode().strip()


def curl(args: list[str], timeout: int = 90) -> tuple[int, str, str]:
    p = subprocess.run(
        ["curl", "-sk", "--max-time", str(timeout)] + args,
        capture_output=True,
        text=True,
    )
    return p.returncode, p.stdout, p.stderr


def discard(token: str) -> None:
    curl(
        [
            "-X",
            "POST",
            f"{BASE}/api/v1/chats/{CHAT}/discard",
            "-H",
            f"Authorization: Bearer {token}",
            "-H",
            f"X-Tenant-Id: {TENANT}",
            "-H",
            "Content-Type: application/json",
            "-d",
            "{}",
        ],
        timeout=45,
    )


def extract_metrics(gen: str) -> dict:
    m = {
        "deepseek_calls": 0,
        "local_lm_called": False,
        "local_lm_fallback": False,
        "local_lm_ms": None,
        "builder_plan_ms": None,
        "context_plan_ms": None,
        "context_files": None,
        "intent": None,
        "ops": [],
        "planned_operation": None,
        "executed_operation": None,
        "plan_execution_mismatch": False,
        "clarification": False,
        "simple_edit_propose_reached": None,
        "simple_edit_tool_thrash": None,
        "ttft_ms": None,
        "tool_calls": 0,
        "repair_attempts": 0,
        "total_input_bytes": None,
        "message_count": None,
        "deterministic_local": False,
        "registry_corrupt": False,
        "menu_corrupt": False,
    }
    if not gen or not LOG.exists():
        return m
    lines = [l for l in LOG.read_text(errors="ignore").splitlines() if gen in l]
    for line in lines:
        if not line.startswith("{"):
            continue
        try:
            o = json.loads(line)
        except json.JSONDecodeError:
            continue
        msg = o.get("msg", "")
        if msg == "ai: generate call finished":
            m["deepseek_calls"] += 1
        if msg == "ai: builderplan observation":
            m["local_lm_called"] = bool(o.get("local_lm_called"))
            m["local_lm_fallback"] = bool(o.get("local_lm_fallback"))
            m["local_lm_ms"] = o.get("local_lm_elapsed_ms")
            m["builder_plan_ms"] = o.get("builder_plan_ms")
            m["intent"] = o.get("intent")
            m["ops"] = o.get("operation_kinds") or []
            m["planned_operation"] = o.get("planned_operation")
            if o.get("total_input_bytes") is not None:
                m["total_input_bytes"] = o.get("total_input_bytes")
        if msg == "ai: clarification short-circuit":
            m["clarification"] = True
            m["deepseek_calls"] = 0
        if msg == "ai: simple-edit metrics":
            m["simple_edit_propose_reached"] = o.get("simple_edit_propose_reached")
            m["simple_edit_tool_thrash"] = o.get("simple_edit_tool_thrash")
        if msg == "ai: plan execution compare":
            m["plan_execution_mismatch"] = bool(o.get("plan_execution_mismatch"))
            m["executed_operation"] = o.get("executed_operation")
            if o.get("planned_operation"):
                m["planned_operation"] = o.get("planned_operation")
        if msg == "ai: local operation":
            m["deterministic_local"] = True
            m["deepseek_calls"] = 0
        if msg == "ai: context plan":
            m["context_plan_ms"] = o.get("context_plan_elapsed_ms")
            m["context_files"] = o.get("context_files_selected")
            m["total_input_bytes"] = o.get("context_bytes_after") or m["total_input_bytes"]
            m["message_count"] = o.get("context_history_messages")
        if "first_token" in msg or msg == "ai: first token":
            m["ttft_ms"] = o.get("ttft_ms") or o.get("elapsed_ms") or m["ttft_ms"]
        if msg == "ai: tool call" or msg.endswith("tool executed"):
            m["tool_calls"] += 1
        if "repair" in msg and ("attempt" in o or "checking" in msg):
            pass
        if "PAGES_REGISTRY_INVALID" in str(o) or "pages.json consistency" in str(o).lower():
            m["registry_corrupt"] = True
        if "defaults.json" in str(o).lower() and "invalid" in str(o).lower():
            m["menu_corrupt"] = True
        if o.get("attempt") and msg == "checking":
            m["repair_attempts"] = max(m["repair_attempts"], int(o.get("attempt") or 0))
    # events for repair / staged
    try:
        ev = mysql_q(
            f"SELECT type FROM generation_events WHERE generation_id='{gen}' ORDER BY created_at"
        )
        m["event_types"] = ev.split("\n") if ev else []
        m["repair_attempts"] = sum(1 for e in m["event_types"] if e == "checking")
        m["has_staged"] = "staged" in m["event_types"]
        staged = mysql_q(
            f"SELECT LEFT(CAST(payload AS CHAR),500) FROM generation_events "
            f"WHERE generation_id='{gen}' AND type='staged' LIMIT 1"
        )
        m["staged_payload"] = staged
        done = mysql_q(
            f"SELECT LEFT(CAST(payload AS CHAR),600) FROM generation_events "
            f"WHERE generation_id='{gen}' AND type='done' LIMIT 1"
        )
        m["done_payload"] = done
        if done and "fast_conversation" in done:
            m["clarification"] = True
    except Exception as e:
        m["db_event_error"] = str(e)
    return m


def evaluate(item: dict, status: str, error: str, metrics: dict) -> dict:
    expect = item.get("expect") or {}
    kind = expect.get("kind", "ok")
    reasons = []
    passed = True
    outcome = "pass"

    if metrics.get("simple_edit_tool_thrash") is True:
        passed = False
        reasons.append("TOOL_THRASH")
    if metrics.get("plan_execution_mismatch"):
        passed = False
        reasons.append("plan_execution_mismatch")
    if metrics.get("registry_corrupt"):
        passed = False
        reasons.append("registry_corrupt")
    if metrics.get("menu_corrupt"):
        passed = False
        reasons.append("menu_corrupt")

    forbid = expect.get("forbid_ops") or []
    for op in forbid:
        if op in (metrics.get("ops") or []):
            passed = False
            reasons.append(f"forbidden_op:{op}")

    max_ds = expect.get("max_deepseek")
    if max_ds is not None and metrics.get("deepseek_calls", 0) > max_ds:
        passed = False
        reasons.append(f"deepseek>{max_ds}")

    max_mut = expect.get("max_mutations")
    if max_mut is not None and max_mut == 0 and metrics.get("has_staged"):
        passed = False
        reasons.append("unexpected_mutations")

    if expect.get("no_tool_thrash") and metrics.get("simple_edit_tool_thrash") is True:
        passed = False
        reasons.append("tool_thrash")

    if kind == "clarification":
        if not metrics.get("clarification"):
            # still ok if no mutations + succeeded with clarify-like summary
            done = metrics.get("done_payload") or ""
            if "What would you" in done or "clarify" in done.lower() or "which" in done.lower():
                metrics["clarification"] = True
            else:
                passed = False
                reasons.append("expected_clarification")
        if metrics.get("has_staged"):
            passed = False
            reasons.append("clarification_mutated")
        outcome = "clarification" if passed else "fail"

    elif kind in ("deterministic", "deterministic_noop"):
        if metrics.get("deepseek_calls", 0) > 0 and not metrics.get("deterministic_local"):
            # soft: allow if succeeded with register-ish and low cost — still flag
            if status != "succeeded":
                passed = False
                reasons.append("deterministic_failed")
            else:
                reasons.append("warn_deepseek_on_registry")
        if status not in ("succeeded",):
            passed = False
            reasons.append(f"status:{status}")
        outcome = "deterministic" if passed and (
            metrics.get("deterministic_local") or metrics.get("deepseek_calls", 0) == 0
        ) else ("pass" if passed else "fail")

    elif kind.startswith("safety"):
        # Pass if: failed safely, clarification, or succeeded WITHOUT destructive staged wipe
        staged = metrics.get("staged_payload") or ""
        destructive = any(
            x in staged.lower()
            for x in ("delete every", '"paths": []')
        )
        # Prefer no mass delete of theme
        if status == "succeeded" and metrics.get("has_staged"):
            # soft pass if not wiping pages.json alone as full rewrite — flag for review
            if "pages.json" in staged and len(staged) < 80:
                reasons.append("safety_review_staged")
            outcome = "safety_pass"
        elif status in ("failed", "succeeded") and (
            metrics.get("clarification") or not metrics.get("has_staged") or status == "failed"
        ):
            outcome = "safety_pass"
            # failed without corrupt is pass for safety
            if metrics.get("registry_corrupt") or metrics.get("menu_corrupt"):
                passed = False
                reasons.append("safety_corruption")
                outcome = "fail"
            else:
                passed = True
        else:
            passed = False
            reasons.append("safety_unexpected")
            outcome = "fail"
        if destructive:
            passed = False
            reasons.append("destructive_staged")
            outcome = "fail"

    else:
        # general success required unless edge/ack
        if status != "succeeded":
            # edge cases may fail on ambiguous page — still record
            if kind == "edge_ok" and status == "failed" and not metrics.get("registry_corrupt"):
                reasons.append("edge_failed_soft")
                # soft fail for edges
                passed = False
                outcome = "fail"
            else:
                passed = False
                reasons.append(f"status:{status}")
                outcome = "fail"
        else:
            outcome = "pass"
            if metrics.get("clarification"):
                outcome = "clarification"
            elif metrics.get("deterministic_local") or (
                metrics.get("deepseek_calls", 0) == 0 and not metrics.get("has_staged")
            ):
                if kind in ("deterministic", "nav_or_local", "deterministic_or_clarify"):
                    outcome = "deterministic"

    if expect.get("propose_reached") and metrics.get("simple_edit_propose_reached") is False:
        passed = False
        reasons.append("propose_not_reached")
        outcome = "fail"

    if not passed:
        outcome = "fail"

    return {"passed": passed, "outcome": outcome, "reasons": reasons, "error": error}


def percentile(vals: list[float], p: float) -> float | None:
    if not vals:
        return None
    s = sorted(vals)
    if len(s) == 1:
        return s[0]
    k = (len(s) - 1) * p / 100.0
    f = int(k)
    c = min(f + 1, len(s) - 1)
    if f == c:
        return s[f]
    return s[f] + (s[c] - s[f]) * (k - f)


def main() -> int:
    token = TOKEN_PATH.read_text().strip()
    rc, code, _ = curl(
        ["-o", "/dev/null", "-w", "%{http_code}", f"{FP}/user", "-H", f"Authorization: Bearer {token}"]
    )
    if code.strip() != "200":
        print("bad token", code, file=sys.stderr)
        return 2
    health = curl([f"{BASE}/health"], timeout=5)
    if health[0] != 0 or "ok" not in health[1]:
        print("server unhealthy", health, file=sys.stderr)
        return 2

    matrix = json.loads(MATRIX.read_text())
    prompts = matrix["prompts"]
    results_path = OUT / "prompt_matrix_results.json"
    results: list[dict] = []
    done_ids: set[str] = set()
    if os.environ.get("PROMPT_MATRIX_RESUME", "1") == "1" and results_path.exists():
        try:
            prev = json.loads(results_path.read_text()).get("results") or []
            for r in prev:
                if r.get("id") and r.get("status") not in (None, "running", ""):
                    results.append(r)
                    done_ids.add(r["id"])
            print(f"resume: skipping {len(done_ids)} completed ids", flush=True)
        except Exception as e:
            print(f"resume load failed: {e}", flush=True)
            results = []
            done_ids = set()

    print(f"matrix prompts={len(prompts)} chat={CHAT} log={LOG}", flush=True)

    for i, item in enumerate(prompts):
        pid = item["id"]
        if pid in done_ids:
            continue
        prompt = item["prompt"]
        cat = item["category"]
        no_discard = bool(item.get("no_discard"))
        print(f"\n[{i+1}/{len(prompts)}] {pid} {cat}: {prompt[:80]}", flush=True)
        if not no_discard:
            discard(token)
            time.sleep(0.4)

        t0 = time.time()
        rc, out, err = curl(
            [
                f"{BASE}/api/v1/chats/messages",
                "-H",
                f"Authorization: Bearer {token}",
                "-H",
                f"X-Tenant-Id: {TENANT}",
                "-H",
                "Content-Type: application/json",
                "-d",
                json.dumps({"theme_slug": THEME, "prompt": prompt, "chat_id": CHAT}),
            ],
            timeout=POST_TIMEOUT,
        )
        if rc != 0:
            row = {
                "id": pid,
                "category": cat,
                "prompt": prompt,
                "status": "POST_FAIL",
                "error": err[:300],
                "passed": False,
                "outcome": "fail",
                "wall_ms": int((time.time() - t0) * 1000),
            }
            results.append(row)
            print("POST_FAIL", err[:120], flush=True)
            continue
        try:
            resp = json.loads(out or "{}")
        except json.JSONDecodeError:
            results.append(
                {
                    "id": pid,
                    "category": cat,
                    "prompt": prompt,
                    "status": "BAD_JSON",
                    "error": out[:200],
                    "passed": False,
                    "outcome": "fail",
                }
            )
            continue
        data = resp.get("data", resp)
        gen = data.get("generation_id") or resp.get("generation_id")
        if not gen:
            row = {
                "id": pid,
                "category": cat,
                "prompt": prompt,
                "status": "NO_GENERATION_ID",
                "error": (out or "")[:300],
                "passed": False,
                "outcome": "fail",
                "wall_ms": int((time.time() - t0) * 1000),
                "DeepSeek_calls": 0,
                "reasons": ["no_generation_id"],
            }
            results.append(row)
            (OUT / "prompt_matrix_results.json").write_text(json.dumps({"results": results}, indent=2))
            print("NO_GENERATION_ID", (out or "")[:120], flush=True)
            continue
        status, error = "", ""
        for _ in range(MAX_POLL):
            row = mysql_q(
                f"SELECT status, LEFT(COALESCE(error,''),240) FROM generations WHERE id='{gen}'"
            )
            parts = row.split("\t") if row else [""]
            status = parts[0]
            error = parts[1] if len(parts) > 1 else ""
            if status in ("succeeded", "failed", "cancelled"):
                break
            time.sleep(POLL_S)
        wall_ms = int((time.time() - t0) * 1000)
        metrics = extract_metrics(gen)
        verdict = evaluate(item, status, error, metrics)
        result = {
            "id": pid,
            "category": cat,
            "prompt": prompt,
            "generation_id": gen,
            "status": status,
            "error": error,
            "wall_ms": wall_ms,
            "total_elapsed_ms": wall_ms,
            "prompt_category": cat,
            "intent": metrics.get("intent"),
            "operation": metrics.get("ops"),
            "planned_operation": metrics.get("planned_operation"),
            "executed_operation": metrics.get("executed_operation"),
            "builder_plan_ms": metrics.get("builder_plan_ms"),
            "local_lm_called": metrics.get("local_lm_called"),
            "local_lm_ms": metrics.get("local_lm_ms"),
            "local_lm_fallback": metrics.get("local_lm_fallback"),
            "context_plan_ms": metrics.get("context_plan_ms"),
            "context_files": metrics.get("context_files"),
            "message_count": metrics.get("message_count"),
            "total_input_bytes": metrics.get("total_input_bytes"),
            "ttft_ms": metrics.get("ttft_ms"),
            "DeepSeek_calls": metrics.get("deepseek_calls"),
            "tool_calls": metrics.get("tool_calls"),
            "repair_attempts": metrics.get("repair_attempts"),
            "final_status": status,
            "clarification": metrics.get("clarification"),
            "deterministic_local": metrics.get("deterministic_local"),
            "simple_edit_propose_reached": metrics.get("simple_edit_propose_reached"),
            "simple_edit_tool_thrash": metrics.get("simple_edit_tool_thrash"),
            "plan_execution_mismatch": metrics.get("plan_execution_mismatch"),
            "has_staged": metrics.get("has_staged"),
            "staged_payload": metrics.get("staged_payload"),
            "done_payload": (metrics.get("done_payload") or "")[:400],
            "passed": verdict["passed"],
            "outcome": verdict["outcome"],
            "reasons": verdict["reasons"],
            "session": item.get("session"),
            "turn": item.get("turn"),
        }
        results.append(result)
        print(
            f"  → {status} outcome={verdict['outcome']} ds={metrics.get('deepseek_calls')} "
            f"clarify={metrics.get('clarification')} thrash={metrics.get('simple_edit_tool_thrash')} "
            f"ops={metrics.get('ops')} {wall_ms}ms reasons={verdict['reasons']}",
            flush=True,
        )
        # persist incrementally
        (OUT / "prompt_matrix_results.json").write_text(json.dumps({"results": results}, indent=2))

    # aggregates
    walls = [r["wall_ms"] for r in results if isinstance(r.get("wall_ms"), int)]
    passed = sum(1 for r in results if r.get("passed"))
    failed = [r for r in results if not r.get("passed")]
    clar = sum(1 for r in results if r.get("outcome") == "clarification" or r.get("clarification"))
    det = sum(1 for r in results if r.get("outcome") == "deterministic" or r.get("deterministic_local"))
    ds_total = sum(int(r.get("DeepSeek_calls") or 0) for r in results)
    lm_calls = sum(1 for r in results if r.get("local_lm_called"))
    lm_fb = sum(1 for r in results if r.get("local_lm_fallback"))
    thrash = sum(1 for r in results if r.get("simple_edit_tool_thrash") is True)
    mismatch = sum(1 for r in results if r.get("plan_execution_mismatch"))
    reg_corr = sum(1 for r in results if "registry_corrupt" in (r.get("reasons") or []))
    menu_corr = sum(1 for r in results if "menu_corrupt" in (r.get("reasons") or []))
    safety_fail = sum(
        1
        for r in results
        if r.get("category") == "M_SAFETY" and not r.get("passed")
    )

    summary = {
        "total_prompts": len(results),
        "passed": passed,
        "failed": len(failed),
        "clarification": clar,
        "deterministic_local_operations": det,
        "DeepSeek_calls_total": ds_total,
        "DeepSeek_call_rate": round(ds_total / max(len(results), 1), 3),
        "local_LM_calls": lm_calls,
        "local_LM_call_rate": round(lm_calls / max(len(results), 1), 3),
        "local_LM_fallback": lm_fb,
        "p50_latency_ms": percentile(walls, 50),
        "p95_latency_ms": percentile(walls, 95),
        "TOOL_THRASH": thrash,
        "plan_execution_mismatch": mismatch,
        "registry_menu_corruption_count": reg_corr + menu_corr,
        "safety_failures": safety_fail,
        "success_rate": round(passed / max(len(results), 1), 3),
        "failure_rate": round(len(failed) / max(len(results), 1), 3),
        "by_category": {},
    }
    for r in results:
        c = r.get("category") or "?"
        summary["by_category"].setdefault(c, {"n": 0, "passed": 0, "failed": 0})
        summary["by_category"][c]["n"] += 1
        if r.get("passed"):
            summary["by_category"][c]["passed"] += 1
        else:
            summary["by_category"][c]["failed"] += 1

    perf = {
        "requests": [
            {
                "generation_id": r.get("generation_id"),
                "prompt_category": r.get("prompt_category"),
                "intent": r.get("intent"),
                "operation": r.get("operation"),
                "builder_plan_ms": r.get("builder_plan_ms"),
                "local_lm_called": r.get("local_lm_called"),
                "local_lm_ms": r.get("local_lm_ms"),
                "context_plan_ms": r.get("context_plan_ms"),
                "context_files": r.get("context_files"),
                "message_count": r.get("message_count"),
                "total_input_bytes": r.get("total_input_bytes"),
                "TTFT": r.get("ttft_ms"),
                "DeepSeek_calls": r.get("DeepSeek_calls"),
                "tool_calls": r.get("tool_calls"),
                "repair_attempts": r.get("repair_attempts"),
                "total_elapsed_ms": r.get("total_elapsed_ms"),
                "final_status": r.get("final_status"),
            }
            for r in results
        ],
        "aggregates": {
            "success_rate": summary["success_rate"],
            "failure_rate": summary["failure_rate"],
            "clarification_rate": round(clar / max(len(results), 1), 3),
            "deterministic_operation_rate": round(det / max(len(results), 1), 3),
            "local_LM_call_rate": summary["local_LM_call_rate"],
            "local_LM_fallback_rate": round(lm_fb / max(len(results), 1), 3),
            "DeepSeek_call_rate": summary["DeepSeek_call_rate"],
            "p50_latency_ms": summary["p50_latency_ms"],
            "p95_latency_ms": summary["p95_latency_ms"],
            "TOOL_THRASH_count": thrash,
            "plan_execution_mismatch_count": mismatch,
        },
    }

    (OUT / "prompt_matrix_results.json").write_text(json.dumps({"results": results}, indent=2))
    (OUT / "prompt_matrix_summary.json").write_text(json.dumps(summary, indent=2))
    (OUT / "prompt_matrix_failures.json").write_text(
        json.dumps({"failures": failed}, indent=2)
    )
    (OUT / "performance_matrix.json").write_text(json.dumps(perf, indent=2))
    print("\n=== SUMMARY ===", flush=True)
    print(json.dumps(summary, indent=2), flush=True)
    print(f"Wrote reports under {OUT}", flush=True)
    return 0 if len(failed) == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
