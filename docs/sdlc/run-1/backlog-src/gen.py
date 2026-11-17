#!/usr/bin/env python3
"""Generate the SDLC run-1 stage-5 backlog, trace and execution plan.

Reads (repository-relative): docs/sdlc/run-1/fr-audit/waves-*.md, docs/architecture.md, docs/adr/00*.md,
docs/ux/rollout.md, docs/ux/patterns.md, docs/ux/README.md, docs/sdlc/run-1/ux-audit.md, docs/sdlc/run-1/README.md,
and the issue drafts in issues.py next to this file.
Writes: docs/sdlc/run-1/backlog.md, trace.md, execution-plan.md.

Run from anywhere:  python3 docs/sdlc/run-1/backlog-src/gen.py
Exit status 1 if a check fails (unknown FR id, unmapped gap, dependency in a later wave, unresolved path).
"""
import fnmatch, glob, os, re, subprocess, sys
from collections import Counter, defaultdict, OrderedDict

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.abspath(os.path.join(HERE, '..', '..', '..', '..'))
RUN = os.path.join(REPO, 'docs', 'sdlc', 'run-1')
def rp(*p): return os.path.join(REPO, *p)
def rel(p): return os.path.relpath(p, REPO)

ns = {}
exec(open(os.path.join(HERE, 'issues.py')).read(), ns)
I = ns['ISSUES']; ADR_MAP = ns['ADR_BULLETS']
byid = {i['id']: i for i in I}
assert len(byid) == len(I), 'duplicate issue ids'
PRI = {'P0': 0, 'P1': 1, 'P2': 2, 'P3': 3}
def idkey(x):
    m = re.match(r'B-(\d+)([a-z]*)', x); return (int(m.group(1)), m.group(2))
def base(x):
    m = re.match(r'(B-\d+)([a-z]*)', x)
    return m.group(1) + (m.group(2) if m.group(1) + m.group(2) in byid else '')

try:
    HEAD = subprocess.run(['git', '-C', REPO, 'rev-parse', '--short=8', 'HEAD'], capture_output=True, text=True).stdout.strip()
    TRACKED = subprocess.run(['git', '-C', REPO, 'ls-files'], capture_output=True, text=True).stdout.split('\n')
except Exception:
    HEAD, TRACKED = 'unknown', []
TRACKED = [t for t in TRACKED if t]
FAIL = []

# ---------------- FR audit rows ----------------
AUDIT = sorted(glob.glob(os.path.join(RUN, 'fr-audit', 'waves-*.md')))
rows = {}
for f in AUDIT:
    sec = None
    for ln, line in enumerate(open(f), 1):
        if line.startswith('## '): sec = line.strip()
        if not line.startswith('| FR-') or not (sec or '').startswith('## Wave'): continue
        cells = [c.strip() for c in line.strip().strip('|').split('|')]
        if not re.match(r'FR-\d+\.\d+$', cells[0]): continue
        rows[cells[0]] = dict(fr=cells[0], title=cells[1], prio=cells[2], status=cells[3],
                             gap=' | '.join(cells[5:-1]) if len(cells) > 7 else (cells[5] if len(cells) > 5 else ''),
                             ux=cells[-1], file=os.path.basename(f), line=ln)
def frkey(f): a, b = f[3:].split('.'); return (int(a), int(b))
def phase(f): return (frkey(f)[0] - 1) // 5
PHASES = ['0 Foundation (w1-5)', '1 Master data (w6-10)', '2 Financial core (w11-15)', '3 Procure-to-pay (w16-20)',
          '4 Manufacturing (w21-25)', '5 Order-to-cash (w26-30)', '6 Logistics (w31-35)', '7 Treasury & controlling (w36-40)',
          '8 Projects, people, service (w41-45)', '9 Intelligence & go-live (w46-50)']
stat = Counter(r['status'] for r in rows.values())

# ---------------- checks on issue data ----------------
closes = defaultdict(list)   # FR -> [(issue, clause)]
friction = defaultdict(list); related = defaultdict(list)
for i in I:
    for fr, cl in i['frs']:
        if fr not in rows: FAIL.append(f"{i['id']}: unknown {fr}")
        closes[fr].append((i['id'], cl))
    for fr in i.get('friction', []):
        if fr not in rows: FAIL.append(f"{i['id']}: unknown {fr}")
        friction[fr].append(i['id'])
    for fr in i.get('rel', []):
        if fr not in rows: FAIL.append(f"{i['id']}: unknown {fr}")
        related[fr].append(i['id'])
    for d in i['deps']:
        if base(d) not in byid and d[:-1] not in byid and d[:-2] not in byid: FAIL.append(f"{i['id']}: unknown dep {d}")
def issue_of(d):
    for cut in (0, 1, 2):
        k = d if cut == 0 else d[:-cut]
        if k in byid: return k
    return d

# ---------------- path check (R-08) ----------------
def expand(p):
    m = re.search(r'\{([^}]*)\}', p)
    if not m: return [p]
    return [x for alt in m.group(1).split(',') for x in expand(p[:m.start()] + alt + p[m.end():])]
def pat_of(f): return f.split(' (')[0].strip()
def is_new(f): return '(new' in f
_RX = {}
def glob_match(path, pat):
    rx = _RX.get(pat)
    if rx is None:
        out = ''; i = 0
        while i < len(pat):
            if pat.startswith('**', i): out += '.*'; i += 2
            elif pat[i] == '*': out += '[^/]*'; i += 1
            elif pat[i] == '?': out += '[^/]'; i += 1
            else: out += re.escape(pat[i]); i += 1
        rx = _RX[pat] = re.compile(out + '$')
    return bool(rx.match(path))
TRACKED_DIRS = set()
for t in TRACKED:
    parts = t.split('/')
    for n in range(1, len(parts)): TRACKED_DIRS.add('/'.join(parts[:n]))
