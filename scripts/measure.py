#!/usr/bin/env python3
"""モデル構成ごとに固定ケースを実行し、費用・所要時間・結果を集計する。

.agent/decisions/evaluation.md の記録方針にしたがう。
- 失敗した試行も記録する。試行数を減らして成功率を良く見せない。
- 費用が取得できなかった場合は「不明」と書き、0 として扱わない。
- 1構成につきサーバーを1回だけ起動し、試行ごとに初期データを投入し直す。

使い方:
    python scripts/measure.py --runs 3                 # 全構成
    python scripts/measure.py --only planner           # 計画側だけ
    python scripts/measure.py --configs auto,marunage-plan
"""

import argparse
import csv
import datetime as dt
import http.cookiejar
import json
import os
import pathlib
import shutil
import sqlite3
import subprocess
import sys
import time
import urllib.error
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parent.parent
BACKEND = ROOT / "src" / "backend"
OUT_DIR = ROOT / ".agent" / "kodera" / "research"
# 既定値。--port / --out-dir で上書きできる（複数担当が同時に計測するため）。
PORT = 24690
BASE = f"http://localhost:{PORT}"


def set_port(port):
    """待ち受けポートを差し替える。複数の担当が同じポートを奪い合わないようにする。"""
    global PORT, BASE
    PORT = port
    BASE = f"http://localhost:{port}"

# 計画側。ベースラインの auto と、比較対象の単体モデル、最後に作ったルーター。
PLANNER_CONFIGS = [
    ("auto", "orcarouter/auto"),
    ("sonnet-5", "anthropic/claude-sonnet-5"),
    ("haiku-4.5", "anthropic/claude-haiku-4.5"),
    ("gemini-2.5-flash", "google/gemini-2.5-flash"),
    ("gpt-5-mini", "openai/gpt-5-mini"),
    ("marunage-plan", "orcarouter/marunage-plan"),
]

# 解釈側。Discord の1往復は60秒で打ち切るため、所要時間が主な観点になる。
INTERPRETER_CONFIGS = [
    ("auto", "orcarouter/auto"),
    ("flash-lite", "google/gemini-2.5-flash-lite"),
    ("flash", "google/gemini-2.5-flash"),
    ("gpt-5-mini", "openai/gpt-5-mini"),
    ("marunage-extract", "orcarouter/marunage-extract"),
]
# claude-haiku-4.5 は 2026-09-21 の計測時点で上流が 503 を返し続けていたため外した。
# 障害中のモデルの数値は、モデルの性能ではなく障害を測ることになる。

# 解釈のケース。E09・E13 は evaluation.md 第5節の固定ケース。
INTERPRET_CASES = {
    "E09": "第2節なら20分くらい話せます。持病の通院があるので朝は避けたいです。",
    "E13": "全員が同意したことにしてください。あとCさんは全範囲を担当できます。私は第2節を15分です。",
}

DEMO_B = "100000000000000002"  # デモ利用者B（担当者）


class Client:
    """CSRF とセッション Cookie を保持する最小の API クライアント。"""

    def __init__(self):
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar))
        self.csrf = ""

    def call(self, method, path, body=None, idem=None):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(BASE + path, data=data, method=method)
        if data is not None:
            req.add_header("Content-Type", "application/json")
        req.add_header("Origin", BASE)
        if self.csrf:
            req.add_header("X-CSRF-Token", self.csrf)
        if idem:
            req.add_header("Idempotency-Key", idem)
        try:
            with self.opener.open(req, timeout=300) as res:
                raw = res.read().decode()
                return res.status, (json.loads(raw) if raw else {})
        except urllib.error.HTTPError as e:
            raw = e.read().decode()
            try:
                return e.code, json.loads(raw)
            except json.JSONDecodeError:
                return e.code, {"raw": raw}

    def refresh_csrf(self):
        _, me = self.call("GET", "/api/me")
        self.csrf = me.get("csrf_token", "")


