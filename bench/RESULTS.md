# Benchmark results

Not yet generated in this checkout. Run the benchmark against a deployed
stack (see docs/RUNBOOK.md sections 6-9 for prerequisites):

```bash
make bench
```

The runner executes all 30 questions (3 phrasings each) through both paths
and overwrites this file with the summary table (accuracy, paraphrase
consistency, hallucination rate), per-question verdicts, and notable
raw-path failures. Results are reproducible for a given Bedrock model id:
the demo data seed is fixed and inference runs at temperature 0.