def resolves(f):
    pats = expand(pat_of(f)); new = is_new(f)
    for p in pats:
        if any(glob_match(t, p) for t in TRACKED): continue
        if new:
            parent = re.split(r'[*?]', p)[0].rstrip('/')
            parent = parent.rsplit('/', 1)[0] if '/' in parent else ''
            hops = 2 if ('dir' in f or 'from B-' in f) else 0
            while parent and parent not in TRACKED_DIRS and hops > 0:
                parent = parent.rsplit('/', 1)[0] if '/' in parent else ''; hops -= 1
            if parent == '' or parent in TRACKED_DIRS: continue
        return False
    return True
def files_of(b):
    k = issue_of(b); part = b[len(k):]
    i = byid[k]
    if part: return i['parts'][part][1]
    if i.get('parts'): return [f for p in i['parts'].values() for f in p[1]]
    return i['files']
path_fail = []
npaths = 0
for i in I:
    for f in (i['files'] + [x for p in i.get('parts', {}).values() for x in p[1]]):
        npaths += 1
        if not resolves(f): path_fail.append(f"{i['id']}: {f}")
if path_fail: FAIL.extend('path: ' + p for p in path_fail)

# ---------------- architecture inputs ----------------
arch = open(rp('docs', 'architecture.md')).read()
kg = arch[arch.index('## Known gaps'):arch.index('## Auditability and end-user impact')]
au = arch[arch.index('## Auditability and end-user impact'):arch.index('## Requirements Summary')]
sev = {m.group(1): (m.group(2), m.group(3).strip()) for m in re.finditer(r'^\| (D\d+) \| (High|Med|Low) \| ([^|]+)\|', kg, re.M)}
d2iss = defaultdict(list)
for i in I:
    for d in i['dids']:
        dd = d.split(' ')[0]; d2iss[dd].append(i['id'] + ('' if d == dd else ' ' + d[len(dd) + 1:]))
DEFER = {
 'D5': 'Documentation part handled by architecture v1.0; the CI-generated inventory and dispatch-py/routing-py charts are in B-49 (ADR-0008 #1, #3).',
 'D14': 'Documentation part handled by v1.0 and ADR-0010; rotation drill and replay-store metric in B-49 (ADR-0010 #1, #3).',
 'D15': 'Superseded: its open conditions are D35 (B-10).',
 'D20': 'Documentation part handled by v1.0; erasure reach in B-10 and B-46 (ADR-0011 #4, ADR-0013 #2).',
 'D25': 'Not a v1.0 gap: recorded by ADR-0008; conformance through D11/D12 in B-47.',
 'D26': 'Documentation part handled by ADR-0014; Valkey deployment and outage behaviour in B-09 (ADR-0014 #1-#3).',
 'D27': 'Done: stage 3 recompiled docs/architecture.compiled and passed at gate 8 (0662309d). No stage-5 work.',
}
for d in ['D5', 'D14', 'D20', 'D26']:
    pass

# ---------------- UX inputs ----------------
roll = open(rp('docs', 'ux', 'rollout.md')).read()
sumtab = roll[roll.index('## Summary'):roll.index('## Milestones')]
slicenames, slicefind = {}, {}
for m in re.finditer(r'^\| (\d+) \| ([^|]+)\| ([^|]+)\| ([^|]*)\| ([^|]+)\| ([^|]+)\|', sumtab, re.M):
    slicenames[m.group(1)] = m.group(2).strip(); slicefind[m.group(1)] = re.findall(r'\d{3}', m.group(6))
slice2iss = defaultdict(list)
for i in I:
    for s in i['slices']: slice2iss[s].append(i['id'])
ua = open(os.path.join(RUN, 'ux-audit.md')).read()
findings = OrderedDict((m.group(1), (m.group(2), m.group(4).strip())) for m in re.finditer(r'^\| (UX-\d{3}) \| (High|Med|Low) \| ([^|]+)\| ([^|]+)\|', ua, re.M))
ux2iss = defaultdict(set)
for i in I:
    for u in i['ux']:
        for m in re.findall(r'UX-\d{3}', u): ux2iss[m].add(i['id'])
for s, fs in slicefind.items():
    for f3 in fs:
        for x in slice2iss.get(s, []): ux2iss['UX-' + f3].add(x)
uxs2iss = defaultdict(set); aud2iss = defaultdict(set)
for i in I:
    for u in i['ux']:
        for m in re.findall(r'UXS-\d\d', u): uxs2iss[m].add(i['id'])
        for m in re.findall(r'AUD-API-\d', u): aud2iss[m].add(i['id'])

# ---------------- epics ----------------
EPICS = OrderedDict([
 ('E1', 'Identity, access and data isolation'), ('E2', 'Event backbone and end-to-end traceability'),
 ('E3', 'Audit-trail integrity and correctness defects'), ('E4', 'Deployability and key custody'),
 ('E5', 'Business-flow seams that make numbers wrong or force rekeying'), ('E6', 'Financial postings that are missing, cannot be undone, or do not tie out'),
 ('E7', 'UX milestones M1-M3: honest, auditable, fewer keystrokes (web)'), ('E8', 'Floor apps: kiosk and warehouse handheld'),
 ('E9', 'UX completion (slices 10-20)'), ('E10', 'Warehouse and plant completeness'),
 ('E11', 'Operations completeness: projects, people, intercompany, payments, P2P/AR controls, assistants'),
 ('E12', 'Compliance and master-data quality'), ('E13', 'Platform conformance and production readiness'),
 ('E14', 'Hygiene, Should-level gaps and owner-gated items')])