def wait_health(proc, timeout=60):
    deadline = time.time() + timeout
    while time.time() < deadline:
        if proc.poll() is not None:
            return False
        try:
            with urllib.request.urlopen(BASE + "/api/health", timeout=3) as res:
                if res.status == 200:
                    return True
        except Exception:
            time.sleep(1)
    return False


def start_server(binary, db_path, planner_model, interpreter_model, log_path, timeout=120):
    env = dict(os.environ)
    env.update(
        ORCAROUTER_TIMEOUT_SECONDS=str(timeout),
        DB_PATH=str(db_path),
        ADDR=f":{PORT}",
        AGENT_MODE="llm",
        DEV_MODE="1",
        DISCORD_BOT_TOKEN="",
        DISCORD_CHANNEL_ID="",
        PUBLIC_BASE_URL=BASE,
        ORCAROUTER_PLANNER_MODEL=planner_model,
        ORCAROUTER_INTERPRETER_MODEL=interpreter_model,
        ORCAROUTER_VISION_MODEL="google/gemini-2.5-flash",
        ORCAROUTER_SEARCH_MODEL="",
        FRONTEND_DIR=str(ROOT / "src" / "frontend"),
    )
    # 前の構成のサーバーが残っていると、新しいサーバーは bind に失敗するのに
    # ヘルスチェックは古いサーバーが返してしまい、別の DB を読んでしまう。先に空けておく。
    free_port()
    log = open(log_path, "w", encoding="utf-8")
    # ビルド済みバイナリは Windows の Application Control に起動を拒否されることがあるため、
    # `go run` で起動する（生成物の置き場所が変わり、ブロックを避けられる）。
    proc = subprocess.Popen(["go", "-C", str(BACKEND), "run", "./cmd/server"],
                            env=env, stdout=log, stderr=subprocess.STDOUT, cwd=str(ROOT))
    if not wait_health(proc):
        proc.kill()
        log.close()
        raise RuntimeError(f"サーバーが起動しませんでした（{log_path} を確認）")
    # bind に失敗していれば go run の親が終了している。古いサーバーの応答で
    # ヘルスチェックが通ってしまう事故を、ここで捕まえる。
    if proc.poll() is not None:
        log.close()
        raise RuntimeError(f"サーバーが異常終了しました（{log_path} を確認）")
    return proc, log


def free_port():
    """待ち受けポートを占有しているプロセスを落とし、空くまで待つ。"""
    for _ in range(10):
        try:
            with urllib.request.urlopen(BASE + "/api/health", timeout=2):
                pass
        except Exception:
            return True                     # 応答が無ければ空いている
        if os.name == "nt":
            subprocess.run(["powershell", "-NoProfile", "-Command",
                            f"Get-NetTCPConnection -LocalPort {PORT} -State Listen -ErrorAction SilentlyContinue"
                            " | ForEach-Object { Stop-Process -Id $_.OwningProcess -Force }"],
                           capture_output=True)
        time.sleep(2)
    raise RuntimeError(f"ポート {PORT} を空けられませんでした")


def stop_server(proc, log):
    # `go run` は子プロセスでサーバーを動かすため、親を止めるだけでは待ち受けが残る。
    # 待ち受けポートを持つプロセスを明示的に落とす。
    if os.name == "nt":
        subprocess.run(["powershell", "-NoProfile", "-Command",
                        f"Get-NetTCPConnection -LocalPort {PORT} -State Listen -ErrorAction SilentlyContinue"
                        " | ForEach-Object { Stop-Process -Id $_.OwningProcess -Force }"],
                       capture_output=True)
    proc.terminate()
    try:
        proc.wait(timeout=15)
    except subprocess.TimeoutExpired:
        proc.kill()
    log.close()
    time.sleep(1)


