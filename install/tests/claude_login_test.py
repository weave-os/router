"""Exercise Claude's interactive login against subscription_cli_test.sh's fixtures."""

import json
import os
import pty
import re
import select
import signal
import sys
import time
from pathlib import Path


def interactive_login(installer, base_url, env):
    pid, fd = pty.fork()
    if pid == 0:
        os.execvpe("bash", ["bash", installer, "login", "claude", "--base-url",
                           base_url, "--quiet"], env)

    output = bytearray()
    sent_code = False
    deadline = time.monotonic() + 30
    try:
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise AssertionError("Claude login timed out")
            ready, _, _ = select.select([fd], [], [], remaining)
            if not ready:
                continue
            try:
                chunk = os.read(fd, 4096)
            except OSError:
                break
            if not chunk:
                break
            output.extend(chunk)
            if not sent_code and b"Paste the Claude authorization code:" in output:
                state = re.search(rb"[?&]state=([A-Za-z0-9_-]+)", output)
                assert state, "authorization URL omitted state"
                os.write(fd, b"authorization-code#" + state.group(1) + b"\n")
                sent_code = True
        _, status = os.waitpid(pid, 0)
        pid = None
    finally:
        os.close(fd)
        if pid is not None:
            os.kill(pid, signal.SIGKILL)
            os.waitpid(pid, 0)
    return os.waitstatus_to_exitcode(status), bytes(output)


def login(response, succeeds):
    enrollments = Path(os.environ["FAKE_ENROLLMENTS"])
    previous = enrollments.read_text().splitlines()
    status, output = interactive_login(
        sys.argv[1], "https://router.example.test",
        dict(os.environ, FAKE_CLAUDE_RESPONSE=json.dumps(response)))

    for field in ("refresh_token", "access_token"):
        assert response[field].encode() not in output, "credential leaked into terminal output"
    assert b"Install scope:" not in output, "login asked for install scope"
    current = enrollments.read_text().splitlines()
    if not succeeds:
        assert status != 0, "missing identity must fail closed"
        assert (b"no valid account UUID" in output or
                b"no valid organization UUID" in output), "missing identity needs an actionable error"
        assert current == previous, "invalid identity must not enroll an account"
        return None
    assert status == 0, "Claude login failed"
    assert b"Claude subscription enrolled." in output, "Claude login did not finish"
    assert len(current) == len(previous) + 1, "login must enroll exactly once"
    return json.loads(current[-1])


if len(sys.argv) == 4 and sys.argv[2] == "--enroll":
    status, output = interactive_login(sys.argv[1], sys.argv[3], os.environ)
    assert b"refresh-fixture-" not in output and b"access-fixture-" not in output, "credential leaked"
    assert status == 0 and b"Claude subscription enrolled." in output, "Claude enrollment failed"
    sys.exit(0)

account_a = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
account_b = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
organization_a = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
organization_b = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
response = {
    "access_token": "access-secret",
    "refresh_token": "refresh-first",
    "account": {"uuid": account_a.upper(), "email_address": "first@example.test"},
    "organization": {"uuid": organization_a, "name": "First Org"},
}
first = login(response, True)
response["refresh_token"] = "refresh-second"
response["account"] = {"uuid": account_a, "email_address": "changed@example.test"}
second = login(response, True)
assert first["external_account_id"] == second["external_account_id"] == account_a + ":" + organization_a
assert first["provider"] == second["provider"] == "claude"
assert first["display_name"] == "First Org: first@example.test"
assert second["display_name"] == "First Org: changed@example.test"
assert second["refresh_token"] == "refresh-second", "reconnect must send the latest credential"
response["organization"]["uuid"] = organization_b
response["organization"]["name"] = "Second Org"
third = login(response, True)
assert third["external_account_id"] == account_a + ":" + organization_b, "different organizations must remain distinct"
response["account"]["uuid"] = account_b
fourth = login(response, True)
assert fourth["external_account_id"] == account_b + ":" + organization_b, "different accounts must remain distinct"

for identity in (None, {}, {"uuid": None}, {"uuid": ""}, {"uuid": 123},
                 {"uuid": "refresh-secret"}, {"uuid": "   "}):
    response["account"] = identity
    login(response, False)
del response["account"]
login(response, False)
response["account"] = {"uuid": account_a}
for identity in (None, {}, {"uuid": None}, {"uuid": ""}, {"uuid": 123},
                 {"uuid": "refresh-secret"}, {"uuid": "   "}):
    response["organization"] = identity
    login(response, False)
