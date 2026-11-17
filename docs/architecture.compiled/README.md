# Compiled architecture

`docs/architecture.md` is long (over 8,000 lines). This directory is its compiled form: a short spine, one sheet per
component, a manifest and a machine-readable contract list, so a reader or an agent can load only the part it needs.
It follows the architecture's v1.0 "as built" reading: what the code does first, then *Target — not built* items with
their gap ids (D-nn) from the architecture's "Known gaps", and ADRs where a decision replaced the design.

| File | Kind | What it holds |
|---|---|---|
| `manifest.json` | generated | Source path, sha256 and line count of `docs/architecture.md`; the compiler; the component index (name, slug, status, keywords, aliases, dependencies) and the directories each component owns |
| `contracts.json` | generated | Every operation in the OpenAPI specs (method, path, operationId, `x-depot-duty`), every topic published and subscribed in the AsyncAPI specs, a topic index, and the 59 pattern contracts with their as-built verdicts |
| `spine.md` | hand-written | Inventory, stack, naming, cross-cutting rules and open questions in one page |
| `components/<slug>.md` | hand-written | One sheet per component: purpose, owned services, interfaces, data, events, dependencies, constraints, target items, source sections |

The specs (`services/*/api/openapi.yaml`, `services/*/events/asyncapi.yaml`, and the same under `tools/*/`) are the
source of truth for endpoints and topics (ADR-0008); `contracts.json` is generated from them, never edited by hand.

## Rebuild

```
python3 scripts/compile_architecture.py --write     # after editing docs/architecture.md, a spec, or the inputs below
python3 scripts/compile_architecture.py --check     # what CI runs
python3 -m unittest discover -s scripts -p 'test_compile_architecture.py'
```

`--write` keeps the manifest's `created` date unless you pass `--created YYYY-MM-DD`. It needs Python 3 and PyYAML.

Inputs you edit by hand:

- `scripts/architecture_components.json`: the components, their keywords and dependencies, and the directories each
  owns. Every directory under `services/` and `apps/` must be owned by exactly one component. When you add a service,
  add it here and cite it in its component sheet.
- `scripts/architecture_patterns.json`: the pattern contracts and their as-built verdicts. Each `source` must name a
  heading in a file of this directory.
- The prose: `spine.md` and `components/*.md`. The script never writes them.

## What CI checks

The `docs-tools` job in `.github/workflows/ci.yml` runs `--check` and the unit test. The gate runs it when
`docs/architecture.md`, anything in this directory, a spec, a service or app directory, or the script and its inputs
change. `--check` fails, with one `DRIFT` line per problem, when:

- the manifest's sha256 or line count does not match `docs/architecture.md` (an edit to the architecture without a
  recompile);
- `manifest.json` or `contracts.json` differs from a fresh generation (a spec changed, or someone edited the JSON);
- a directory under `services/` or `apps/` is owned by no component or by more than one, or a component lists a
  directory that does not exist;
- a component sheet is missing, does not start with `# <name>`, lacks one of the standard headings, or does not cite a
  directory its component owns; or a sheet exists for no component;
- a pattern's `source` heading does not exist;
- a repo path cited in backticks in any `.md` file here does not exist. A backticked token counts as a path when its
  first segment is a top-level directory of the repository; placeholders (`<`, `{`, `|`, `...`) are skipped, globs
  must match. Write a path that is designed but absent in plain text, not in backticks.

A small review edit to `docs/architecture.md` therefore needs only `--write` and a commit of the new manifest. A
change that moves a boundary needs the component sheet updated too.
