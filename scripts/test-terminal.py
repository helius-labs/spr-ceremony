#!/usr/bin/env python3
"""Verify hidden input and terminal recovery with an actual PTY/native binary."""
import os
import pty
import select
import signal
import sys
import termios
import time

binary = os.path.abspath(sys.argv[1])
for cancel in ['none', 'ctrl-c', 'sigterm']:
    pid, master = pty.fork()
    if pid == 0:
        os.execv(binary, [binary, 'join'])
    output = b''
    secret = b'private-invitation-invalid-for-test'
    try:
        deadline = time.monotonic() + 10
        while b'(hidden):' not in output:
            if time.monotonic() > deadline: raise RuntimeError('No hidden prompt')
            if select.select([master], [], [], .1)[0]: output += os.read(master, 4096)
        # ReadPassword disables echo just after writing the prompt.
        while termios.tcgetattr(master)[3] & termios.ECHO:
            if time.monotonic() > deadline: raise RuntimeError('Echo was not disabled')
            time.sleep(.01)
        if cancel == 'sigterm': os.kill(pid, signal.SIGTERM)
        else: os.write(master, b'\x03' if cancel == 'ctrl-c' else secret + b'\n')
        while time.monotonic() < deadline:
            if select.select([master], [], [], .1)[0]:
                try: chunk = os.read(master, 4096)
                except OSError: break
                if not chunk: break
                output += chunk
        else: raise RuntimeError('Hidden prompt did not exit')
        _, status = os.waitpid(pid, 0)
        assert os.waitstatus_to_exitcode(status) != 0
        assert secret not in output, 'Invitation echoed'
        assert termios.tcgetattr(master)[3] & termios.ECHO, 'Terminal echo not restored'
    finally:
        os.close(master)
        try: os.kill(pid, signal.SIGKILL)
        except ProcessLookupError: pass
print('Hidden invitation input, Ctrl-C and SIGTERM recovery passed.')
