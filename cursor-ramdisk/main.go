// cursor-ramdisk manages moving Cursor IDE's hot-path state directories onto a
// RAM disk and symlinking them back to their original locations.
//
// Target directories (based on observed write patterns during active sessions):
//
//	User/globalStorage/    ~9.6 GB  state.vscdb + WAL, written every few seconds
//	User/workspaceStorage/ ~1.2 GB  per-workspace state.vscdb, written constantly
//	User/History/           ~60 MB  written on every file save
//	Cache/                 ~128 MB  HTTP cache, written on every network request
//
// Usage:
//
//	cursor-ramdisk [-y] setup     # move hot dirs to RAM disk and symlink back (idempotent)
//	cursor-ramdisk [-y] teardown  # rsync RAM disk back to disk, remove symlinks (safe)
//	cursor-ramdisk status         # show current state of each target dir
//
// -y skips all interactive confirmation prompts.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type runner func(name string, args ...string) error

type outputRunner func(name string, args ...string) ([]byte, error)

type app struct {
	run        runner
	runOutput  outputRunner
	stdin      io.Reader
	stderr     io.Writer
	stdout     io.Writer
	ramdisk    string
	volName    string
	headroom   int
	dirs       []string
	cursorDir  string
	userHome   func() (string, error)
	executable func() (string, error)
	rename     func(oldpath, newpath string) error
	symlink    func(oldpath, newpath string) error
	remove     func(name string) error
	removeAll  func(path string) error
}

const (
	ramdiskMount    = "/Volumes/CursorRAM"
	ramdiskVolName  = "CursorRAM"
	ramdiskHeadroom = 4096 // MB of headroom added on top of measured dir sizes
)

var defaultTargetDirs = []string{
	"User/globalStorage",
	"User/workspaceStorage",
	"User/History",
	"Cache",
}

// osExit is swappable in tests so main() can be covered without exiting the process.
var osExit = os.Exit

func newApp() *app {
	a := &app{
		stdin:     os.Stdin,
		stderr:    os.Stderr,
		stdout:    os.Stdout,
		ramdisk:   ramdiskMount,
		volName:   ramdiskVolName,
		headroom:  ramdiskHeadroom,
		dirs:      append([]string(nil), defaultTargetDirs...),
		cursorDir: "",
		userHome:  nil,
	}
	a.run = func(name string, args ...string) error {
		cmd := exec.Command(name, args...)
		cmd.Stdout = a.stdout
		cmd.Stderr = a.stderr
		return cmd.Run()
	}
	a.runOutput = func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).Output()
	}
	a.executable = os.Executable
	a.rename = os.Rename
	a.symlink = os.Symlink
	a.remove = os.Remove
	a.removeAll = os.RemoveAll
	return a
}

func main() {
	osExit(newApp().runArgs(os.Args[1:]))
}

func (a *app) runArgs(args []string) int {
	yes := false
	filtered := args[:0]
	for _, arg := range args {
		if arg == "-y" {
			yes = true
		} else {
			filtered = append(filtered, arg)
		}
	}
	args = filtered

	if len(args) < 1 {
		a.usage()
		return 1
	}

	var err error
	switch args[0] {
	case "setup":
		err = a.cmdSetup(yes)
	case "teardown":
		err = a.cmdTeardown(yes)
	case "status":
		err = a.cmdStatus()
	case "sync":
		err = a.cmdSync()
	case "install-sync":
		err = a.cmdInstallSync()
	case "uninstall-sync":
		err = a.cmdUninstallSync()
	default:
		a.usage()
		return 1
	}

	if err != nil {
		fmt.Fprintf(a.stderr, "ERROR: %v\n", err)
		return 1
	}
	return 0
}

func (a *app) usage() {
	fmt.Fprintf(a.stderr, `cursor-ramdisk: manage Cursor IDE state on a RAM disk

Usage:
  cursor-ramdisk [-y] setup         move hot dirs to RAM disk and symlink back (idempotent)
  cursor-ramdisk [-y] teardown      rsync RAM disk state back to disk, remove symlinks
  cursor-ramdisk status             show current state of each target directory
  cursor-ramdisk sync               rsync RAM disk -> .sync on disk (no-op if Cursor not running)
  cursor-ramdisk install-sync       install launchd agent to run sync every 5 minutes
  cursor-ramdisk uninstall-sync     remove the launchd sync agent and its plist

Flags:
  -y  non-interactive: skip all confirmation prompts

Cursor must not be running for setup or teardown.
`)
}