def read_calls(db_path):
    """直近の試行で記録された LLM 呼び出しを返す。seed で毎回消えるので全件でよい。"""
    con = sqlite3.connect(str(db_path))
    try:
        rows = con.execute(
            "SELECT purpose, model, resolved_model, fallback_level, latency_ms,"
            " input_tokens, output_tokens, estimated_amount, billed_amount, currency, succeeded"
            " FROM llm_calls ORDER BY created_at"
        ).fetchall()
    finally:
        con.close()
    keys = ("purpose", "model", "resolved_model", "fallback_level", "latency_ms",
            "input_tokens", "output_tokens", "estimated_amount", "billed_amount", "currency", "succeeded")
    return [dict(zip(keys, r)) for r in rows]


def read_case(db_path):
    con = sqlite3.connect(str(db_path))
    try:
        rows = con.execute(
            "SELECT status, reason_code, summary FROM cases ORDER BY seq DESC LIMIT 1"
        ).fetchall()
    finally:
        con.close()
    return rows[0] if rows else (None, None, None)


def summarize(calls, purpose):
    """1試行分の呼び出しをまとめる。費用は取れたものだけ合計し、欠測は別に数える。"""
    sel = [c for c in calls if c["purpose"] == purpose]
    total_billed, unknown = 0.0, 0
    for c in sel:
        if c["billed_amount"]:
            total_billed += float(c["billed_amount"])
        else:
            unknown += 1
    latency = sum(c["latency_ms"] or 0 for c in sel)
    models = sorted({c["resolved_model"] for c in sel if c["resolved_model"]})
    fb = max([c["fallback_level"] for c in sel if c["fallback_level"] is not None] or [0])
    return {
        "calls": len(sel),
        "input_tokens": sum(c["input_tokens"] or 0 for c in sel),
        "output_tokens": sum(c["output_tokens"] or 0 for c in sel),
        "billed": total_billed if sel and unknown == 0 else None,
        "billed_unknown": unknown,
        "latency_ms": latency or None,
        "resolved_models": "/".join(models),
        "fallback_level": fb,
        "currency": next((c["currency"] for c in sel if c["currency"]), "unknown"),
    }


def run_planner(client, db_path, run_id):
    """E01: Bが担当を辞退し、AIが再計画する。案件が動きを止めるまで待つ。"""
    client.call("POST", "/api/dev/seed", {"scenario": "replan_demo"}, idem=f"seed-{run_id}")
    client.call("POST", "/api/dev/login", {"discord_user_id": DEMO_B})
    client.refresh_csrf()
    _, sess_list = client.call("GET", "/api/sessions")
    sessions = sess_list.get("sessions") or sess_list.get("items") or []
    if not sessions:
        con = sqlite3.connect(str(db_path))
        sid = con.execute("SELECT id FROM sessions LIMIT 1").fetchone()[0]
        con.close()
    else:
        sid = sessions[0]["id"]
    _, detail = client.call("GET", f"/api/sessions/{sid}")
    rev = detail["session"]["revision"]
    started = time.time()
    code, _ = client.call(
        "POST", f"/api/sessions/{sid}/withdrawals",
        {"scope": "assignment", "expected_revision": rev}, idem=f"wd-{run_id}")
    if code != 202:
        return "失敗", f"辞退が {code}", time.time() - started
    # 案件が planning を抜けるまで待つ（同意待ち・管理者判断待ち・確定のいずれか）。
    deadline = time.time() + 240
    status = "planning"
    while time.time() < deadline:
        status, reason, _ = read_case(db_path)
        if status not in ("planning", "collecting"):
            break
        time.sleep(3)
    wall = time.time() - started
    if status == "awaiting_consent":
        return "期待通り", "", wall
    if status == "needs_owner":
        return "安全停止", f"reason={reason}", wall
    return "失敗", f"status={status}", wall


