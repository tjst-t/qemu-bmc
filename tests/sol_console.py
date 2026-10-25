#!/usr/bin/env python3
"""sol_console.py - drives an interactive console command (ipmitool sol
activate) on a pseudo terminal, like expect.

Usage: sol_console.py [--timeout S] [--log FILE] STEP... -- COMMAND [ARG...]

Steps, run in order:
  expect:REGEX  wait for REGEX (Python re, MULTILINE) in the output since the
                last match; CR is removed before matching
  send:TEXT     type TEXT (backslash escapes such as \\r are interpreted)
  sleep:S       wait S seconds (output is still read)
  run:CMD       run shell command CMD (it must succeed)
  eof           wait for COMMAND to exit
Exit status: 0 if every step succeeded, 1 otherwise (the output is printed).
"""

import os
import pty
import re
import select
import signal
import subprocess
import sys
import time


def main():
    args = sys.argv[1:]
    timeout, log = 30.0, None
    while args and args[0].startswith("--") and args[0] != "--":
        if args[0] == "--timeout":
            timeout = float(args[1])
            args = args[2:]
        elif args[0] == "--log":
            log = args[1]
            args = args[2:]
        else:
            sys.exit("unknown option " + args[0])
    if "--" not in args:
        sys.exit(__doc__)
    sep = args.index("--")
    steps, command = args[:sep], args[sep + 1:]

    pid, fd = pty.fork()
    if pid == 0:
        os.execvp(command[0], command)

    output = bytearray()  # everything
    pending = ""  # text since the last match
    exited = False

    def read(until):
        """Read output until the deadline or until the command exits."""
        nonlocal pending, exited
        while not exited:
            left = until - time.time()
            if left <= 0:
                return
            r, _, _ = select.select([fd], [], [], left)
            if not r:
                return
            try:
                data = os.read(fd, 4096)
            except OSError:
                data = b""
            if not data:
                exited = True
                return
            output.extend(data)
            pending += data.decode("utf-8", "replace").replace("\r", "")
            if log:
                with open(log, "ab") as f:
                    f.write(data)
            return

    def fail(msg):
        print("sol_console: FAIL: %s" % msg)
        print("----- output -----")
        print(output.decode("utf-8", "replace").replace("\r", ""))
        print("------------------")
        try:
            os.kill(pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        sys.exit(1)

    for step in steps:
        kind, _, arg = step.partition(":")
        if kind == "expect":
            rx = re.compile(arg, re.MULTILINE)
            deadline = time.time() + timeout
            while True:
                m = rx.search(pending)
                if m:
                    pending = pending[m.end():]
                    break
                if exited or time.time() >= deadline:
                    fail("timed out waiting for /%s/" % arg)
                read(deadline)
        elif kind == "send":
            os.write(fd, arg.encode().decode("unicode_escape").encode("latin-1"))
        elif kind == "sleep":
            deadline = time.time() + float(arg)
            while time.time() < deadline and not exited:
                read(deadline)
        elif kind == "run":
            r = subprocess.run(arg, shell=True, capture_output=True, text=True)
            if r.returncode != 0:
                fail("%r failed (%d): %s%s" % (arg, r.returncode, r.stdout, r.stderr))
        elif kind == "eof":
            deadline = time.time() + timeout
            while not exited and time.time() < deadline:
                read(deadline)
            if not exited:
                fail("command did not exit")
        else:
            fail("unknown step %r" % step)

    if not exited:
        os.kill(pid, signal.SIGTERM)
    os.waitpid(pid, 0)
    print(output.decode("utf-8", "replace").replace("\r", ""))


if __name__ == "__main__":
    main()