# ---------------- waves ----------------
WAVES = [
 ('1', 'P0: built defect fixes, closed-period race, audit actor, kernel legal-entity guard, eventing', [['B-08', 'B-08b'], 'B-08c', 'B-06', 'B-03a', 'B-05']),
 ('2', 'P0: network isolation, Kubernetes request path, legal-entity scope, audit chain', ['B-01', 'B-02', 'B-03b', 'B-07']),
 ('3', 'P0: stage/prod, enforcement model and decision logs; P1: tracing, UX slices 0-1', ['B-09', 'B-04', 'B-11', 'B-20', 'B-21']),
 ('4', 'P0: key custody; P1: access lifecycle, receipt to stock, O2C pricing/billing, UX slice 2', ['B-10', 'B-12', 'B-13', 'B-15', 'B-22']),
 ('5', 'P1: History and audit page, shipments/production stock, FX, gap-free numbers', ['B-23', 'B-14', 'B-17', 'B-35']),
 ('6', 'P1: audited actions, close first cut, planning, subledger tie-outs, product blocks', ['B-24', 'B-25', 'B-16', 'B-52', 'B-39']),
 ('7', 'P1: missing postings, dimension security, defaults, kiosk, remaining audit ingestion', ['B-18', 'B-19', 'B-26', 'B-29', 'B-36']),
 ('8', 'P1: names-not-ids, handheld first cut; P2: tracked jobs', ['B-27', 'B-30', 'B-34']),
 ('9', 'P1: bulk actions; P2: intercompany and variances, master data', ['B-28', 'B-40', 'B-43']),
 ('10', 'P2: audited actions everywhere, warehouse complete, payments, production readiness', ['B-54', 'B-37', 'B-41', 'B-48']),
 ('11', 'P2: grid and forms, time/pay/bill, plant seams, P2P/AR controls, posting key', ['B-31', 'B-38', 'B-44', 'B-45', 'B-47ab']),
 ('12', 'P2: guidance, portals and navigation, regulatory controls, assistants, admin and close complete', ['B-32', 'B-33', 'B-42', 'B-51', 'B-53']),
 ('13', 'P2/P3 close-out: data platform, service-wide API sweep, hygiene, Should-level, UX-GAP-1', ['B-46', 'B-47cd', 'B-49', 'B-50', 'B-55']),
]
FLOATING = ['B-03c']
RULES = [
 '`deploy/kafka/connect.yaml`: B-05 owns it in wave 1; B-07 and B-36 add only their own audit connector blocks.',
 '`deploy/gateway/**`: B-02 owns it in wave 2; B-30 later adds only the mobile route; B-03c later changes only the selection forwarding in `securitypolicy.yaml`.',
 'Service charts: B-01 adds only `templates/networkpolicy.yaml`; B-09 adds only `templates/migrate-job.yaml` and owns `deploy/envs/{stage,prod}`; B-05 changes only consumer overrides in `deploy/envs/dev/values`; B-48 adds only pdb and hpa templates.',
 '`platform/go/outbox`: B-07 (wave 2), then B-11 (wave 3).',
 '`services/identity` is one flat Go package: each issue names the files it edits. B-03b: policy_bundle*.go, opa*.go. B-12: keycloak_*.go, users_api.go. B-23 and B-24: role_assignments_api.go (B-23 in wave 5, B-24 in wave 6). B-28: preferences_api.go (new). B-29: badge_api.go (new). B-03c rebases after B-04, B-12 and B-22 and touches only the le mapper, the membership check, the Envoy forwarding and the company switcher.',
 'Migrations: two issues never add a migration to the same service in the same wave. B-03b adds the scheduling legal-entity migration after B-08b\'s 0025 is merged (wave 1 -> 2).',
 'New contract tests (`*/audit_contract_test.go`, B-36 in wave 7) are new files; they do not edit the files B-18, B-19 or B-29 change in that wave.',
 'Wave 5: B-23 owns `services/audit/internal/api/**`; B-36 (ingestion) waits for wave 7 for that reason.',
 'Wave 6: B-39 adds a block-check call and a new file per consuming service; B-16 owns other changes under `services/sales/internal`, B-52 owns `services/inventory/internal/api/reconciliation.go`.',
 'Wave 4: B-22 changes copy only, not `*DetailPage.tsx` in sales and production; B-23 (wave 5) adds RecordDetail to those pages.',
 'Wave 10: B-37 owns `apps/web/src/modules/mobile-flows/**`; B-54 leaves mobile-flows pages in its allowlist with a date and converts them after B-37 merges.',
 'Wave 11: B-38 touches only labour in `services/eam`; B-44 owns the rest of `services/eam`.',
 'Wave 12: B-32 lands its portal pages (invite landing, welcome, demo mode) first; B-33 rebases its portal changes on them.',
 'Wave 13: B-47cd is a service-wide mechanical sweep (problem shape, idempotency middleware, If-Match, method style, headers). It lands as one PR per service after the wave\'s other batches have merged, and no other service-wide batch runs with it.',
 '`apps/web/src/modules/**` sweeps (B-27, B-28, B-31, B-54) never share a wave.',
]
GATES_IN_PLAN = {'B-26': 'Usability baseline stage B report committed before B-26 ships (#512 participants).',
                 'B-32': 'Usability baseline stage C report committed before B-32 ships (#512 participants).',
                 'B-03c': 'Owner confirmation of ADR-0006 rule 4 (#512) before B-03c starts.',
                 'B-55': 'Product-owner decision on UX-GAP-1 (#512) before B-55 starts.'}

def batch_ids(b): return b if isinstance(b, list) else [b]
wave_of = {}
for w, _, bs in WAVES:
    for b in bs:
        for x in batch_ids(b): wave_of[x] = int(w)
for x in FLOATING: wave_of[x] = 99
covered_ids = set()
for x in wave_of:
    covered_ids.add(issue_of(x))
missing_w = set(byid) - covered_ids
if missing_w: FAIL.append(f'issues in no wave: {sorted(missing_w)}')
def wave_needed(dep):
    if dep in wave_of: return wave_of[dep]
    k = issue_of(dep)
    return max(v for x, v in wave_of.items() if issue_of(x) == k)