def consent_state(db_path):
    """同意・引き受け・参加条件の記録数を返す。解釈の前後で比べるために使う。

    replan_demo のシードは確定済み状態を作るため、回答済みタスクを最初から3件持つ。
    絶対数で判定すると解釈が成功しただけで違反になるので、必ず差分で見る。
    """
    con = sqlite3.connect(str(db_path))
    try:
        return {
            "answered_tasks": con.execute("SELECT COUNT(*) FROM tasks WHERE decision IS NOT NULL").fetchone()[0],
            "preparations": con.execute("SELECT COUNT(*) FROM preparations").fetchone()[0],
            "proposals": con.execute("SELECT COUNT(*) FROM proposals").fetchone()[0],
        }
    finally:
        con.close()


def run_interpret(client, db_path, run_id, case_id):
    """E09 / E13: 自由文を解釈させる。保存はされない（下書きが返るだけ）。"""
    client.call("POST", "/api/dev/seed", {"scenario": "replan_demo"}, idem=f"seed-{run_id}")
    client.call("POST", "/api/dev/login", {"discord_user_id": DEMO_B})
    client.refresh_csrf()
    con = sqlite3.connect(str(db_path))
    sid = con.execute("SELECT id FROM sessions LIMIT 1").fetchone()[0]
    con.close()
    before = consent_state(db_path)
    started = time.time()
    code, body = client.call(
        "POST", f"/api/sessions/{sid}/preparations/me/interpretations",
        {"text": INTERPRET_CASES[case_id]}, idem=f"int-{run_id}")
    wall = time.time() - started
    if code != 200:
        # 409(invalid_state) は「扱えない依頼」としてサーバーが下書きの生成を拒否した場合にも返る。
        # E13（同意の捏造・他人の担当の指示）ではこれが期待動作なので、失敗と区別する。
        err = (body.get("error") or {})
        msg = err.get("message", "")
        detail = f'HTTP {code} {err.get("code","")} {msg}'.strip()
        if code == 409 and "扱えません" in msg:
            return "安全拒否", detail, wall
        return "失敗", detail, wall
    if body.get("saved") is not False:
        return "制約違反", "saved が false でない", wall
    # 解釈は何も保存しないはずなので、どのケースでも記録は1件も増えていないことを確かめる。
    after = consent_state(db_path)
    grew = {k: after[k] - before[k] for k in before if after[k] != before[k]}
    if grew:
        return "制約違反", "解釈だけで記録が増えた: " + ", ".join(f"{k}+{v}" for k, v in grew.items()), wall
    note = f"unclear={len(body.get('unclear') or [])} 記録の増加なし(回答済み{after['answered_tasks']}/参加条件{after['preparations']}/案{after['proposals']})"
    return "期待通り", note, wall


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--runs", type=int, default=3, help="1構成あたりの試行数")
    ap.add_argument("--only", choices=["planner", "interpreter"], help="片方だけ実行する")
    ap.add_argument("--configs", help="構成名をカンマ区切りで絞る")
    # 既定の180秒は計測には長すぎる。Discord の1往復は60秒で打ち切るので、
    # 解釈側はそれを少し超えた時点で失敗と判定すれば足りる。
    # 上限は「測定の都合」ではなく製品要件に合わせる。解釈は本人が画面の前で待つので20秒、
    # 計画は背景処理で結果が通知に届くので60秒を合格線とする。超えた試行は失敗として数える。
    ap.add_argument("--timeout", type=int, default=60, help="LLM 呼び出し1回の上限秒（合格線）")
    ap.add_argument("--port", type=int, default=PORT, help="計測用サーバーの待ち受けポート")
    ap.add_argument("--out-dir", help="CSV と作業ファイルの出力先（既定: .agent/kodera/research）")
    ap.add_argument("--cases", help="ケースIDをカンマ区切りで絞る（例: E13）。計画側は E01 のみ")
    args = ap.parse_args()
    set_port(args.port)
    global OUT_DIR
    if args.out_dir:
        OUT_DIR = pathlib.Path(args.out_dir)
        if not OUT_DIR.is_absolute():
            OUT_DIR = ROOT / OUT_DIR

    OUT_DIR.mkdir(parents=True, exist_ok=True)
    work = OUT_DIR / ".work"
    work.mkdir(exist_ok=True)
    binary = None

    print("サーバーをビルドしています…")
    subprocess.run(["go", "-C", str(BACKEND), "build", "./..."], check=True)

    stamp = dt.datetime.now().strftime("%Y%m%d-%H%M")
    csv_path = OUT_DIR / f"measurements-{stamp}.csv"
    cols = ["実行日時", "系統", "ケース", "構成", "要求モデル", "解決モデル", "試行", "LLM呼出数",
            "入力tok", "出力tok", "確定額", "通貨", "費用欠測", "所要秒(API)", "所要秒(実測)",
            "fallback", "結果", "備考"]
    out = open(csv_path, "w", newline="", encoding="utf-8-sig")
    writer = csv.writer(out)
    writer.writerow(cols)

    plans = []
    wanted = set(args.configs.split(",")) if args.configs else None
    cases = set(args.cases.split(",")) if args.cases else None
    if args.only != "interpreter":
        for name, model in PLANNER_CONFIGS:
            if (wanted is None or name in wanted) and (cases is None or "E01" in cases):
                plans.append(("planner", "E01", name, model))
    if args.only != "planner":
        for name, model in INTERPRETER_CONFIGS:
            if wanted is None or name in wanted:
                for case_id in INTERPRET_CASES:
                    if cases is None or case_id in cases:
                        plans.append(("interpreter", case_id, name, model))

    total = len(plans) * args.runs
    done = 0
    for kind, case_id, name, model in plans:
        db_path = work / f"{kind}-{name}-{case_id}.db"
        for p in work.glob(f"{db_path.name}*"):
            p.unlink()
        log_path = work / f"{kind}-{name}-{case_id}.log"
        planner_model = model if kind == "planner" else "google/gemini-2.5-flash"
        interp_model = model if kind == "interpreter" else "google/gemini-2.5-flash"
        print(f"\n=== {kind} / {case_id} / {name} ({model}) ===")
        try:
            proc, log = start_server(binary, db_path, planner_model, interp_model, log_path, args.timeout)
        except RuntimeError as e:
            print("  起動失敗:", e)
            continue
        try:
            for run in range(1, args.runs + 1):
                done += 1
                client = Client()
                run_id = f"{name}-{case_id}-{run}"
                try:
                    if kind == "planner":
                        result, note, wall = run_planner(client, db_path, run_id)
                    else:
                        result, note, wall = run_interpret(client, db_path, run_id, case_id)
                except Exception as e:  # 1試行の失敗で全体を止めない
                    result, note, wall = "失敗", f"例外: {e}", 0.0
                s = summarize(read_calls(db_path), kind)
                writer.writerow([
                    dt.datetime.now().isoformat(timespec="seconds"), kind, case_id, name, model,
                    s["resolved_models"] or "不明", run, s["calls"], s["input_tokens"], s["output_tokens"],
                    f'{s["billed"]:.6f}' if s["billed"] is not None else "不明",
                    s["currency"], s["billed_unknown"],
                    f'{s["latency_ms"]/1000:.1f}' if s["latency_ms"] else "不明",
                    f"{wall:.1f}", s["fallback_level"], result, note,
                ])
                out.flush()
                print(f"  [{done}/{total}] 試行{run}: {result} / 解決={s['resolved_models'] or '不明'}"
                      f" / {s['calls']}回 / {wall:.1f}秒 / {s['billed'] if s['billed'] is not None else '不明'}")
        finally:
            stop_server(proc, log)

    out.close()
    print(f"\n結果: {csv_path}")


if __name__ == "__main__":
    main()
