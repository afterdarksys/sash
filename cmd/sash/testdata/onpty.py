"""Run a command on a pseudo terminal and report what happened, as JSON.

The scenario is one JSON object on argv[1]:
  argv     the command (argv[0] is the program to run)
  argv0    what the command sees as its argv[0], when it differs
  stdin    "pty" (default) or "pipe"; a pipe is fed "input" and closed
  stdout   "pty" (default) or "pipe"
  stderr   "pty" (default) or "pipe"
  env      the whole environment
  cwd      the working directory
  steps    a list of {"expect": text}, {"send": text}, {"signal": name},
           {"sleep": seconds}, {"eof": true}; expect waits for text on the terminal
  wait     seconds to wait for the exit (default 5)
The report: {"code", "tty", "stdout", "stderr", "restored", "error"}; the
captured text is latin-1, so every byte survives.
"""
import json
import os
import pty
import select
import signal
import subprocess
import sys
import termios
import time

sc = json.loads(sys.argv[1])
master, slave = pty.openpty()
original = termios.tcgetattr(slave)
transcript = bytearray()
report = {"error": ""}


def end(name):
    return slave if sc.get(name, "pty") == "pty" else subprocess.PIPE


process = subprocess.Popen(
    sc["argv"],
    executable=sc["argv"][0],
    stdin=end("stdin"),
    stdout=end("stdout"),
    stderr=end("stderr"),
    env=sc.get("env"),
    cwd=sc.get("cwd"),
    start_new_session=True,
) if "argv0" not in sc else subprocess.Popen(
    [sc["argv0"]] + sc["argv"][1:],
    executable=sc["argv"][0],
    stdin=end("stdin"),
    stdout=end("stdout"),
    stderr=end("stderr"),
    env=sc.get("env"),
    cwd=sc.get("cwd"),
    start_new_session=True,
)
piped = {"stdout": bytearray(), "stderr": bytearray()}


def pump(timeout):
    fds = [master]
    for name in ("stdout", "stderr"):
        f = getattr(process, name)
        if f is not None and not f.closed:
            fds.append(f.fileno())
    ready = select.select(fds, [], [], timeout)[0]
    for fd in ready:
        try:
            data = os.read(fd, 65536)
        except OSError:
            data = b""
        if fd == master:
            transcript.extend(data)
            continue
        for name in ("stdout", "stderr"):
            f = getattr(process, name)
            if f is not None and not f.closed and f.fileno() == fd:
                if data:
                    piped[name].extend(data)
                else:
                    f.close()
    return bool(ready)


def expect(needle):
    deadline = time.monotonic() + 5
    while needle not in transcript:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise AssertionError("missing %r in %r" % (needle, bytes(transcript)))
        pump(remaining)


try:
    if sc.get("stdin") == "pipe":
        process.stdin.write(sc.get("input", "").encode("latin-1"))
        process.stdin.close()
    for step in sc.get("steps", []):
        if "expect" in step:
            expect(step["expect"].encode("latin-1"))
        elif "send" in step:
            os.write(master, step["send"].encode("latin-1"))
        elif "signal" in step:
            os.kill(process.pid, getattr(signal, "SIG" + step["signal"]))
        elif "sleep" in step:
            end_at = time.monotonic() + step["sleep"]
            while time.monotonic() < end_at:
                pump(max(0, end_at - time.monotonic()))
    deadline = time.monotonic() + sc.get("wait", 5)
    while process.poll() is None and time.monotonic() < deadline:
        pump(0.05)
    if process.poll() is None:
        raise AssertionError("still running: %r" % bytes(transcript))
    while pump(0.05):
        pass
    report["code"] = process.returncode
    restored = termios.tcgetattr(slave)
    # Darwin sets the transient PENDIN flag when canonical mode is restored.
    pending = getattr(termios, "PENDIN", 0)
    restored[3] &= ~pending
    original[3] &= ~pending
    report["restored"] = restored == original
except AssertionError as e:
    report["error"] = str(e)
finally:
    if process.poll() is None:
        process.kill()
    process.wait()
    os.close(master)
    os.close(slave)
report["tty"] = bytes(transcript).decode("latin-1")
report["stdout"] = bytes(piped["stdout"]).decode("latin-1")
report["stderr"] = bytes(piped["stderr"]).decode("latin-1")
print(json.dumps(report))
