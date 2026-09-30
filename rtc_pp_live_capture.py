#!/usr/bin/env python3
"""Capture PP motion logs and read-only EtherCAT snapshots during stalls.

Run against the jamun service journal, or a file receiving jamun stdout.
No EtherCAT writes, resets, or motor commands are issued.
"""

import argparse
import csv
import datetime as dt
import queue
import re
import signal
import subprocess
import sys
import threading
import time
from pathlib import Path


TRANSITION = re.compile(
    r"\[PP-TRANSITION\].*?sw=(0x[0-9a-fA-F]+).*?bit10=(true|false)"
    r".*?goal=(-?\d+).*?actual=(-?\d+).*?diff=(\d+)"
)
SETTLED = re.compile(r"\[PDO-PP SETTLED\].*?goal=(-?\d+).*?actual=(-?\d+).*?residualPulses=(\d+)")
GOAL = re.compile(r"\[PDO\] Starting rotation in PP mode.*?goal=(-?\d+)")
LOG_TIME = re.compile(r'time="(\d{4}-\d\d-\d\d \d\d:\d\d:\d\d\.\d+)"')
FIELDS = [
    ("sw", "uint16", "0x6041"),
    ("mode", "int8", "0x6061"),
    ("demand", "int32", "0x6062"),
    ("actual", "int32", "0x6064"),
    ("target", "int32", "0x607A"),
    ("following_error", "int32", "0x60F4"),
    ("window", "uint32", "0x6067"),
    ("window_time", "uint16", "0x6068"),
    ("profile_velocity", "uint32", "0x6081"),
    ("profile_accel", "uint32", "0x6083"),
    ("profile_decel", "uint32", "0x6084"),
]


def timestamp():
    return dt.datetime.now().astimezone().isoformat(timespec="milliseconds")


def read_lines(proc, out):
    try:
        for line in proc.stdout:
            out.put(line.rstrip("\n"))
    finally:
        out.put(None)


def snapshot(slave, ethercat, reason, goal, csv_writer, csv_file, events):
    values = {}
    for name, data_type, index in FIELDS:
        try:
            result = subprocess.run(
                [ethercat, "upload", "-p", str(slave), "-t", data_type, index, "0"],
                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
                timeout=2, check=False,
            )
            if result.returncode:
                values[name] = "ERR:" + result.stderr.strip()[:100]
            else:
                match = re.search(r"(-?\d+)\s*$", result.stdout.strip())
                values[name] = int(match.group(1)) if match else "ERR:" + result.stdout.strip()[:100]
        except (OSError, subprocess.TimeoutExpired) as exc:
            values[name] = "ERR:" + str(exc)[:100]
    sw = values.get("sw")
    target = values.get("target")
    actual = values.get("actual")
    window = values.get("window")
    residual = abs(target - actual) if isinstance(target, int) and isinstance(actual, int) else ""
    result = {
        "time": timestamp(), "reason": reason, "log_goal": goal,
        **values, "residual": residual,
        "bit10": bool(sw & 0x0400) if isinstance(sw, int) else "",
        "within_window": residual <= window if isinstance(residual, int) and isinstance(window, int) else "",
    }
    csv_writer.writerow(result)
    csv_file.flush()
    events.write(f"{result['time']} SNAPSHOT {reason} goal={goal} sw={sw} "
                 f"demand={values.get('demand')} target={target} actual={actual} "
                 f"follow={values.get('following_error')} residual={residual} "
                 f"window={window} mode={values.get('mode')} "
                 f"vel={values.get('profile_velocity')} accel={values.get('profile_accel')} "
                 f"decel={values.get('profile_decel')}\n")
    events.flush()
    print(f"SNAPSHOT {reason}: sw={sw} demand={values.get('demand')} "
          f"target={target} actual={actual} residual={residual} window={window}", flush=True)