// ---------------------------------------------------------------------------
// Commands
// ---------------------------------------------------------------------------

// healDanglingSymlinks detects symlinks whose targets no longer exist (e.g.
// the RAM disk vanished after a reboot) and restores them from .sync or .orig.
// This lets setup work correctly after an unclean reboot without requiring the
// user to run teardown first.
func (a *app) healDanglingSymlinks(cursorDir string) error {
	for _, dir := range a.dirs {
		src := filepath.Join(cursorDir, filepath.FromSlash(dir))
		info, err := os.Lstat(src)
		if err != nil {
			continue // missing entirely — pendingDirs will skip it too
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			continue // regular dir, nothing to heal
		}
		if symlinkTargetExists(src) {
			continue // valid symlink pointing at a live RAM disk
		}

		// Dangling symlink — RAM disk is gone. Restore from best available backup.
		orig := src + ".orig"
		syncDir := src + ".sync"

		a.logf("Healing dangling symlink: %s", dir)

		if _, statErr := os.Stat(syncDir); statErr == nil {
			a.logf("  Restoring from .sync (most recent periodic backup)...")
			if err := a.remove(src); err != nil {
				return fmt.Errorf("remove dangling symlink %s: %w", src, err)
			}
			if err := a.rename(syncDir, src); err != nil {
				return fmt.Errorf("restore sync %s -> %s: %w", syncDir, src, err)
			}
			if err := a.removeAll(orig); err != nil {
				a.logf("WARN: could not remove .orig %s: %v", orig, err)
			}
			a.logf("  Restored from .sync: %s", dir)
		} else if _, statErr2 := os.Stat(orig); statErr2 == nil {
			a.logf("  Restoring from .orig (cold backup from last setup)...")
			if err := a.remove(src); err != nil {
				return fmt.Errorf("remove dangling symlink %s: %w", src, err)
			}
			if err := a.rename(orig, src); err != nil {
				return fmt.Errorf("restore orig %s -> %s: %w", orig, src, err)
			}
			a.logf("  Restored from .orig: %s", dir)
		} else {
			a.logf("  WARN: no .sync or .orig for %s -- removing dangling symlink", dir)
			if err := a.remove(src); err != nil {
				return fmt.Errorf("remove dangling symlink %s: %w", src, err)
			}
			// Dir won't exist; pendingDirs will skip it and Cursor will recreate it.
		}
	}
	return nil
}

// cmdSetup is idempotent: it skips any directory that is already a symlink
// pointing at a live RAM disk. Dangling symlinks (left after a reboot that
// erased the RAM disk) are healed automatically from .sync or .orig before
// the pending-dirs check, so setup works after an unclean reboot without
// requiring teardown first.
//
// On first run it takes an APFS snapshot, measures the actual disk usage of
// pending directories, creates a right-sized RAM disk (measured total +
// headroom MB), copies each target directory onto it, saves the original as
// <dir>.orig (never touched again), and replaces the original path with a
// symlink.
//
// .orig is a one-time cold backup taken at setup time. All durable state is
// managed by teardown (which rsyncs the live RAM disk back to disk).
func (a *app) cmdSetup(yes bool) error {
	cursorDir, err := a.cursorAppSupportDir()
	if err != nil {
		return err
	}

	if err := a.guardCursorNotRunning(); err != nil {
		return err
	}

	// Heal dangling symlinks left by a crash/reboot before checking pending.
	if err := a.healDanglingSymlinks(cursorDir); err != nil {
		return err
	}

	pending := a.pendingDirs(cursorDir)
	if len(pending) == 0 {
		a.logf("All directories already on RAM disk. Nothing to do.")
		return a.cmdStatus()
	}

	// Measure actual sizes so we can right-size the RAM disk.
	totalMB, sizes, err := a.measureDirs(cursorDir, pending)
	if err != nil {
		return err
	}
	ramdiskSizeMB := totalMB + a.headroom

	a.logf("Directories to move:")
	for _, dir := range pending {
		a.logf("  %-30s  %d MB", dir, sizes[dir])
	}
	a.logf("Total: %d MB  +  %d MB headroom  =  %d MB RAM disk",
		totalMB, a.headroom, ramdiskSizeMB)

	if !yes {
		if !a.confirm("Proceed with setup?") {
			a.logf("Aborted.")
			return nil
		}
	}

	a.logf("Creating local APFS Time Machine snapshot...")
	if err := a.run("tmutil", "localsnapshot"); err != nil {
		return fmt.Errorf("tmutil localsnapshot: %w", err)
	}
	a.logf("Snapshot created.")

	if err := a.ensureRamdisk(ramdiskSizeMB); err != nil {
		return err
	}

	for _, dir := range pending {
		src := filepath.Join(cursorDir, filepath.FromSlash(dir))
		dest := filepath.Join(a.ramdisk, flattenPath(dir))
		orig := src + ".orig"

		a.logf("Copying %s -> %s ...", dir, dest)
		if err := a.copyDir(src, dest); err != nil {
			return fmt.Errorf("copy %s -> %s: %w", src, dest, err)
		}

		a.logf("Saving original as %s ...", orig)
		if err := a.rename(src, orig); err != nil {
			return fmt.Errorf("rename %s -> %s: %w", src, orig, err)
		}

		a.logf("Symlinking %s -> %s ...", src, dest)
		if err := a.symlink(dest, src); err != nil {
			return fmt.Errorf("symlink %s -> %s: %w", dest, src, err)
		}

		a.logf("Done: %s", dir)
	}

	fmt.Fprintln(a.stdout)
	return a.cmdStatus()
}

