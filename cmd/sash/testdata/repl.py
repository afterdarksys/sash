"""Exercise the actual binary through a terminal, including batched input."""
import os
import pty
import select
import subprocess
import sys
import termios
import time

master, slave = pty.openpty()
original = termios.tcgetattr(slave)
process = subprocess.Popen(sys.argv[1:], stdin=slave, stdout=slave, stderr=slave)
transcript = bytearray()


def expect(needle):
    deadline = time.monotonic() + 3
    while needle not in transcript:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise AssertionError(f"missing {needle!r}: {bytes(transcript)!r}")
        if select.select([master], [], [], remaining)[0]:
            transcript.extend(os.read(master, 65536))


try:
    expect(b"sash> ")
    os.write(master, b"echo first\r")
    expect(b"\r\nfirst\r\n")
    transcript.clear()
    # Up recalls a command; Ctrl-C cancels a partial line without exiting.
    os.write(master, b"\x1b[A\r")
    expect(b"\r\nfirst\r\n")
    transcript.clear()
    os.write(master, b"echo cancelled\x03echo second\recho third\r\x04")
    expect(b"\r\nsecond\r\n")
    expect(b"\r\nthird\r\n")
    assert b"\r\ncancelled\r\n" not in transcript
    assert process.wait(timeout=3) == 0
    restored = termios.tcgetattr(slave)
    # Darwin sets the transient PENDIN flag when canonical mode is restored.
    pending = getattr(termios, "PENDIN", 0)
    restored[3] &= ~pending
    original[3] &= ~pending
    assert restored == original, "terminal mode was not restored"
finally:
    if process.poll() is None:
        process.kill()
    process.wait()
    os.close(master)
    os.close(slave)