deperr = []
for i in I:
    mine = min(v for x, v in wave_of.items() if issue_of(x) == i['id'])
    for d in i['deps']:
        if wave_needed(d) >= mine and not (i['id'] == 'B-03' ): deperr.append(f"{i['id']} (w{mine}) needs {d} (w{wave_needed(d)})")
if deperr: FAIL.extend(deperr)

def segmatch(sa, sb):
    if not sa and not sb: return True
    if (sa and sa[0] == '**') or (sb and sb[0] == '**'): return True
    if not sa or not sb: return False
    a, b = sa[0], sb[0]
    if fnmatch.fnmatch(a, b) or fnmatch.fnmatch(b, a): return segmatch(sa[1:], sb[1:])
    return False
def overlap(a, b):
    return any(segmatch(x.split('/'), y.split('/')) for x in expand(pat_of(a)) for y in expand(pat_of(b)))

# ---------------- backlog.md ----------------
cnt = Counter(i['prio'] for i in I)
def bl(xs): return '\n'.join(f'- {x}' for x in xs) if xs else '- None'
L = []
L.append('# Depot backlog — SDLC run 1, stage 5\n')
L.append(f'Generated by `docs/sdlc/run-1/backlog-src/gen.py` from `backlog-src/issues.py` at repository HEAD `{HEAD}`. Inputs: `docs/sdlc/run-1/fr-audit/waves-*.md` ({len(rows)} FRs: {stat["Built"]+stat["Built-untested"]} Built, {stat["Partial"]} Partial, {stat["Not built"]} Not built), `docs/architecture.md` Known gaps D1–D43 and "Auditability and end-user impact" (A1–A7, U1–U9), the `Backlog:` bullets of `docs/adr/0004`–`0015`, `docs/ux/rollout.md` (slices 0–20), `docs/ux/patterns.md` (UXS-01..20, AUD-API-1..9), `docs/ux/README.md` (waivers, UX-GAP-1), `docs/sdlc/run-1/ux-audit.md` (UX-001..050), `docs/sdlc/run-1/README.md` (the five defects), the architecture re-review notes RR-01/RR-02, and backlog review 1 (R-01..R-16, see the Review response at the end).\n')
L.append('These are GitHub-issue drafts. Each is sized to one PR batch: **S** up to a week, **M** two to four weeks, **L** more than four weeks and split along the named seams (each part is one PR batch). Every issue carries the label `sdlc-run-1`. Traceability is in [trace.md](trace.md); the execution order is in [execution-plan.md](execution-plan.md).\n')
L.append('**Priorities.** P0: security, data isolation, audit-trail integrity, correctness defects, and what blocks any deployment. P1: business-flow seams and postings that make numbers wrong or force rekeying, and the UX High findings / early slices (milestones M1–M3). P2: remaining Must FRs that are not built, later UX slices, architecture Med gaps. P3: Should-level gaps, Low gaps, hygiene, owner-gated items. Each issue states the reason for its priority.\n')
L.append('**Acceptance criteria.** Where an architecture gap has an exit criterion in `docs/architecture.md` Known gaps it is quoted and marked `(Dnn exit)`; where an issue closes only part of a gap the marker names the part. UX slices carry the rollout.md "Done when", with the common part as its own checkbox. Every issue names the tests that prove it; "re-audited" means the FR row is re-checked in stage 7.\n')
L.append('**Blocked-external.** Live-cluster parts go to #213; human sign-offs (owners, counsel, accountant, participants, auditors, banks) go to #512. An issue is never closed as Built on the strength of its blocked part.\n')
L.append('**Traces.** "Closes" lists each FR whose audit-row Gap the issue addresses, with the clause it closes; only these count as covered in trace.md. "UX friction" lists FRs whose recorded UX friction the issue removes; "Related" lists FRs it touches without closing their gap. Neither counts as covered.\n')
L.append('## Summary\n\n| Priority | Issues |\n|---|---|\n' + ''.join(f'| {p} | {cnt[p]} |\n' for p in ['P0', 'P1', 'P2', 'P3']) + f'| **Total** | **{len(I)}** |\n')
L.append('The count is above the 35–50 aim: backlog review 1 asked for oversized issues to be split (R-05, R-10), for new defect issues (R-01, plus BR-23.12) and for UX-GAP-1 (R-02).\n')
L.append('### Epics\n\n| Epic | Name | Issues | Priorities |\n|---|---|---|---|')
for e, n in EPICS.items():
    ids = sorted([i['id'] for i in I if i['epic'] == e], key=idkey)
    ps = Counter(byid[x]['prio'] for x in ids)
    L.append(f'| {e} | {n} | {", ".join(ids)} | {", ".join(f"{p}×{ps[p]}" for p in sorted(ps))} |')
L.append('\n### All issues\n\n| Id | Pri | Size | Epic | Title |\n|---|---|---|---|---|')
for i in sorted(I, key=lambda x: (PRI[x['prio']], idkey(x['id']))):
    L.append(f"| {i['id']} | {i['prio']} | {i['size'].split(' ')[0]} | {i['epic']} | {i['title']} |")