// cmdTeardown rsyncs the live RAM disk state back to disk, replaces each
// symlink with the synced directory, and ejects the RAM disk. The .orig cold
// backup is removed after a successful rsync since the on-disk directory is
// now current.
func (a *app) cmdTeardown(yes bool) error {
	cursorDir, err := a.cursorAppSupportDir()
	if err != nil {
		return err
	}

	if err := a.guardCursorNotRunning(); err != nil {
		return err
	}

	// Show what will happen before doing anything destructive.
	active := a.activeDirs(cursorDir)
	if len(active) == 0 {
		a.logf("No directories are on the RAM disk. Nothing to do.")
		return a.cmdStatus()
	}

	a.logf("Directories to sync back to disk:")
	for _, dir := range active {
		dest := filepath.Join(a.ramdisk, flattenPath(dir))
		if _, err := os.Stat(dest); errors.Is(err, fs.ErrNotExist) {
			a.logf("  %-30s  (RAM disk gone -- will restore from .orig)", dir)
		} else {
			a.logf("  %-30s  %s", dir, a.dirSize(dest))
		}
	}

	if !yes {
		if !a.confirm("Proceed with teardown?") {
			a.logf("Aborted.")
			return nil
		}
	}

	for _, dir := range active {
		src := filepath.Join(cursorDir, filepath.FromSlash(dir))
		dest := filepath.Join(a.ramdisk, flattenPath(dir))
		orig := src + ".orig"

		if _, err := os.Stat(dest); errors.Is(err, fs.ErrNotExist) {
			a.logf("WARN: RAM disk dest %s missing -- RAM disk may be gone", dest)
			a.logf("      Restoring from .sync or .orig if available...")
			syncDir := src + ".sync"
			if _, statErr := os.Stat(syncDir); statErr == nil {
				// Prefer .sync — the most recent periodic backup.
				if err := a.remove(src); err != nil {
					return fmt.Errorf("remove dangling symlink %s: %w", src, err)
				}
				if err := a.rename(syncDir, src); err != nil {
					return fmt.Errorf("restore sync %s -> %s: %w", syncDir, src, err)
				}
				if err := a.removeAll(orig); err != nil {
					a.logf("WARN: could not remove .orig %s: %v", orig, err)
				}
				a.logf("  Restored from .sync: %s", dir)
			} else if _, statErr2 := os.Stat(orig); statErr2 == nil {
				// Fall back to .orig cold backup.
				if err := a.remove(src); err != nil {
					return fmt.Errorf("remove dangling symlink %s: %w", src, err)
				}
				if err := a.rename(orig, src); err != nil {
					return fmt.Errorf("restore orig %s -> %s: %w", orig, src, err)
				}
				a.logf("  Restored from .orig: %s", dir)
			} else {
				a.logf("  ERROR: no .sync or .orig found -- %s is unrecoverable", dir)
			}
			continue
		}

		// Rsync live RAM disk state -> src+".new" first so the operation is
		// atomic: if rsync fails mid-way, the existing symlink is untouched.
		newDir := src + ".new"
		a.logf("Syncing %s -> %s ...", dest, newDir)
		if err := a.syncDir(dest, newDir); err != nil {
			return fmt.Errorf("sync %s -> %s: %w", dest, newDir, err)
		}

		a.logf("Replacing symlink with synced directory ...")
		if err := a.remove(src); err != nil {
			return fmt.Errorf("remove symlink %s: %w", src, err)
		}
		if err := a.rename(newDir, src); err != nil {
			return fmt.Errorf("rename %s -> %s: %w", newDir, src, err)
		}

		// .orig and any .sync backup are now redundant -- the on-disk directory is current.
		if err := a.removeAll(orig); err != nil {
			a.logf("WARN: could not remove .orig %s: %v", orig, err)
		}
		syncDir := src + ".sync"
		if err := a.removeAll(syncDir); err != nil {
			a.logf("WARN: could not remove .sync %s: %v", syncDir, err)
		}

		a.logf("Done: %s", dir)
	}

	fmt.Fprintln(a.stdout)
	a.logf("Teardown complete. Cursor state is back on disk.")

	// Eject the RAM disk if it is still mounted.
	if _, err := os.Stat(a.ramdisk); err == nil {
		a.logf("Ejecting RAM disk at %s ...", a.ramdisk)
		if err := a.run("hdiutil", "detach", a.ramdisk); err != nil {
			a.logf("WARN: could not eject RAM disk: %v", err)
			a.logf("You can eject it manually: hdiutil detach %s", a.ramdisk)
		} else {
			a.logf("RAM disk ejected.")
		}
	}
	return nil
}

