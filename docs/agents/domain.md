# Domain Docs

How the engineering skills should consume this repo's domain documentation when exploring the codebase.

## Layout: single-context

This repo is **single-context**. There is one glossary and one ADR directory, both at the
repo root. There is no `GLOSSARY-MAP.md`.

```
/
├── GLOSSARY.md
├── docs/adr/
│   ├── 0001-....md
│   └── 0002-....md
├── backend/
├── frontend/
└── docs/
```

## Before exploring, read these

- **`GLOSSARY.md`** at the repo root
- **`docs/adr/`**: read ADRs that touch the area you're about to work in

If either of these doesn't exist, **proceed silently**. Don't flag the absence; don't
suggest creating them upfront. The `/domain-modeling` skill (reached via `/grill-with-docs`
and `/improve-codebase-architecture`) creates them lazily when terms or decisions actually
get resolved.

Note: this repo keeps its own engineering records under `docs/` — `architecture-boundaries.md`,
`business-contracts.md`, `operations.md`, and the roadmaps. Those are maintained by hand and
are *not* a substitute for `GLOSSARY.md` or `docs/adr/`, which stay reserved for the
skills' domain-modeling workflow.

## Use the glossary's vocabulary

When your output names a domain concept (in an issue title, a refactor proposal, a hypothesis, a test name), use the term as defined in `GLOSSARY.md`. Don't drift to synonyms the glossary explicitly avoids.

If the concept you need isn't in the glossary yet, that's a signal: either you're inventing language the project doesn't use (reconsider) or there's a real gap (note it for `/domain-modeling`).

## Flag ADR conflicts

If your output contradicts an existing ADR, surface it explicitly rather than silently overriding:

> _Contradicts ADR-0007 (event-sourced orders), but worth reopening because…_
