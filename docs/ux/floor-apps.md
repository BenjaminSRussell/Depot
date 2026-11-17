# Floor apps: kiosk and warehouse device

This is the design for UXS-09 ([patterns.md](patterns.md)). It covers the shared shop-floor kiosk (operators) and the warehouse handheld app (warehouse workers). The field technician app (`apps/technician`, code in `apps/web/src/modules/technician`) already follows most of it and is the reference: plain words, large targets, offline first, the online state and queue count always visible, no overflow at 390 px (`ux-scratch/shots/m_technician.png`). It stays the reference and is not rebuilt: when `shell/floor/` ships (rollout slice 8), the technician app takes the shared tokens and `QueueStatus` only, so the three floor apps look alike.

## 1. What changes, in one table

| Today | After |
|---|---|
| Kiosk is `/shopfloor/kiosk` inside the desktop shell, with hamburger, legal-entity and site pickers, Help and Compact view (UX-011) | Kiosk is `/kiosk` outside the shell, full screen, on an enrolled station; `/shopfloor/kiosk` redirects |
| Kiosk device id is the constant `"web-kiosk"` (`modules/shopfloor/KioskJobs.tsx:22`) | Each kiosk station is enrolled, has its own device id and is bound to a work cell or resource |
| Job reporting happens on the desktop `/shopfloor/jobs/:id` page; scrap reason is free text (UX-029) | Report good and scrap on the kiosk job screen; reasons are buttons from the reason master |
| Andon and downtime are supervisor screens (`/shopfloor/andon`, `/shopfloor/downtimes`) | "Call for help" and "Machine stopped" are one tap from the kiosk home |
| Device prompt "Scan the pick location" with no location shown (UX-010) | Each step shows the location, item and quantity from the work line in large type; the scan confirms |
| Cart built by typing `workId,orderId,LP` lines (`mobile-flows/ClusterPanel.tsx`) | Cart built from the worker's assigned work; positions shown by number |
| No sign-in on the device; work recorded as user `"unassigned"` (`apps/warehouse-mobile/src/main.tsx:29`) | Device enrolment by device flow, then badge or PIN per worker, PIN again after 8 hours |
| Count sheets saved one line at a time on a tablet page (UX-031) | Count flow on the device: scan location, scan item, enter count |
| "license plate", "cart position LP", lower-case "menu" (UX-043) | "Pallet" or "container" label wording from the glossary; sentence case |

## 2. The floor layout

One layout for both apps, `apps/web/src/shell/floor/FloorLayout.tsx`, modelled on the technician app header.

```
+------------------------------------------------------------+
| Station 3 · Assembly cell A          [Online] [0 to send]  |  header strip, 56 px
| Maria Lopez                                   [Sign out]   |
+------------------------------------------------------------+
|                                                            |
|  STEP AREA                                                 |
|  The instruction, in 20 px+ text                           |
|  The thing to act on, in 32-40 px text                     |
|  (location, item, quantity, job)                           |
|                                                            |
|  [ scan field: hidden caret, always focused ]              |
|                                                            |
+------------------------------------------------------------+
| [ Secondary ]                     [     PRIMARY ACTION    ] |  action bar, 64 px tall
+------------------------------------------------------------+
```

Rules:

- No sidebar, no breadcrumb, no work-context pickers, no Compact view, no command palette. The station's site and legal entity come from its enrolment.
- The header always shows: station or device name, the signed-in person, online or offline, and the number of actions waiting to send (the technician app's "Everything sent" / "3 waiting to send" pattern).
- One primary action per screen, full width at phone size, at the bottom where a thumb reaches.
- The scan field keeps focus whenever no other field is being edited. A hardware scanner (keyboard wedge, DataWedge `depot:scan` intent, Honeywell) always lands in it.
- Feedback on every scan: a success tone and short vibration (Vibration API where present) plus a green check and text; a failure tone, a long vibration, red border, an icon and the message. Never colour alone.

### Sizes

From `apps/web/src/modules/mobile-flows/domain/layout.ts` (already tested), applied to the kiosk too:

| Item | Size |
|---|---|
| Narrowest supported width | 320 px; no horizontal scroll |
| Body text | 16 px minimum (18 px on the kiosk) |
| Instruction text | 20 px |
| "Go to" location, item number, quantity | 32 px on the device, 40 px on the kiosk, tabular figures |
| Touch target | 48 px minimum; 64 px in gloves mode (`LARGE_TARGET_PX`), with text scaled by 1.25 |
| Primary action | 64 px tall, full width below 600 px |
| Spacing between targets | 8 px minimum |
| Reason buttons | An icon and a word on each (for example a broken-gear icon with "Machine fault"); icons from the shared icon set, never the icon alone, so workers with little English can scan the choices quickly |
| Orientation | Portrait and landscape; the kiosk is designed landscape-first (wall-mounted 10 to 15 inch), the device portrait-first |
| Contrast | The token pairs in `apps/web/docs/design-system.md` section 6 (4.5:1 text, 3:1 non-text); a high-contrast floor theme for bright warehouses is a token set, not new components |

Gloves mode is a per-device setting chosen at enrolment and changeable by a supervisor.

## 3. States

Every floor screen is in exactly one connection state and at most one notice state.

| State | Header | What the person can do | Text |
|---|---|---|---|
| Online | "Online" pill | Everything | — |
| Offline | "Offline" pill, amber | Everything the flow allows offline; actions queue | "You're offline. Your work is saved on this device and sent when the network is back." |
| Offline, queue near limit | Amber, with count | Continue; warned at 80% | Device: "Offline: 412 actions waiting; 88 left before the limit." (MOB-0105 in Details). Kiosk limit 5,000 actions (FR-25.12); device 500 actions or 24 hours (FR-32.5) |
| Offline, queue full | Red | Nothing that writes; reading cached work still works | "This device can't save more work offline. Move to where there is network to continue." (MOB-0106 in Details) |
| Sending | "Sending 12" | Continue working | — |
| Sent with conflicts | Notice under the header until dismissed | Open the conflict to read it; the supervisor resolves it in `/mobile-flows/conflicts` | "2 actions couldn't be applied. Your supervisor has been told. Pick of A-01-02 was already done by Lee Chen." |
| Error on a step | Red border on the step | Try again, or choose an exception | The step's plain message (section 7) |
| Signed out (idle) | Sign-in screen | Sign in | "Signed out after 10 minutes without input." (the kiosk shows the minutes the service returns, as today) |
| Locked: signed in elsewhere | Sign-in screen | Sign out the other device (if allowed) or ask a supervisor | "You're signed in on device WH-07. Sign out there, or ask a supervisor to sign you out." (MOB-SESSION-ACTIVE in Details) |
| PIN check due | Overlay, keeps the step | Enter PIN | "Enter your PIN to keep working." (after `pin_reauth_hours`, default 8) |
| Device not enrolled | Enrolment screen | Show code to a supervisor | Section 5 |

What works offline, by flow (defaults; the warehouse configuration can change them):

| Flow | Offline |
|---|---|
| Kiosk clock in/out, job start/stop, good/scrap report | Yes (queued, client-generated keys; already built in `modules/shopfloor/offlineQueue.ts`) |
| Kiosk andon call | Queued, and the screen says the call has not reached anyone yet: "Your call for help is saved but not sent. Find your supervisor if it is urgent." |
| Device receive, put-away, pick, pack, move | Yes, against work downloaded at sign-in |
| Device count | Off by default (§6.32.13); the menu item shows "Needs network" when offline |
| Device look-up | Shows the last downloaded value with its time: "On hand at 09:12: 24 EA" |

## 4. Scan grammar

The device already classifies scans (`mobile-flows/domain/scanRouting.ts`, `gs1.ts`). This grammar states what each scan means to the person and how one scan completes a step.

### Scan classes

| Class | What is scanned | Recognised by | Fills |
|---|---|---|---|
| Badge | Worker badge (kiosk and device sign-in) | The tenant's badge format (Code 128 or QR of the badge number) or NFC; only on the sign-in screen | Worker |
| Location | Shelf, bin or dock label | The warehouse's location label pattern; never parsed as GS1 when the step expects a location (existing rule: "10-01-02" is never AI 10) | Location |
| Container | Pallet or carton label | GS1 AI (00) SSCC, or the warehouse's own pallet id format | Container (today "LP") |
| Item | Product label | GS1 AI (01) GTIN or (02), or the product number barcode; may carry (10) batch, (17) expiry, (21) serial, (30) or (37) count, (310n) net weight | Item, and every carried field |
| Document | Paperwork: PO, ASN, transfer, order, job card | Document number barcode with the tenant's numbering prefix | The document (opens its work) |
| Quantity | Typed or carried in (30)/(37) | Number keypad or GS1 AI | Quantity |
| Command (optional) | Printed command cards on carts or stations ("Done", "Short", "Next position") | A reserved prefix configured per warehouse | A step action |

### Rules

1. **Expected value first.** A directed step knows the expected location, item and quantity from the work line. The screen shows them before any scan.
2. **One scan completes a step** when the scan matches the expected value and no other input is needed. When the item scan carries a quantity, or the expected quantity is 1, or the step's quantity defaults to the expected quantity and "confirm by scan" is on, the item scan completes the line. The person taps only to change the quantity or raise an exception.

   "Confirm by scan" defaults, per flow (a warehouse setting can turn each off):

   | Flow | Default | Why |
   |---|---|---|
   | Pick, cluster pick | On | The expected quantity comes from the work line; a short is an explicit exception |
   | Put away, move | On | The location scan is the confirmation |
   | Receive | On for lines received in full; the quantity field opens when the PO line has a tolerance or the item is catch-weight | Partial deliveries are common at the dock |
   | Pack | On | Each item scan adds one unit, or the GS1 count |
   | Count | Off | A count must be typed or scanned as a quantity; defaulting to the expected value would defeat the count |
3. **A GS1 scan fills every field it carries** (batch, expiry, serial, count) and the steps for those fields are skipped.
4. **Wrong value, right class** (scanned A-01-03, expected A-01-02): rejected. "That's A-01-03. Go to A-01-02." Error tone. The step stays.
5. **Wrong class** (scanned an item when a location is expected): rejected. "Scan the location label first: A-01-02." If the warehouse allows skipping location verification, the item scan is accepted and the location is recorded as the expected one, flagged in the log.
6. **Scan on a screen with no scan step** (a menu, a dialog): ignored and logged (existing rule AC-32.7), with a short notice "Scan ignored on this screen."
7. **Typed entry** is always possible with "Type it instead" (a numeric or text keypad); a typed entry is logged as such in the step timing for supervisors, never blocked.
8. **Keyboard wedge**: input that arrives faster than a person types and ends in Enter is treated as a scan.
9. **Duplicates**: the same scan twice within one second on the same step counts once.

### Example: directed pick, one line

```
[Pick 1 of 6 · Order SO-5521]
  Go to          A-01-02
  Pick           PMP-200  Booster pump seal kit
  Quantity       4 EA
  [scan]   ...person scans A-01-02 ......  check, tone
  [Pick 1 of 6]  Location A-01-02 confirmed
  Scan the item  PMP-200
  [scan]   ...person scans GS1 (01)...(10)B-77(30)4  check, tone
  -> line complete, batch B-77 recorded, next line shown
  Buttons:  [Short]  [Skip]  [Damaged]  [Undo last line]   (no primary tap needed)
```

### Undoing a mistake

People scan the wrong thing and tap the wrong number. Every floor action that writes can be undone briefly, then corrected with a reason:

| App | Action | Undo | After the undo window |
|---|---|---|---|
| Kiosk | Good or scrap report, job start or stop, clock in or out | "Undo" on the confirmation notice for 10 seconds, or until the next action | "Correct last report" on the job screen: a correction with a reason button (tier 1, UXS-12); clock corrections go to the supervisor (`/shopfloor/corrections`) |
| Device | A confirmed pick, put-away or count line | "Undo last line" until the next line is confirmed, or 10 seconds | The line is corrected by a supervisor override (BR-32.7) with a reason |
| Both, offline | A queued action | Undo removes it from the queue before it is sent | — |

An undo is itself recorded (who, when, what was undone), so the trail stays complete.

Two scans, zero taps. Today the same line is a location scan, an item scan, a typed quantity, a branch answer and a confirm tap, with no expected values shown (`PICK_FLOW` in `coreFlows.ts`).

## 5. Sign-in

### Kiosk station

1. **Enrol the station (once).** A supervisor opens `/kiosk` on the new station. It shows "This station isn't set up yet" with a code and a QR. The supervisor approves it on a desktop at `/shopfloor/stations` (new route) and binds it to a site, a work cell or resource, the reason lists it uses, and gloves mode. The station stores its device credential; the shared edge enrolment in `services/shopfloor` (edge nodes) is the starting point.
2. **Worker sign-in.** Scan badge (or NFC tap), or type badge number and PIN when the badge is forgotten (FR-25.1). One tap to sign in, no password.
3. **Switch user** in at most two taps: "Sign out" then the next badge scan (FR-25.1).
4. **Idle sign-out** after the service's timeout (10 minutes by default, already built), with a 30-second countdown overlay "Signing you out in 30 seconds. Tap to stay."

### Warehouse device

1. **Enrol the device (once, or after a reset).** The app uses the OAuth 2.0 device authorization grant through Keycloak (FR-32.1): the device shows a short code and a QR; a supervisor approves it at `/mobile-flows/devices` (new route, also closing UX-044) and binds it to a warehouse, a menu and gloves mode. Today this is a handoff to edge-identity (`apps/warehouse-mobile/NOT-IMPLEMENTED.md`).
2. **Worker sign-in per shift.** Scan badge, then a 4 to 6 digit PIN. The device downloads the worker's assigned work.
3. **PIN again** after `pin_reauth_hours` (default 8, FR-32.1), without losing the current step.
4. **One device per worker.** The service enforces one active session (built: `DeviceApp` shows "another worker is signed in"); the message follows section 3.
5. **Supervisor override** by PIN for one action within 60 seconds (BR-32.7, built in `exceptions.ts`; the server PIN check is a handoff). The override screen names the exact action: "Ana Ruiz approves: short pick of 2 EA at A-01-02."

## 6. Screen flows

### 6.1 Kiosk

```
[Station not set up] --supervisor approves--> [Sign in]

[Sign in]  "Scan your badge"
   |  badge scan
   v
[Home: Maria Lopez]
   Status: "Clocked out" / "Clocked in since 06:02"
   [ Clock in ] or [ Clock out ]              (primary)
   My jobs (from KioskJobs: product name, operation, planned qty, planned start)
     PMP-200 Booster pump seal kit · Op 20 Assembly · 40 EA   [Start]
     ...
   [ Call for help ]  [ Machine stopped ]  [ Instructions ]
   |
   +-- Clock in --> notice "Clocked in at 06:02." --> Home (jobs enabled)
   |
   +-- Start (job) --> [Job running]
   |                     PMP-200 Booster pump seal kit
   |                     Op 20 Assembly · Work order PO-7781
   |                     Done 12 of 40 EA (machine count if linked, FR-25.13)
   |                     [ Report quantity ]  [ Stop ]  [ Instructions ]
   |                        |
   |                        v
   |                     [Report quantity]
   |                       Good [ 12 ] (prefilled from machine count)   +/- buttons
   |                       Scrap [ 0 ]   if > 0: reason buttons from the scrap reason master
   |                         [ Material defect ] [ Machine fault ] [ Setup ] [ Other: note ]
   |                       [ ] Operation finished
   |                       [ Save report ]  --> notice --> Job running or Home
   |
   +-- Call for help --> [Call for help] reason buttons (Quality, Material, Machine, Safety)
   |                     --> "Your supervisor has been called." --> Home
   |
   +-- Machine stopped --> reason buttons from the downtime reason master
   |                       --> "Downtime started at 09:41 on Press 4." --> Home with a banner
   |                           "Press 4 stopped since 09:41 · [ Machine running again ]"
   |
   +-- Instructions --> [PI sheet or work instruction for the running job]
                         (existing KioskPiPage, without needing the id in the URL)
                         Signature steps go through the step-up (UXS-12)

Idle 10 min --> countdown --> [Sign in]
```

Routes: `/kiosk` (sign in and home), `/kiosk/jobs/:id`, `/kiosk/pi/:id`. All outside `Shell`, mounted like `/technician` in `apps/web/src/main.tsx`. They need building; `/shopfloor/kiosk` and `/shopfloor/kiosk/pi/:id` exist today inside the shell.

### 6.2 Warehouse device

```
[Device not enrolled] --supervisor approves--> [Sign in]
[Sign in] badge scan --> PIN --> downloads assigned work
   v
[Menu]                          header: WH-07 · Main warehouse · Lee Chen · Online · 0 to send
   [ Next task ]   (the next assigned work, whatever its type; primary)
   [ Receive ] [ Put away ] [ Pick ] [ Move ] [ Count ] [ Look up ]
   [ Exceptions ]  [ Sign out ]

Receive
  Scan the delivery: ASN, PO or container
   -> shows vendor, PO, lines expected (product name, open qty)
  Per line: scan item -> qty defaults to open qty (tap to change) -> line received
  New container? scan or "Print a pallet label" (existing ReceiveLabelPanel)
  [ Finish receipt ]  -> "Received 3 lines on PO-1042. Put-away work created."

Put away (directed)
  Scan the container -> "Go to B-04-01" (from put-away rules)
  Scan B-04-01 -> done (one scan per step)
  Location full? [ Location full ] -> next suggested location

Pick (directed; see section 4 example)
  Per line: Go to / Pick / Quantity -> scan location -> scan item -> done
  [ Short ] -> reason buttons -> supervisor PIN if the warehouse requires it

Cluster pick
  [Start cart] -> assigned orders shown as positions 1..8 ("Position 3: SO-5521, 2 lines")
  Scan each position's container label once -> positions bound
  Per line: Go to / Pick / Put in position 3 -> scan location -> scan item -> scan position -> done

Count
  Scan location -> "Count PMP-200 at A-01-02" (or "Count everything at A-01-02" for a blind count)
  Scan item -> enter count -> next item or [ Location done ]

Move
  Scan container or item at source -> quantity (defaults to all) -> scan destination -> done

Look up
  Scan item, location or container -> on hand by location, with the time of the figure
```

The device needs the expected work line from the mobile service for every step. The mobile service already drives pick and receive from the warehouse work API; count, move, pack and cluster have no warehouse call yet (`apps/warehouse-mobile/NOT-IMPLEMENTED.md`). Those flows ship directed when their calls exist; until then they stay on the menu but say "Not directed yet: scan what you handle" so the screen is honest (UXS-01).

## 7. Copy on the floor

Short, imperative, the object first. The code never leads.

| Situation | Text |
|---|---|
| Scan step | "Scan the location label" with "A-01-02" in large type under it |
| Wrong location | "That's A-01-03. Go to A-01-02." |
| Wrong label type | "That's an item label. Scan the location label first." |
| Unknown badge | "This badge isn't recognised. Ask your supervisor." (built) |
| Item not on this order | "PMP-310 isn't on this pick. Check the shelf or choose Short." |
| Over quantity | "That's more than the 4 EA to pick." |
| Expired batch | "Batch B-77 expired on 2 Nov. Don't pick it; choose Damaged or ask your supervisor." |
| Offline queued | "Saved on this device. It sends when the network is back." |
| Conflict after sync | "Pick at A-01-02 wasn't applied: Lee Chen already picked it." |

Glossary for the floor: "container" or "pallet" (not LP or license plate), "location" (not bin, unless the warehouse calls it that in its own labels), "job" (kiosk), "task" (device next work), "count" (not stock take).

## 8. Build list

| Piece | Path | Reuse |
|---|---|---|
| Floor layout, big button, scan field, reason buttons, queue status | `apps/web/src/shell/floor/` | Technician app CSS pattern; `layout.ts` sizes |
| Kiosk routes outside the shell | `apps/web/src/main.tsx`, `modules/shopfloor/kiosk/` | `KioskPage`, `KioskJobs`, `KioskPiList`, `KioskPiPage`, `KioskSyncStatus`, `offlineQueue.ts` |
| Kiosk job report and reasons | `modules/shopfloor/kiosk/JobScreen.tsx` | Job report logic from `JobDetailPage.tsx`; downtime reasons from `/shopfloor/downtime-reasons` |
| Station enrolment screen | `/shopfloor/stations` | Edge node pages (`EdgeNodesPage.tsx`) |
| Directed device flows | `modules/mobile-flows/domain/coreFlows.ts` (prompts with expected values), `DeviceApp.tsx` | `flowRunner.ts`, `scanRouting.ts`, `gs1.ts` |
| Device sign-in and enrolment | `apps/warehouse-mobile/src/`, `modules/mobile-flows/DeviceApp.tsx` | `sessionKeeper.ts`, `session.ts` |
| Device administration | `/mobile-flows/devices` | `services/mobile` sessions, devices, force sign-out, stale sweep (built server side) |
| Operator workspace | `shell/workspaces/workspaces.json` | — |

Server dependencies (stage 5 backlog): device flow client registration (edge-identity); per-station kiosk credential; mobile service calls for count, move, pack and cluster against the warehouse work API; the server PIN check; scrap and downtime reason masters exposed to the kiosk; a gateway route to `services/mobile`.

## 9. Acceptance and tests

| # | Check | Test |
|---|---|---|
| F1 | `/kiosk` and every device screen render no desktop shell element | New `e2e/floor-layout.spec.ts` |
| F2 | No control under 48 px (64 px in gloves mode); body text at least 16 px | `e2e/floor-layout.spec.ts` (same check as `e2e/tablet-layout.demo.spec.ts`), `layout.test.ts` |
| F3 | No horizontal scroll at 320 px on any floor screen | `e2e/floor-layout.spec.ts` |
| F4 | Each directed step shows location, item and quantity before a scan | `coreFlows.test.ts`, `e2e/warehouse-mobile.spec.ts` |
| F5 | A matching scan completes the step without a tap when quantity equals expected | `e2e/warehouse-mobile.spec.ts` (count taps between scans: 0) |
| F6 | Wrong-value and wrong-class scans show the section 7 text and keep the step | `scanRouting.test.ts`, `DeviceApp.test.tsx` |
| F7 | Offline: actions queue; count and limit visible; full queue blocks writes with the plain message | `apps/web/e2e/kiosk-offline.spec.ts`, `apps/web/src/modules/mobile-flows/domain/offlineQueue.test.ts`, `apps/web/src/modules/mobile-flows/DeviceApp.wired.test.tsx`, `apps/web/src/modules/shopfloor/kiosk-offline.test.tsx`, and a new `apps/web/src/modules/shopfloor/offlineQueue.test.ts` for the kiosk queue (limits, order, idempotent replay), which has no unit test today |
| F8 | Scrap, downtime and short reasons are buttons with an icon and text, from the master | `modules/shopfloor/kiosk-pages.test.tsx`, `ExceptionsPanel.test.tsx` |
| F8a | Undo within the window, correction with a reason after it; the undo is recorded | `modules/shopfloor/kiosk-pages.test.tsx`, `DeviceApp.test.tsx` |
| F8b | "Confirm by scan" defaults per flow as in section 4 | `coreFlows.test.ts` |
| F9 | Sign-in: badge, PIN fallback, switch user in two taps, idle sign-out with countdown | `modules/shopfloor/kiosk-pages.test.tsx`, `e2e/kiosk-labor.spec.ts` |
| F10 | No work recorded with user "unassigned" | `DeviceApp.wired.test.tsx`: replay entries carry a real user |
| F11 | Interaction budgets for OP1 to OP6 and WH6 to WH9 met | `e2e/task-budgets.demo.spec.ts` ([usability-programme.md](usability-programme.md)) |
| F12 | Floor tasks pass usability sessions on real devices, with gloves where the job uses them | Usability round ([usability-programme.md](usability-programme.md)) |
| F13 | Manual assistive-technology pass (VoiceOver on iOS, TalkBack on Android, 200% text, keyboard on the kiosk) before each floor release | AT pass report (UXS-15) |