func (a *app) cmdStatus() error {
	cursorDir, err := a.cursorAppSupportDir()
	if err != nil {
		return err
	}
	return a.printStatus(cursorDir)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (a *app) logf(format string, args ...any) {
	ts := time.Now().Format("15:04:05")
	fmt.Fprintf(a.stdout, "[%s] %s\n", ts, fmt.Sprintf(format, args...))
}

// confirm prints a y/N prompt to stderr and reads a line from stdin.
// It returns true only if the user types "y" or "yes" (case-insensitive).
func (a *app) confirm(prompt string) bool {
	fmt.Fprintf(a.stderr, "%s [y/N] ", prompt)
	scanner := bufio.NewScanner(a.stdin)
	if !scanner.Scan() {
		return false
	}
	answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
	return answer == "y" || answer == "yes"
}

func (a *app) cursorAppSupportDir() (string, error) {
	if a.cursorDir != "" {
		return a.cursorDir, nil
	}
	homeFn := a.userHome
	if homeFn == nil {
		homeFn = os.UserHomeDir
	}
	home, err := homeFn()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, "Library", "Application Support", "Cursor"), nil
}

// flattenPath replaces path separators with underscores so subdirectories can
// be stored as a flat name on the RAM disk root.
// e.g. "User/globalStorage" -> "User_globalStorage"
func flattenPath(dir string) string {
	return strings.ReplaceAll(dir, "/", "_")
}

func (a *app) guardCursorNotRunning() error {
	_, err := a.runOutput("pgrep", "-x", "Cursor")
	if err == nil {
		return errors.New("Cursor is still running -- quit it first")
	}
	a.logf("Cursor is not running. Proceeding.")
	return nil
}

// pendingDirs returns the subset of dirs that are not yet symlinks,
// i.e. still need to be moved to the RAM disk.
func (a *app) pendingDirs(cursorDir string) []string {
	var pending []string
	for _, dir := range a.dirs {
		src := filepath.Join(cursorDir, filepath.FromSlash(dir))
		info, err := os.Lstat(src)
		if err != nil {
			continue
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			continue
		}
		pending = append(pending, dir)
	}
	return pending
}

// activeDirs returns the subset of dirs that are currently symlinks
// (i.e. on the RAM disk), used by teardown to know what to sync back.
func (a *app) activeDirs(cursorDir string) []string {
	var active []string
	for _, dir := range a.dirs {
		src := filepath.Join(cursorDir, filepath.FromSlash(dir))
		info, err := os.Lstat(src)
		if err != nil {
			continue
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			active = append(active, dir)
		}
	}
	return active
}