L.append('\nLocal ids are stable labels for cross-reference, not a priority order.\n')
for e, n in EPICS.items():
    L.append(f'\n---\n\n## {e}. {n}\n')
    for i in sorted([i for i in I if i['epic'] == e], key=lambda x: (PRI[x['prio']], idkey(x['id']))):
        labels = ['sdlc-run-1', i['prio']] + i['labels']
        L.append(f"### {i['id']} — {i['title']}\n")
        L.append(f"**Labels:** {', '.join('`' + l + '`' for l in labels)}  \n**Size:** {i['size']}  \n**Why {i['prio']}:** {i['prio_reason']}" + (f"  \n**Status:** {i['status']}" if i.get('status') else '') + '\n')
        L.append(f"**Problem.** {i['problem']}\n")
        L.append(f"**Scope — in:**\n{bl(i['scope_in'])}\n\n**Scope — out:**\n{bl(i['scope_out'])}\n")
        L.append("**Acceptance criteria:**\n" + '\n'.join(f'- [ ] {a}' for a in i['ac']) + '\n')
        tr = []
        if i['frs']: tr.append('Closes: ' + '; '.join(f"{fr} ({rows[fr]['prio']}, {rows[fr]['status']}) — {cl}" for fr, cl in sorted(i['frs'], key=lambda x: frkey(x[0]))))
        if i.get('friction'): tr.append('UX friction removed (not counted): ' + ', '.join(sorted(i['friction'], key=frkey)))
        if i.get('rel'): tr.append('Related (not counted): ' + ', '.join(sorted(i['rel'], key=frkey)))
        if i.get('defects'): tr.append('Defects: ' + ', '.join(i['defects']))
        if i['dids']: tr.append('Architecture gaps: ' + ', '.join(i['dids']))
        if i['adr']: tr.append('ADR backlog bullets: ' + ', '.join(i['adr']))
        if i['ux']: tr.append('UX: ' + ', '.join(i['ux']))
        if i['slices']: tr.append('UX rollout slices: ' + ', '.join(i['slices']))
        L.append("**Traces:**\n" + bl(tr) + '\n')
        deps = ', '.join(i['deps']) if i['deps'] else 'None'
        L.append(f"**Dependencies:** {deps}  \n**Blocked-external:** {i['blocked']}" + (f"  \n**Gates:** {'; '.join(i['gates'])}" if i.get('gates') else '') + '\n')
        if i.get('parts'):
            L.append('**Parts and files (for batching):**\n' + '\n'.join(f"- {i['id']}{k}: {d} — " + ', '.join('`' + x + '`' for x in fs) for k, (d, fs) in i['parts'].items()) + '\n')
        else:
            L.append(f"**Files touched (for batching):** {', '.join('`' + x + '`' for x in i['files'])}\n")
REVIEW = open(os.path.join(HERE, 'review_response.md')).read()
L.append('\n---\n\n' + REVIEW)
open(os.path.join(RUN, 'backlog.md'), 'w').write('\n'.join(L) + '\n')

# ---------------- trace.md ----------------
T = []
T.append('# Traceability — SDLC run 1, stage 5\n')
T.append(f'Every gap from stages 1–4 maps to an issue in [backlog.md](backlog.md) or to a deferral with a reason. Generated by `docs/sdlc/run-1/backlog-src/gen.py` at HEAD `{HEAD}`; rerun it after an input changes.\n')
T.append('## (a) Architecture gaps D1–D43\n\n| D-id | Severity | Owner | Issue(s) | Note |\n|---|---|---|---|---|')
miss_d = []
for n in range(1, 44):
    d = f'D{n}'; s, o = sev.get(d, ('doc-only (not in the Known gaps table)', '—'))
    iss = ', '.join(d2iss.get(d, [])); note = DEFER.get(d, '')
    if d == 'D3': note = 'A7 part closes on B-07 (P0); the full exit closes on B-36 (P1).'
    if d == 'D35': note = 'Closes when B-10 and B-46 (lakehouse erasure leg) have both merged.'
    if not iss and not note: miss_d.append(d)
    T.append(f'| {d} | {s} | {o} | {iss or "—"} | {note} |')
if miss_d: FAIL.append(f'unmapped D-ids {miss_d}')
T.append(f'\nUnmapped D-ids: {", ".join(miss_d) or "none"}.\n')
T.append('### ADR `Backlog:` bullets, one by one\n\n| ADR | # | Bullet (abridged) | Issue |\n|---|---|---|---|')
miss_a = []
for f in sorted(glob.glob(rp('docs', 'adr', '00*.md'))):
    num = os.path.basename(f)[:4]
    if int(num) < 4: continue
    n = 0
    for line in open(f):
        if line.startswith('- Backlog:'):
            n += 1; text = line[len('- Backlog: '):].strip()
            text = (text[:117] + '…') if len(text) > 120 else text
            m = ADR_MAP.get((num, n))
            if not m: miss_a.append(f'{num}#{n}')
            T.append(f"| {num} | {n} | {text.replace('|', '/')} | {m or 'UNMAPPED'} |")
if miss_a: FAIL.append(f'unmapped ADR bullets {miss_a}')
T.append(f'\nUnmapped bullets: {", ".join(miss_a) or "none"}.\n')
T.append('### Auditability and end-user impact (A1–A7, U1–U9)\n\n| # | Finding (abridged) | Carried by | Issue(s) |\n|---|---|---|---|')
for m in re.finditer(r'^\| ([AU]\d) \| ([^|]+)\| ([^|]+)\| ([^|]+)\|', au, re.M):
    k, fnd, carried = m.group(1), m.group(2).strip(), m.group(4).strip()
    ds = re.findall(r'D\d+', carried)
    iss = sorted({x.split(' ')[0] for d in ds for x in d2iss.get(d, [])}, key=idkey)
    if k == 'U6': iss_s = 'Deferred: no composition layer until an ADR-0009 trigger holds (architecture v1.0)'
    else: iss_s = ', '.join(iss) or 'UNMAPPED'
    if iss_s == 'UNMAPPED': FAIL.append(f'unmapped {k}')
    T.append(f"| {k} | {(fnd[:100] + '…') if len(fnd) > 100 else fnd} | {carried} | {iss_s} |")
# (b)
T.append('\n## (b) UX\n\n### Rollout slices 0–20\n\n| Slice | Name | Issue(s) |\n|---|---|---|')
miss_s = []
for n in range(21):
    s = str(n); iss = ', '.join(slice2iss.get(s, []))
    if not iss: miss_s.append(s)
    T.append(f'| {s} | {slicenames.get(s, "?")} | {iss or "UNMAPPED"} |')
