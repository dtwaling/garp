# AGENTS.md

## Skills

- Performance and benchmark validation: `garp-perf-benchmarking` (repo-local project
  skill at `.agents/skills/garp-perf-benchmarking/`). Use it when a garp card carries a
  before/after perf cap (Stage 1 discovery, prefilters, cleaner readers), or when two
  agents report conflicting percentages for the same change.

## Orchestrators

The validation skill is a project skill, so it only loads for sessions rooted in this
repo. A card that starts from the ThinkTank root will not load it. Pin it explicitly on
any card that needs it -- `skills: ["garp-perf-benchmarking"]` -- and err on the side of
pinning it even when it would have loaded anyway.
