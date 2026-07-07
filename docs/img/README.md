# Screenshots — capture these

The main `README.md` references the images below. They don't exist yet —
**capture them from a running instance and commit them here** (PNG, ~1400px
wide, light theme, trimmed to the content area). Until then the README links
render as broken-image placeholders, which is expected.

| File | View | What to show |
|------|------|--------------|
| `home.png` | Home tab (default) | the pipeline-map strip (Catalog → Package → Write → Verify) with live counts, and the per-archive health table showing package-status stamps, protection, drift, and verify-due |
| `getting-started.png` | Home tab, empty catalog | the Getting Started checklist with a mix of ✓ done and numbered next-step buttons (run against a fresh `-data` dir) |
| `vault.png` | Vault tab | archives list, the file-search box with a result showing copies/volumes, and a drift badge |
| `packages.png` | Packages tab | a few packages across the lifecycle rail (PLANNED → STAGED → VERIFIED), build timings + copy tags visible |
| `volumes.png` | Volumes tab | registered volumes with barcode/location/last-verified, and one expanded showing its packages + files |
| `verify.png` | Packages / a package detail | verify history, an "under-protected (1 of 2 copies)" tag, and ring-buffer telemetry from a throttled write |

Optional extras worth having: `burn.png` (burn queue disc squares),
`recoverykit.png` (Settings/Keys → recovery kit warning), `settings.png`
(Preflight tool checks + LTFS row).

To grab them: run the binary (`mnemosyne -port 7821`), open
<http://127.0.0.1:7821>, and screenshot each tab. A throwaway `-data` dir with
a small sample archive makes for clean, representative shots.