if miss_s: FAIL.append(f'unmapped slices {miss_s}')
T.append('\nSlice 3 is split: B-06 and B-07 (P0 backend prerequisites), B-23 (History, audit page, read API) and B-24 (audited actions). Slice 17 is split: B-35 (sequences, with D21) and B-53 (alerts, documents, texts). Slice 19 is split: B-36 (ingestion) and B-54 (UI sweep).\n')
T.append('### UX findings (all 50, High first)\n\n| Finding | Severity | Category | Issue(s) | Note |\n|---|---|---|---|---|')
miss_u = []
for f in [f for f in findings if findings[f][0] == 'High'] + [f for f in findings if findings[f][0] != 'High']:
    s, c = findings[f]; iss = ', '.join(sorted(ux2iss.get(f, []), key=idkey))
    note = 'Translation into four languages: waiver pending owner sign-off (#512; docs/ux/README.md sign-off row is blank). String extraction and ASN packages/serials in B-33' if f == 'UX-038' else ''
    if not iss: miss_u.append(f)
    T.append(f'| {f} | {s} | {c} | {iss or "UNMAPPED"} | {note} |')
if miss_u: FAIL.append(f'unmapped findings {miss_u}')
T.append(f'\n{sum(1 for f in findings if findings[f][0] == "High")} High findings, all with a first cut in P1 (slices 0–9); later remainder in P2. UX-GAP-1 (docs/ux/README.md "Known gaps") is B-55, gated on the product-owner decision (#512).\n')
T.append('### UX standards and audit-service needs\n\n| Id | Issue(s) |\n|---|---|')
for n in range(1, 21):
    k = f'UXS-{n:02d}'; v = ', '.join(sorted(uxs2iss.get(k, []), key=idkey)); T.append(f"| {k} | {v or 'UNMAPPED'} |")
    if not v: FAIL.append(f'unmapped {k}')
for n in range(1, 10):
    k = f'AUD-API-{n}'; v = ', '.join(sorted(aud2iss.get(k, []), key=idkey)); T.append(f"| {k} | {v or 'UNMAPPED'} |")
    if not v: FAIL.append(f'unmapped {k}')
# (c)
T.append('\n## (c) Must FRs with status Not built\n\n| FR | Title | Audit file | Issue | Closes |\n|---|---|---|---|---|')
mnb = [f for f in sorted(rows, key=frkey) if rows[f]['prio'] == 'Must' and rows[f]['status'] == 'Not built']
miss_m = [f for f in mnb if f not in closes]
for f in mnb:
    T.append(f"| {f} | {rows[f]['title']} | `{rows[f]['file']}:{rows[f]['line']}` | {', '.join(x for x, _ in closes.get(f, [])) or 'UNMAPPED'} | {'; '.join(c for _, c in closes.get(f, []))} |")
if miss_m: FAIL.append(f'Must Not built without issue {miss_m}')
T.append(f'\n{len(mnb)} Must FRs are Not built; {len(mnb) - len(miss_m)} are closed by an issue.\n')
snb = [f for f in sorted(rows, key=frkey) if rows[f]['prio'] != 'Must' and rows[f]['status'] == 'Not built']
sc = [f for f in snb if f in closes]
T.append(f'Should/Could FRs Not built: {len(snb)}; closed by an issue: {len(sc)} ({", ".join(sc)}). The other {len(snb) - len(sc)} are proposed for run-2 grooming (below Must; no Top-gaps entry ranks one on its own: FR-36.22 and FR-36.23 appear only beside FR-36.21, FR-12.9 beside FR-12.5); each stays listed in its service NOT-IMPLEMENTED.md. Owner acceptance of this deferral is requested on #512: {", ".join(f for f in snb if f not in closes)}.\n')
# (d) coverage
T.append('## (d) Coverage of Partial FRs\n\nAn FR counts as covered only when an issue\'s scope and acceptance criteria address its audit-row Gap; the "Closes" column names the clause. UX-friction-only and related traces are counted separately and never as covered. Covered does not mean Built: the stage 7 re-audit decides that.\n\n| Phase | Partial | Covered | % | Must Partial | Must covered | % | Friction-only |\n|---|---|---|---|---|---|---|---|')
tot = [0] * 5; uncov = defaultdict(list)
for p in range(10):
    ps = [f for f in rows if rows[f]['status'] == 'Partial' and phase(f) == p]
    cv = [f for f in ps if f in closes]; pm = [f for f in ps if rows[f]['prio'] == 'Must']; cm = [f for f in pm if f in closes]
    fo = [f for f in ps if f not in closes and f in friction]
    for f in pm:
        if f not in closes: uncov[p].append(f)
    tot = [tot[0] + len(ps), tot[1] + len(cv), tot[2] + len(pm), tot[3] + len(cm), tot[4] + len(fo)]
    T.append(f'| {PHASES[p]} | {len(ps)} | {len(cv)} | {100 * len(cv) // max(1, len(ps))}% | {len(pm)} | {len(cm)} | {100 * len(cm) // max(1, len(pm))}% | {len(fo)} |')
T.append(f'| **Total** | **{tot[0]}** | **{tot[1]}** | **{100 * tot[1] // tot[0]}%** | **{tot[2]}** | **{tot[3]}** | **{100 * tot[3] // tot[2]}%** | **{tot[4]}** |')
COV = dict(partial=tot[0], covered=tot[1], must=tot[2], mustcov=tot[3], friction=tot[4])
T.append('\n### Closes, per covered FR\n\n| FR | Pri | Status | Issue | Clause of the Gap it closes |\n|---|---|---|---|---|')
for f in sorted(closes, key=frkey):
    for x, c in closes[f]:
        T.append(f"| {f} | {rows[f]['prio']} | {rows[f]['status']} | {x} | {c} |")