// measureDirs returns the total size in MB of each dir in dirs, and a per-dir
// map. It uses `du -sm` (megabytes, one line per dir) for speed.
func (a *app) measureDirs(cursorDir string, dirs []string) (totalMB int, sizes map[string]int, err error) {
	sizes = make(map[string]int, len(dirs))
	for _, dir := range dirs {
		src := filepath.Join(cursorDir, filepath.FromSlash(dir))
		mb, e := a.dirSizeMB(src)
		if e != nil {
			return 0, nil, fmt.Errorf("measure %s: %w", dir, e)
		}
		sizes[dir] = mb
		totalMB += mb
	}
	return totalMB, sizes, nil
}

// dirSizeMB returns the disk usage of path in whole megabytes using `du -sm`.
func (a *app) dirSizeMB(path string) (int, error) {
	out, err := a.runOutput("du", "-sm", path)
	if err != nil {
		return 0, fmt.Errorf("du -sm %s: %w", path, err)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return 0, fmt.Errorf("du -sm %s: empty output", path)
	}
	var mb int
	if _, err := fmt.Sscan(fields[0], &mb); err != nil {
		return 0, fmt.Errorf("du -sm %s: parse %q: %w", path, fields[0], err)
	}
	return mb, nil
}

// physRAMTotalMB returns the total physical RAM in megabytes via sysctl hw.memsize.
// This is used instead of vm.page_free_count because macOS aggressively holds
// pages as inactive/purgeable/speculative — the kernel "free" count can be a
// tiny fraction of what's actually available for allocation.
func (a *app) physRAMTotalMB() (int, error) {
	out, err := a.runOutput("sysctl", "-n", "hw.memsize")
	if err != nil {
		return 0, fmt.Errorf("sysctl hw.memsize: %w", err)
	}
	var memBytes int64
	if _, err := fmt.Sscan(strings.TrimSpace(string(out)), &memBytes); err != nil {
		return 0, fmt.Errorf("parse hw.memsize %q: %w", strings.TrimSpace(string(out)), err)
	}
	return int(memBytes / (1024 * 1024)), nil
}

// ensureRamdisk creates a RAM disk of sizeMB at a.ramdisk if one is not
// already there. If one is already mounted but smaller than sizeMB, it
// returns an error asking the user to teardown first. It checks physical
// RAM availability before creating a new disk.
func (a *app) ensureRamdisk(sizeMB int) error {
	if info, err := os.Stat(a.ramdisk); err == nil && info.IsDir() {
		// Check whether the existing RAM disk is large enough.
		currentMB, dfErr := a.ramdiskSizeMB()
		if dfErr != nil {
			a.logf("WARN: could not determine current RAM disk size: %v", dfErr)
			a.logf("RAM disk already mounted at %s, proceeding anyway.", a.ramdisk)
			return nil
		}
		if currentMB < sizeMB {
			return fmt.Errorf(
				"RAM disk at %s is %d MB but %d MB is needed -- run teardown first, then re-run setup",
				a.ramdisk, currentMB, sizeMB)
		}
		a.logf("RAM disk already mounted at %s (%d MB >= %d MB needed), skipping creation.", a.ramdisk, currentMB, sizeMB)
		return nil
	}

	totalMB, err := a.physRAMTotalMB()
	if err != nil {
		return fmt.Errorf("check available RAM: %w", err)
	}
	// Reserve 8 GB or 25% of total RAM (whichever is smaller) for the OS
	// and other applications. The rest can safely be used for the RAM disk
	// since macOS will reclaim inactive/purgeable/speculative pages as needed.
	reserveMB := totalMB / 4
	if reserveMB > 8192 {
		reserveMB = 8192
	}
	maxMB := totalMB - reserveMB
	a.logf("Physical RAM: %d MB total, %d MB reserve, %d MB available for RAM disk, %d MB requested",
		totalMB, reserveMB, maxMB, sizeMB)
	if sizeMB > maxMB {
		return fmt.Errorf(
			"RAM disk too large: need %d MB but only %d MB available "+
				"(%d MB total - %d MB reserve) -- reduce headroom or close other applications",
			sizeMB, maxMB, totalMB, reserveMB,
		)
	}

	sectors := sizeMB * 2048
	a.logf("Creating %d MB RAM disk...", sizeMB)

	attachOut, err := a.runOutput("hdiutil", "attach", "-nomount",
		fmt.Sprintf("ram://%d", sectors))
	if err != nil {
		return fmt.Errorf("hdiutil attach: %w", err)
	}

	dev := strings.TrimSpace(string(attachOut))
	a.logf("RAM disk device: %s", dev)

	if err := a.run("diskutil", "eraseDisk", "APFS", a.volName, dev); err != nil {
		return fmt.Errorf("diskutil eraseDisk: %w", err)
	}

	a.logf("RAM disk mounted at %s", a.ramdisk)
	return nil
}

