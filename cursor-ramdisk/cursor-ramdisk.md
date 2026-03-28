# Cursor RAM Disk

Move Cursor's hot-path state directories from SSD to a RAM disk to reduce
SSD wear and improve I/O latency. With 128 GB of RAM, a 12 GB RAM disk costs
nothing meaningful.

## What gets moved

Targets are based on observed write patterns during an active session, not
guesswork. Cold directories (extension bundles, GPU cache) are left on disk.

| Directory | Size | Why it's hot |
|---|---|---|
| `User/globalStorage/` | ~9.6 GB | `state.vscdb` + WAL written every few seconds |
| `User/workspaceStorage/` | ~1.2 GB | per-workspace `state.vscdb`, written constantly |
| `User/History/` | ~60 MB | written on every file save |
| `Cache/` | ~128 MB | HTTP cache, written on every network request |

Total RAM budget: ~11 GB. A 12 GB RAM disk is used.

## Commands

```
cursor-ramdisk setup     move hot dirs to RAM disk and symlink back (idempotent)
cursor-ramdisk teardown  rsync RAM disk state back to disk, remove symlinks
cursor-ramdisk status    show current state of each target directory
```

Cursor must not be running for `setup` or `teardown`.

## First-time setup

```bash
# 1. Quit Cursor completely, then:
cursor-ramdisk setup
```

What it does:

1. Takes an APFS local snapshot (`tmutil localsnapshot`) -- instant, no
   external drive needed.
2. Creates a 12 GB RAM disk at `/Volumes/CursorRAM`.
3. For each target directory:
   - Copies it to the RAM disk.
   - Renames the original to `<dir>.orig` (cold backup, never touched again).
   - Replaces the original path with a symlink to the RAM disk copy.

`setup` is idempotent -- directories already on the RAM disk are skipped, so
it is safe to run again after a partial failure or reboot.

## After every reboot

The RAM disk is volatile and is gone after a reboot. The symlinks left by
`setup` will be dangling. Run `teardown` before rebooting if you care about
preserving session state, or `setup` again after rebooting to get back on the
RAM disk with `<dir>.orig` as the seed.

### Recommended shutdown ritual

```bash
# Before rebooting:
cursor-ramdisk teardown
```

Then reboot. After the next login:

```bash
cursor-ramdisk setup
```

### If you forgot to teardown before a reboot

`setup` still works -- it seeds from `.orig` (which is the state at the time
of the last `setup` call). Session state written to the RAM disk since then is
lost.

`teardown` also handles this: if the RAM disk is gone it falls back to `.orig`
automatically.

## Teardown

```bash
cursor-ramdisk teardown
```

What it does (for each target directory):

1. rsyncs the live RAM disk directory to a fresh `<dir>.new` on disk (only
   changed files, preserving all metadata).
2. Removes the symlink.
3. Renames `<dir>.new` to the original path -- atomically replacing the
   symlink with the synced directory.
4. Removes `<dir>.orig` since the on-disk copy is now current.

If the RAM disk is already gone (post-reboot), teardown detects the dangling
symlink and falls back to restoring from `<dir>.orig` instead.

## How it works

```
Before setup:
  ~/Library/Application Support/Cursor/User/globalStorage/   (directory on SSD)

After setup:
  ~/Library/Application Support/Cursor/User/globalStorage    (symlink)
      -> /Volumes/CursorRAM/User_globalStorage/              (live, in RAM)
  ~/Library/Application Support/Cursor/User/globalStorage.orig/  (cold backup on SSD)

After teardown:
  ~/Library/Application Support/Cursor/User/globalStorage/   (directory on SSD, rsynced from RAM)
  (globalStorage.orig removed -- on-disk copy is now current)
```

## Drift and the `.orig` backup

`.orig` is a **cold backup** taken once at setup time. It is only used as a
fallback if teardown runs after the RAM disk is already gone (e.g. you forgot
to teardown before rebooting). It is removed after a successful teardown.

Under normal operation (setup → use Cursor → teardown → reboot → setup) there
is no drift: teardown always rsyncs the live RAM disk state to disk before
removing the symlink.

## Periodic background sync (future work)

A launchd plist could run `rsync` from the RAM disk to a `.sync` directory on
disk every N minutes while Cursor is running, so state is preserved even after
a crash. The rough design:

```xml
<!-- ~/Library/LaunchAgents/io.goodkind.cursor-ramdisk-sync.plist -->
<key>ProgramArguments</key>
<array>
  <string>/path/to/cursor-ramdisk-sync</string>  <!-- thin wrapper around syncDir -->
</array>
<key>StartInterval</key>
<integer>300</integer>  <!-- every 5 minutes -->
```

The sync wrapper would: check that `/Volumes/CursorRAM` is mounted, check that
Cursor is running, and rsync each RAM disk directory to a `.sync` sibling on
disk. On the next `teardown`, it would prefer `.sync` over the raw RAM disk if
Cursor crashed and the RAM disk is gone. This is left as future work since the
main crash risk for a homelab machine is low.
