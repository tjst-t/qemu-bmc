#!/usr/bin/env python3
"""sushy client used by tests/test_bios.sh.

Usage: bios_sushy.py URL USER PASS COMMAND [ARG]

Commands (each prints one JSON document):
  attributes        system.bios.attributes
  pending           system.bios.pending_attributes
  apply_times       system.bios.supported_apply_times
  set JSON          system.bios.set_attributes(JSON, apply_time=OnReset);
                    {"ok": true} or {"error": <HTTP status>, "message": ...}
  reset             system.bios.reset_bios()
  registry          system.bios.get_attribute_registry(): its Id and
                    {name: {type, read_only, values, default}}
"""
import json
import sys
import warnings

import sushy

warnings.filterwarnings("ignore")


def main():
    url, user, password, command, *args = sys.argv[1:]
    root = sushy.Sushy(url, username=user, password=password, verify=False)
    bios = root.get_system("/redfish/v1/Systems/1").bios

    if command == "attributes":
        out = bios.attributes
    elif command == "pending":
        out = bios.pending_attributes
    elif command == "apply_times":
        out = [str(t.value if hasattr(t, "value") else t) for t in bios.supported_apply_times]
    elif command == "set":
        try:
            bios.set_attributes(json.loads(args[0]), apply_time=sushy.APPLY_TIME_ON_RESET)
            out = {"ok": True}
        except sushy.exceptions.HTTPError as e:
            out = {"error": e.status_code, "message": str(e)}
    elif command == "reset":
        bios.reset_bios()
        out = {"ok": True}
    elif command == "registry":
        reg = bios.get_attribute_registry()
        if reg is None:
            out = None
        else:
            out = {
                "id": reg.identity,
                "attributes": {
                    a.name: {
                        "type": a.attribute_type,
                        "read_only": a.read_only,
                        "values": [v.get("ValueName") for v in (a.allowable_values or [])],
                        "default": a.default_value,
                    }
                    for a in reg.registry_entries.attributes
                },
            }
    else:
        sys.exit("unknown command: " + command)
    print(json.dumps(out, sort_keys=True))


if __name__ == "__main__":
    main()