def run(args):
    outdir = Path(args.outdir or f"rtc_pp_capture_{dt.datetime.now().strftime('%Y%m%d_%H%M%S')}")
    outdir.mkdir(parents=True, exist_ok=False)
    print(f"Capture directory: {outdir.resolve()}", flush=True)
    if args.replay:
        source = ["tail", "-n", "+1", args.replay]
    elif args.log_file:
        source = ["tail", "-n", "0", "-F", args.log_file]
    else:
        source = ["journalctl", "-u", args.unit, "-f", "-n", "0", "-o", "cat"]
    proc = subprocess.Popen(source, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            text=True, bufsize=1)
    stop = False

    def handle_signal(_signum, _frame):
        nonlocal stop
        stop = True

    signal.signal(signal.SIGINT, handle_signal)
    signal.signal(signal.SIGTERM, handle_signal)
    lines = queue.Queue()
    threading.Thread(target=read_lines, args=(proc, lines), daemon=True).start()
    with (outdir / "jamun.log").open("w", buffering=1) as raw, \
         (outdir / "events.log").open("w", buffering=1) as events, \
         (outdir / "sdo.csv").open("w", newline="") as csv_file:
        columns = ["time", "reason", "log_goal"] + [name for name, _, _ in FIELDS] + ["residual", "bit10", "within_window"]
        writer = csv.DictWriter(csv_file, fieldnames=columns)
        writer.writeheader()
        last_goal = None
        last_actual = None
        stationary_since = None
        last_stall_sample = 0.0
        sampled_near_reach = False
        active = False
        while not stop:
            try:
                line = lines.get(timeout=0.25)
            except queue.Empty:
                line = ""
            now = time.monotonic()
            if line is None:
                break
            if line:
                raw.write(line + "\n")
                if args.replay:
                    match_time = LOG_TIME.search(line)
                    if match_time:
                        now = dt.datetime.fromisoformat(match_time.group(1)).timestamp()
                started = GOAL.search(line)
                if started:
                    last_goal = int(started.group(1))
                    last_actual = None
                    stationary_since = None
                    last_stall_sample = 0.0
                    sampled_near_reach = False
                    active = True
                    events.write(f"{timestamp()} START goal={last_goal}\n")
                transition = TRANSITION.search(line)
                if transition and active:
                    sw, bit10, goal_s, actual_s, diff_s = transition.groups()
                    goal, actual, diff = int(goal_s), int(actual_s), int(diff_s)
                    if goal != last_goal:
                        last_goal = goal
                        stationary_since = None
                    if bit10 == "false" and diff <= args.near_counts:
                        if last_actual is None or abs(actual - last_actual) > args.stable_counts:
                            stationary_since = now
                        last_actual = actual
                        if stationary_since is not None and now - stationary_since >= args.stall_seconds:
                            if now - last_stall_sample >= args.sample_seconds:
                                last_stall_sample = now
                                events.write(f"{timestamp()} STALL sw={sw} goal={goal} actual={actual} residual={diff}\n")
                                print(f"STALL goal={goal} actual={actual} residual={diff} sw={sw}", flush=True)
                                if not args.replay:
                                    snapshot(args.slave, args.ethercat, "stall", goal, writer, csv_file, events)
                    else:
                        stationary_since = None
                        last_actual = actual
                settled = SETTLED.search(line)
                if settled and active:
                    goal, actual, residual = map(int, settled.groups())
                    events.write(f"{timestamp()} SETTLED goal={goal} actual={actual} residual={residual}\n")
                    if residual >= args.capture_reached_counts and not sampled_near_reach and not args.replay:
                        sampled_near_reach = True
                        snapshot(args.slave, args.ethercat, "settled", goal, writer, csv_file, events)
                    active = False
                if ("level=error" in line and
                        ("[PDO-PP] move failed" in line or "timeout waiting for Target Reached" in line)):
                    events.write(f"{timestamp()} ERROR {line}\n")
                    print("ERROR: " + line, flush=True)
                    if active and not args.replay:
                        snapshot(args.slave, args.ethercat, "error", last_goal, writer, csv_file, events)
                    active = False
            if proc.poll() is not None and lines.empty():
                break
        if proc.poll() is None:
            proc.terminate()
        try:
            proc.wait(timeout=2)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()
    err = proc.stderr.read().strip()
    if err:
        print("Log source: " + err, file=sys.stderr)
    print(f"Saved {outdir.resolve()}/ (jamun.log, events.log, sdo.csv)")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    source = parser.add_mutually_exclusive_group()
    source.add_argument("--log-file", help="File receiving live jamun stdout (tail new lines)")
    source.add_argument("--replay", help="Analyze an existing log without SDO reads")
    parser.add_argument("--unit", default="jamun.service")
    parser.add_argument("--slave", type=int, default=0)
    parser.add_argument("--ethercat", default="/opt/etherlab/bin/ethercat")
    parser.add_argument("--outdir")
    parser.add_argument("--near-counts", type=int, default=1000)
    parser.add_argument("--stable-counts", type=int, default=2)
    parser.add_argument("--stall-seconds", type=float, default=1.5)
    parser.add_argument("--sample-seconds", type=float, default=2.0)
    parser.add_argument("--capture-reached-counts", type=int, default=15)
    run(parser.parse_args())
