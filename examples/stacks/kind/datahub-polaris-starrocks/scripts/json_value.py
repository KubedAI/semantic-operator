# /// script
# requires-python = ">=3.10"
# dependencies = []
# ///
"""Read a nested JSON field from stdin using positional object keys."""

import json
import sys


value = json.load(sys.stdin)
for key in sys.argv[1:]:
    if not isinstance(value, dict):
        value = ""
        break
    value = value.get(key, "")
if value is None:
    value = ""
if isinstance(value, (dict, list)):
    print(json.dumps(value, separators=(",", ":")))
else:
    print(value)