// ramdiskSizeMB returns the total size of the mounted RAM disk in MB using df.
func (a *app) ramdiskSizeMB() (int, error) {
	// df -m outputs size in 1 MB blocks; the second field of the matching line is total size.
	out, err := a.runOutput("df", "-m", a.ramdisk)
	if err != nil {
		return 0, fmt.Errorf("df -m %s: %w", a.ramdisk, err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return 0, fmt.Errorf("df -m %s: unexpected output", a.ramdisk)
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 2 {
		return 0, fmt.Errorf("df -m %s: unexpected fields in %q", a.ramdisk, lines[1])
	}
	var mb int
	if _, err := fmt.Sscan(fields[1], &mb); err != nil {
		return 0, fmt.Errorf("df -m %s: parse %q: %w", a.ramdisk, fields[1], err)
	}
	return mb, nil
}

func symlinkTargetExists(link string) bool {
	target, err := os.Readlink(link)
	if err != nil {
		return false
	}
	_, err = os.Stat(target)
	return err == nil
}

func (a *app) printStatus(cursorDir string) error {
	for _, dir := range a.dirs {
		src := filepath.Join(cursorDir, filepath.FromSlash(dir))
		info, err := os.Lstat(src)
		if errors.Is(err, fs.ErrNotExist) {
			a.logf("  MISS  %s (not found)", dir)
			continue
		}
		if err != nil {
			return fmt.Errorf("stat %s: %w", src, err)
		}

		if info.Mode()&fs.ModeSymlink != 0 {
			target, _ := os.Readlink(src)
			valid := symlinkTargetExists(src)
			size := a.dirSize(target)
			if valid {
				a.logf("  RAM   %s -> %s (%s)", dir, target, size)
			} else {
				a.logf("  DANG  %s -> %s (DANGLING -- RAM disk gone, run teardown)", dir, target)
			}
		} else {
			a.logf("  DISK  %s (%s)", dir, a.dirSize(src))
		}
	}
	return nil
}

func (a *app) dirSize(path string) string {
	out, err := a.runOutput("du", "-sh", path)
	if err != nil {
		return "?"
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "?"
	}
	if fields[0] == "0" {
		entries, readErr := os.ReadDir(path)
		if readErr == nil && len(entries) == 0 {
			return "empty"
		}
	}
	return fields[0]
}

// syncDir uses rsync to update dest from src, preserving all metadata.
// Only changed files are written, making it safe for incremental syncs.
func (a *app) syncDir(src, dest string) error {
	srcSlash := strings.TrimRight(src, "/") + "/"
	if err := a.run("rsync", "-a", "--delete", srcSlash, dest); err != nil {
		return fmt.Errorf("rsync %s -> %s: %w", src, dest, err)
	}
	return nil
}

// cmdSync rsyncs each active RAM disk directory to a <dir>.sync sibling on disk.
// It is a no-op (returns nil) if the RAM disk is not mounted or Cursor is not
// running, so it is safe to call from a launchd timer at any time.
func (a *app) cmdSync() error {
	cursorDir, err := a.cursorAppSupportDir()
	if err != nil {
		return err
	}

	// RAM disk not mounted — nothing to sync.
	if _, err := os.Stat(a.ramdisk); errors.Is(err, fs.ErrNotExist) {
		a.logf("RAM disk not mounted at %s -- skipping sync.", a.ramdisk)
		return nil
	}

	// Cursor not running — data is not changing, skip to stay quiet.
	if _, err := a.runOutput("pgrep", "-x", "Cursor"); err != nil {
		a.logf("Cursor is not running -- skipping sync.")
		return nil
	}

	active := a.activeDirs(cursorDir)
	if len(active) == 0 {
		a.logf("No directories are on the RAM disk. Nothing to sync.")
		return nil
	}

	for _, dir := range active {
		ramDir := filepath.Join(a.ramdisk, flattenPath(dir))
		syncDest := filepath.Join(cursorDir, filepath.FromSlash(dir)) + ".sync"
		a.logf("Syncing %s -> %s ...", ramDir, syncDest)
		if err := a.syncDir(ramDir, syncDest); err != nil {
			return fmt.Errorf("sync %s -> %s: %w", ramDir, syncDest, err)
		}
	}

	a.logf("Sync complete.")
	return nil
}

// launchAgentPlistPath returns the path to the launchd plist for the sync agent:
// ~/Library/LaunchAgents/com.cursor-ramdisk.sync.plist
func (a *app) launchAgentPlistPath() (string, error) {
	homeFn := a.userHome
	if homeFn == nil {
		homeFn = os.UserHomeDir
	}
	home, err := homeFn()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", "com.cursor-ramdisk.sync.plist"), nil
}

// cmdInstallSync writes a launchd plist that runs `cursor-ramdisk -y sync`
// every 5 minutes and bootstraps it into the current GUI session.
func (a *app) cmdInstallSync() error {
	execFn := a.executable
	if execFn == nil {
		execFn = os.Executable
	}
	exePath, err := execFn()
	if err != nil {
		return fmt.Errorf("executable path: %w", err)
	}

	plistPath, err := a.launchAgentPlistPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return fmt.Errorf("mkdir LaunchAgents: %w", err)
	}

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.cursor-ramdisk.sync</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>-y</string>
		<string>sync</string>
	</array>
	<key>StartInterval</key>
	<integer>300</integer>
	<key>RunAtLoad</key>
	<false/>
	<key>StandardOutPath</key>
	<string>/tmp/cursor-ramdisk-sync.log</string>
	<key>StandardErrorPath</key>
	<string>/tmp/cursor-ramdisk-sync.log</string>
</dict>
</plist>
`, exePath)

	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return fmt.Errorf("write plist %s: %w", plistPath, err)
	}
	a.logf("Wrote %s", plistPath)

	target := fmt.Sprintf("gui/%d", os.Getuid())
	if err := a.run("launchctl", "bootstrap", target, plistPath); err != nil {
		return fmt.Errorf("launchctl bootstrap %s %s: %w", target, plistPath, err)
	}

	a.logf("Sync agent installed. Runs every 5 minutes while Cursor is running.")
	return nil
}

// cmdUninstallSync bootouts the sync launchd agent and removes its plist.
// A launchctl failure is treated as a warning (the agent may not have been
// loaded), but the plist is always removed so the agent won't reload on login.
func (a *app) cmdUninstallSync() error {
	plistPath, err := a.launchAgentPlistPath()
	if err != nil {
		return err
	}

	target := fmt.Sprintf("gui/%d", os.Getuid())
	if err := a.run("launchctl", "bootout", target, plistPath); err != nil {
		a.logf("WARN: launchctl bootout failed (agent may not be loaded): %v", err)
	}

	if err := a.remove(plistPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove plist %s: %w", plistPath, err)
	}

	a.logf("Sync agent uninstalled.")
	return nil
}

// copyDir recursively copies src to dest, preserving permissions and symlinks.
// It shells out to `cp -a` which handles all macOS-specific metadata correctly
// (xattrs, resource forks, etc.) and is substantially faster than a pure-Go
// walk for multi-gigabyte directories.
func (a *app) copyDir(src, dest string) error {
	parentDir := filepath.Dir(dest)
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", parentDir, err)
	}

	// Remove dest if it already exists so cp -a doesn't nest inside it.
	if _, err := os.Lstat(dest); err == nil {
		if err := os.RemoveAll(dest); err != nil {
			return fmt.Errorf("remove existing dest %s: %w", dest, err)
		}
	}

	if err := a.run("cp", "-a", src, dest); err != nil {
		return fmt.Errorf("cp -a %s %s: %w", src, dest, err)
	}
	return nil
}
