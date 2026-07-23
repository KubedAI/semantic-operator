# /// script
# requires-python = ">=3.10"
# dependencies = [
#   "acryl-datahub==1.6.0",
# ]
# ///
"""Run the shared DataHub enrichment engine via uv with inline (PEP 723) deps.

No uv project and no fork of the engine: this thin wrapper puts the shared
datahub/enrichment directory on sys.path, imports enrich.py unchanged, and hands
it the CLI args (the local run supplies --platform iceberg + the local metadata).
The acryl-datahub version is pinned to match the deployed GMS (v1.6.0).
"""
import pathlib
import sys

# local/scripts/ -> local/ -> datahub-customer-health/  then datahub/enrichment
ENRICH_DIR = pathlib.Path(__file__).resolve().parents[2] / "datahub" / "enrichment"
if not (ENRICH_DIR / "enrich.py").exists():
    raise SystemExit(f"shared enrich.py not found at {ENRICH_DIR}")
sys.path.insert(0, str(ENRICH_DIR))

import enrich  # noqa: E402

if __name__ == "__main__":
    raise SystemExit(enrich.main(sys.argv[1:]))
