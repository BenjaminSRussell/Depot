# Release notes

One committed file per release: `docs/releases/<release-id>.md`, copied from `docs/ops/release-notes-template.md`. `<release-id>` is the release tag (`v*`, the form `deploy/scripts/stage_release.py` accepts) or an ISO date.

`.github/workflows/record-release.yml` runs the usability gate before it records a release. It reads the newest file here (filename order) as the release notes, and the last five files before it as the previous releases for the waiver and provisional-reference counts (rules G7 and G8, `docs/ux/usability-programme.md` section 6). With no previous file, those counts run against zero previous releases and the gate says so. With no file at all, the gate fails and nothing is recorded.

This folder starts with no notes file. A notes file is added when a release is cut.
