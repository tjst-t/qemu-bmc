"""sushy client for test_update.sh: UpdateService simple_update and the
firmware inventory.

Usage: update_sushy.py URL USER PASS COMMAND [ARG...]
  inventory                     {"BMC": version, "BIOS": version}
  simple_update IMAGE [TARGET]  runs SimpleUpdate (TARGET: an inventory URI),
                                waits for the task, prints its state, status
                                and messages
Prints JSON; errors as {"error": <HTTP status or message>, "message": ...}.
"""
import json
import sys
import time

import sushy
import urllib3

urllib3.disable_warnings()

url, user, password, cmd, *args = sys.argv[1:]
root = sushy.Sushy(url, username=user, password=password, verify=False)
updates = root.get_update_service()

try:
    if cmd == "inventory":
        out = {}
        for item in updates.firmware_inventory.get_members():
            out[item.identity] = item.version
    elif cmd == "simple_update":
        targets = [args[1]] if len(args) > 1 else None
        monitor = updates.simple_update(args[0], targets=targets)
        # sushy polls the Task the 202 body names; wait on its state
        deadline = time.time() + 60
        while True:
            task = root.get_task_monitor(monitor.task_monitor_uri).get_task()
            if task.task_state not in (sushy.TASK_STATE_NEW, sushy.TASK_STATE_RUNNING,
                                       sushy.TASK_STATE_STARTING, sushy.TASK_STATE_PENDING):
                break
            if time.time() > deadline:
                raise RuntimeError("task did not finish")
            time.sleep(1)
        out = {
            "state": getattr(task.task_state, "value", task.task_state),
            "status": getattr(task.task_status, "value", task.task_status),
            "percent": task.percent_complete,
            "messages": [m.message_id for m in task.messages],
        }
    else:
        raise SystemExit("unknown command %s" % cmd)
except sushy.exceptions.HTTPError as e:
    out = {"error": e.status_code, "message": str(e)}
print(json.dumps(out))