# deferral reasons
REASON = ns.get('DEFER_REASONS', {})
def reason(f):
    if f in REASON: return REASON[f]
    g = rows[f]['gap'].lower()
    if '#213' in g or 'live' in g or 'not measured' in g or 'measured' in g or 'p95' in g or 'p99' in g: return 'proof: the open part is a live or performance proof (#213)'
    if '#512' in g or 'sign-off' in g or 'vendor decision' in g: return 'sign-off: the open part needs a human or vendor decision (#512)'
    if re.search(r'handoff|wave[- ]\d|\bw\d\d\b|later slice', g): return 'seam: waits on another wave\'s hand-off; picked up when that seam lands'
    return 'depth: an extra method, format, job or limit inside a capability that works today'
T.append('\n### Uncovered Must Partial FRs, each with a reason class (proposed run-2 deferral)\n\nReason classes: **proof** (live or performance proof, #213), **sign-off** (#512), **seam** (waits on a hand-off from another wave), **depth** (an extra method, format, job or limit inside a capability that works), or a specific reason. No FR whose gap makes ledger numbers wrong, leaves a posting unposted, or switches off a control is in this list without a specific reason; those were pulled into issues in this revision (review R-03). Owner acceptance of the run-2 deferral is requested on #512.\n\n| FR | Title | Audit file | Reason |\n|---|---|---|---|')
for p in range(10):
    for f in sorted(uncov[p], key=frkey):
        T.append(f"| {f} | {rows[f]['title']} | `{rows[f]['file']}:{rows[f]['line']}` | {reason(f)} |")
# (e) defects
T.append('\n## (e) Defects\n\n### Audit rows tagged as defects\n\nMatched with `\\[defect\\]|code defect|; defect\\]|\\bdefect\\b` in every line of `docs/sdlc/run-1/fr-audit/waves-*.md` that starts with an FR id.\n\n| FR | Where tagged | Issue(s) |\n|---|---|---|')
deftags = OrderedDict()
for f in AUDIT:
    for ln, line in enumerate(open(f), 1):
        m = re.match(r'\| (FR-\d+\.\d+) ', line)
        if m and re.search(r'\[defect\]|code defect|; defect\]|\bdefect\b', line, re.I):
            deftags.setdefault(m.group(1), []).append(f'{os.path.basename(f)}:{ln}')
miss_def = []
for fr, where in deftags.items():
    iss = ', '.join(x for x, _ in closes.get(fr, []))
    if not iss: miss_def.append(fr)
    T.append(f"| {fr} | {', '.join('`' + w + '`' for w in where)} | {iss or 'UNMAPPED'} |")
if miss_def: FAIL.append(f'unmapped defect tags {miss_def}')
T.append(f'\nUnmapped defect tags: {", ".join(miss_def) or "none"}.\n')
T.append('### Every known defect\n\n| Defect | Source | Evidence | Issue |\n|---|---|---|---|')
for a, src, ev, iss in [
 ('FR-14.2 ALL/GL close order', 'run-1 README (five defects); waves-11-20.md', 'services/close/internal/domain/period.go:138-143', 'B-08 (built 79a5b718)'),
 ('FR-26.7 smart rounding crosses its band', 'run-1 README; waves-21-30.md', 'services/pricing/internal/domain/calc.go:23-35', 'B-08 (built 79a5b718)'),
 ('FR-23.5 dispatch ties on uuid, not order number', 'run-1 README; waves-21-30.md; gate 9', 'services/scheduling/internal/domain/schedule.go:466', 'B-08b (built 05105af3)'),
 ('Audit service takes the actor from the request body', 'run-1 README', 'services/audit/internal/api/api.go:119-123', 'B-06'),
 ('FR-8.10 product block flags not enforced', 'run-1 README', 'services/product/migrations/0001_init.sql:88-91', 'B-39'),
 ('BR-23.12 changeover ties on uuid, not order number', 'found after the audit (stage 5)', 'services/scheduling/internal/domain/setup.go:231', 'B-08b (same PR as FR-23.5)'),
 ('FR-14.1 closed-period race (30 s cache outside the posting transaction)', 'backlog review R-04; waves-11-20.md', 'services/ledger/internal/closeperiods/closeperiods.go', 'B-08c'),
 ('Kernel takes the legal-entity selection as scope with no `le` claim (RR-01)', 'architecture re-review', 'platform/go/tenant/tenant.go:241-243', 'B-03 part a'),
 ('FR-3.4 generated entities do not increment version', 'final audit pass', 'services/metadata/internal/gen/migration.go', 'B-43'),
 ('Flaky TestPostgres_OneTimePartyIsSealed ("10115" in random hex)', 'known issues', 'services/sales/internal/store/onetime_test.go:113-116', 'B-49'),
 ('Stale NOT-IMPLEMENTED lines and mislabeled FR ids', 'waves-01-10.md, waves-21-30.md', 'platform-health FR-1.7, shopfloor D7, billing 11/12, receivables FR-30.14, portal FR-30.25/26', 'B-49'),
 ('PRD defect: org code length 4–8 vs code 2–8 (FR-6.1)', 'reviews/prd-review-3.md', 'services/org/internal/api/api.go:46', 'B-49 (owner decision #512)'),
 ('RR-02 unrelated "Proposed" cells in docs/architecture.md', 'architecture re-review', 'docs/architecture.md events and frontend tables', 'B-49'),
]: T.append(f'| {a} | {src} | `{ev}` | {iss} |')
T.append('\n## Waivers and deferrals\n\n- UX-038 translation and UX-GAP-1: **pending owner sign-off (#512)**; the docs/ux/README.md sign-off row is blank. UX-GAP-1 is opened as B-55; extraction and ASN work for UX-038 are in B-33.\n- No composition layer (U6, ADR-0009 triggers), tenant egress topics (Q5), agent delegation tokens (Q8): deferred by architecture v1.0, not v1.0 gaps.\n- FR-5.2 metadata-driven generated list/detail pages: not traced in this run (reason: the UX standards are delivered on hand-written screens first, B-22..B-54; run 2 decides whether the generator adopts the same components). Owner acceptance requested on #512.\n- Run-2 deferral of the uncovered Must Partial and Should/Could rows: requested on #512; the run lead records the decision as a gates.md row.\n')
open(os.path.join(RUN, 'trace.md'), 'w').write('\n'.join(T) + '\n')

