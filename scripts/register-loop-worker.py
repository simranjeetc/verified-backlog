#!/usr/bin/env python3
"""Start and register one owned worker under the session's launch lock."""
import fcntl
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile

root = Path(os.environ["BL_LOOP_ROOT"])
token = os.environ["BL_LOOP_TOKEN"]
session = Path(os.environ["BL_LOOP_SESSION"]).resolve()


def stopping():
    return (root / ".backlog/loop.cancel").exists() or (session / "workers.stopping").exists()


def register(job):
    workers = session / "workers"
    workers.mkdir(exist_ok=True)
    fd, tmp = tempfile.mkstemp(prefix=".receipt-", dir=workers)
    try:
        with os.fdopen(fd, "w") as out:
            json.dump({"job": job, "cwd": os.getcwd(), "token": token}, out)
            out.flush()
            os.fsync(out.fileno())
        os.replace(tmp, workers / (job + ".json"))
    finally:
        if os.path.exists(tmp):
            os.unlink(tmp)


# The dispatch start process inherits the descriptor, so killing this helper cannot release
# the barrier while start is still pending. Shutdown drains jobs and waits for
# the lock before considering cleanup complete.
binary, role, prompt, output, seconds, title, chain = sys.argv[1:]
job = None
try:
    with (session / "workers.launch.lock").open("a") as launch_lock:
        fcntl.flock(launch_lock, fcntl.LOCK_EX)
        assoc = json.loads((root / ".backlog/loop.json").read_text())
        if assoc["token"] != token or str(session) not in [str(Path(d).resolve()) for d in assoc.get("dirs", [])]:
            raise RuntimeError("loop ownership changed")
        if stopping():
            raise RuntimeError("loop cancellation requested")
        command = [binary, "start", role, prompt, output, "--timeout", seconds, "--title", title]
        if chain:
            command.extend(["--chain", chain])
        result = subprocess.run(command, stdout=subprocess.PIPE, text=True, check=True, pass_fds=(launch_lock.fileno(),))
        job = result.stdout.strip()
        if not re.fullmatch(r"[A-Za-z0-9_-]+", job):
            raise RuntimeError("invalid dispatch job id; tagged job requires loop cleanup")
        register(job)
        # Release the shared lock explicitly after the receipt is durable. If this
        # helper is killed earlier, dispatch start retains the inherited lock.
        fcntl.flock(launch_lock, fcntl.LOCK_UN)
    if stopping():
        raise RuntimeError("loop cancellation requested after worker start")
    print(job)
except Exception as error:
    if job and re.fullmatch(r"[A-Za-z0-9_-]+", job):
        try:
            subprocess.run([binary, "cancel", job], check=True, timeout=20)
        except Exception as cancel_error:
            print(f"worker {job}: cancellation unconfirmed: {cancel_error}; tagged job requires loop cleanup", file=sys.stderr)
    print(f"worker launch failed: {error}", file=sys.stderr)
    raise SystemExit(1)