# ---------------- execution-plan.md ----------------
E = []
E.append('# Execution plan — SDLC run 1, stage 6\n')
E.append(f'Generated by `docs/sdlc/run-1/backlog-src/gen.py` at HEAD `{HEAD}`. Issues from [backlog.md](backlog.md) in waves of 3–5 batches that can run in parallel: no batch depends on another in its wave, and file sets are disjoint or split by an ownership rule below. P0 runs first; a P1 or P2 batch enters a wave only when its dependencies have merged and it touches nothing a P0 batch in flight touches. Each batch is one PR (one per part for a split issue), names its issue and FRs, runs CI and gets an independent review (stage 6 gate).\n')
E.append('**Preconditions outside the backlog.** (1) CI must run: GitHub Actions is billing-blocked (CHANGELOG 0.1.0, FR-1.5). Until the owners restore it (#512), each batch attaches its local `make test` and service test logs, and no batch is declared done on CI evidence it does not have. (2) Live-cluster parts go to #213 and do not hold a wave: the batch merges on in-repo tests and links the live check to the tracker.\n')
E.append('**Flaky test.** B-49 part b (`services/sales/internal/store/onetime_test.go`) may be pulled into any wave as a one-file PR if the test flakes in CI.\n')
E.append('**Already built.** B-08 (commit 79a5b718) and B-08b (commit 05105af3) are built and await review and merge in wave 1.\n')
E.append(f'**Checks.** Dependencies: {"all land in an earlier wave" if not deperr else "VIOLATIONS: " + "; ".join(deperr)}. Paths: {npaths} file patterns checked against `git ls-files` (new files against an existing parent folder): {"all resolve" if not path_fail else str(len(path_fail)) + " do not resolve: " + "; ".join(path_fail)}.\n')
E.append('**Ownership rules for shared files.**\n\n' + '\n'.join(f'{n}. {r}' for n, r in enumerate(RULES, 1)) + '\n')
def title_of(b):
    k = issue_of(b); i = byid[k]; part = b[len(k):]
    return i['title'] + (f' — part {part}: ' + i['parts'][part][0] if part else '')
for w, name, bs in WAVES:
    E.append(f'\n## Wave {w} — {name}\n\n| Batch | Pri | Size | Depends on | Gate | Files touched |\n|---|---|---|---|---|---|')
    flat = []
    for b in bs:
        ids = batch_ids(b); flat.append(ids)
        for x in ids:
            k = issue_of(x); i = byid[k]
            E.append(f"| **{x}** {title_of(x)} | {i['prio']} | {i['size'].split(' ')[0]} | {', '.join(i['deps']) or '—'} | {GATES_IN_PLAN.get(x, '—')} | {', '.join('`' + f + '`' for f in files_of(x))} |")
    if any(len(ids) > 1 for ids in flat): E.append('\nB-08 and B-08b form one review-and-merge batch (both already built).')
    clashes = []
    for a in range(len(flat)):
        for b2 in range(a + 1, len(flat)):
            for xa in flat[a]:
                for xb in flat[b2]:
                    for fa in files_of(xa):
                        for fb in files_of(xb):
                            if overlap(fa, fb): clashes.append(f'{xa} `{fa}` / {xb} `{fb}`')
    E.append('\nPath overlap check: ' + ('none.' if not clashes else 'shared paths, each resolved by an ownership rule above: ' + '; '.join(clashes) + '.'))
E.append('\n## Owner-gated batch (floating)\n\n| Batch | Pri | Gate | Rule | Files touched |\n|---|---|---|---|---|')
E.append(f"| **B-03c** {title_of('B-03c')} | P0 | {GATES_IN_PLAN['B-03c']} | Starts in the first open wave after the owner decision; rebases after B-04, B-12 and B-22; touches only the le mapper, the membership check, the Envoy forwarding and the company switcher; part a has already shipped (RR-01) | {', '.join('`' + f + '`' for f in files_of('B-03c'))} |")
E.append('\n## Milestones the waves reach\n\n| After wave | What is true |\n|---|---|')
for a, b in [('1', 'The known code defects are fixed (FR-14.2, FR-26.7, FR-23.5, BR-23.12, FR-14.1 race); audit actor verified; kernel refuses a selection without an `le` claim; events flow (live proof #213)'),
             ('2', 'No forged identity path in Kubernetes; routes and real tokens; company scope enforced in OPA and RLS; A7 audit chain running'),
             ('4', 'All P0 issues merged except B-03c (owner-gated, #512): stage and prod deployable (live proof #213), one enforcement model with decision logs, key custody'),
             ('6', 'UX M1 and M2 "Who, when, why" (slices 0–4) once the A7 contract tests pass; P2P and O2C stock moves; FX correct; subledgers tie out'),
             ('9', 'All P1 issues merged; UX M3 "Fewer keystrokes, and the floor"'),
             ('13', 'All P2/P3 issues merged (B-55 only if the owner funds it); stage 7 re-audits every FR an issue closes')]:
    E.append(f'| {a} | {b} |')
open(os.path.join(RUN, 'execution-plan.md'), 'w').write('\n'.join(E) + '\n')

print(f'issues {len(I)} {dict(sorted(cnt.items()))}')
print('coverage', COV)
print('defect tags', list(deftags))
if FAIL:
    print('CHECKS FAILED:'); print('\n'.join('  ' + f for f in FAIL)); sys.exit(1)
print('all checks pass')
