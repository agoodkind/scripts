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
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	ramdiskMount    = "/Volumes/CursorRAM"
	ramdiskVolName  = "CursorRAM"
	ramdiskHeadroom = 1024 // MB of headroom added on top of measured dir sizes
)

var targetDirs = []string{
	"User/globalStorage",
	"User/workspaceStorage",
	"User/History",
	"Cache",
}

func main() {
	args := os.Args[1:]

	// Parse -y flag anywhere in args.
	yes := false
	filtered := args[:0]
	for _, a := range args {
		if a == "-y" {
			yes = true
		} else {
			filtered = append(filtered, a)
		}
	}
	args = filtered

	if len(args) < 1 {
		usage()
		os.Exit(1)
	}

	var err error
	switch args[0] {
	case "setup":
		err = cmdSetup(yes)
	case "teardown":
		err = cmdTeardown(yes)
	case "status":
		err = cmdStatus()
	default:
		usage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `cursor-ramdisk: manage Cursor IDE state on a RAM disk

Usage:
  cursor-ramdisk [-y] setup     move hot dirs to RAM disk and symlink back (idempotent)
  cursor-ramdisk [-y] teardown  rsync RAM disk state back to disk, remove symlinks
  cursor-ramdisk status         show current state of each target directory

Flags:
  -y  non-interactive: skip all confirmation prompts

Cursor must not be running for setup or teardown.
`)
}

// ---------------------------------------------------------------------------
// Commands
// ---------------------------------------------------------------------------

// cmdSetup is idempotent: it skips any directory that is already a symlink
// pointing at the RAM disk. On first run it takes an APFS snapshot, measures
// the actual disk usage of pending directories, creates a right-sized RAM disk
// (measured total + ramdiskHeadroom MB), copies each target directory onto it,
// saves the original as <dir>.orig (never touched again), and replaces the
// original path with a symlink.
//
// .orig is a one-time cold backup taken at setup time. All durable state is
// managed by teardown (which rsyncs the live RAM disk back to disk).
func cmdSetup(yes bool) error {
	cursorDir, err := cursorAppSupportDir()
	if err != nil {
		return err
	}

	if err := guardCursorNotRunning(); err != nil {
		return err
	}

	pending := pendingDirs(cursorDir)
	if len(pending) == 0 {
		logf("All directories already on RAM disk. Nothing to do.")
		return cmdStatus()
	}

	// Measure actual sizes so we can right-size the RAM disk.
	totalMB, sizes, err := measureDirs(cursorDir, pending)
	if err != nil {
		return err
	}
	ramdiskSizeMB := totalMB + ramdiskHeadroom

	logf("Directories to move:")
	for _, dir := range pending {
		logf("  %-30s  %d MB", dir, sizes[dir])
	}
	logf("Total: %d MB  +  %d MB headroom  =  %d MB RAM disk", totalMB, ramdiskHeadroom, ramdiskSizeMB)

	if !yes {
		if !confirm("Proceed with setup?") {
			logf("Aborted.")
			return nil
		}
	}

	logf("Creating local APFS Time Machine snapshot...")
	if err := run("tmutil", "localsnapshot"); err != nil {
		return fmt.Errorf("tmutil localsnapshot: %w", err)
	}
	logf("Snapshot created.")

	if err := ensureRamdisk(ramdiskSizeMB); err != nil {
		return err
	}

	for _, dir := range pending {
		src := filepath.Join(cursorDir, filepath.FromSlash(dir))
		dest := filepath.Join(ramdiskMount, flattenPath(dir))
		orig := src + ".orig"

		logf("Copying %s -> %s ...", dir, dest)
		if err := copyDir(src, dest); err != nil {
			return fmt.Errorf("copy %s -> %s: %w", src, dest, err)
		}

		logf("Saving original as %s ...", orig)
		if err := os.Rename(src, orig); err != nil {
			return fmt.Errorf("rename %s -> %s: %w", src, orig, err)
		}

		logf("Symlinking %s -> %s ...", src, dest)
		if err := os.Symlink(dest, src); err != nil {
			return fmt.Errorf("symlink %s -> %s: %w", dest, src, err)
		}

		logf("Done: %s", dir)
	}

	fmt.Println()
	return cmdStatus()
}

// cmdTeardown rsyncs the live RAM disk state back to disk, then replaces each
// symlink with the synced directory. The .orig cold backup is removed after a
// successful rsync since the on-disk directory is now current.
func cmdTeardown(yes bool) error {
	cursorDir, err := cursorAppSupportDir()
	if err != nil {
		return err
	}

	if err := guardCursorNotRunning(); err != nil {
		return err
	}

	// Show what will happen before doing anything destructive.
	active := activeDirs(cursorDir)
	if len(active) == 0 {
		logf("No directories are on the RAM disk. Nothing to do.")
		return cmdStatus()
	}

	logf("Directories to sync back to disk:")
	for _, dir := range active {
		src := filepath.Join(cursorDir, filepath.FromSlash(dir))
		dest := filepath.Join(ramdiskMount, flattenPath(dir))
		if _, err := os.Stat(dest); errors.Is(err, fs.ErrNotExist) {
			logf("  %-30s  (RAM disk gone -- will restore from .orig)", dir)
		} else {
			logf("  %-30s  %s", dir, dirSize(src))
		}
	}

	if !yes {
		if !confirm("Proceed with teardown?") {
			logf("Aborted.")
			return nil
		}
	}

	for _, dir := range active {
		src := filepath.Join(cursorDir, filepath.FromSlash(dir))
		dest := filepath.Join(ramdiskMount, flattenPath(dir))
		orig := src + ".orig"

		if _, err := os.Stat(dest); errors.Is(err, fs.ErrNotExist) {
			logf("WARN: RAM disk dest %s missing -- RAM disk may be gone", dest)
			logf("      Restoring from .orig if available...")
			if _, statErr := os.Stat(orig); statErr == nil {
				if err := os.Remove(src); err != nil {
					return fmt.Errorf("remove dangling symlink %s: %w", src, err)
				}
				if err := os.Rename(orig, src); err != nil {
					return fmt.Errorf("restore orig %s -> %s: %w", orig, src, err)
				}
				logf("  Restored from .orig: %s", dir)
			} else {
				logf("  ERROR: no .orig found either -- %s is unrecoverable", dir)
			}
			continue
		}

		// Rsync live RAM disk state -> src+".new" first so the operation is
		// atomic: if rsync fails mid-way, the existing symlink is untouched.
		newDir := src + ".new"
		logf("Syncing %s -> %s ...", dest, newDir)
		if err := syncDir(dest, newDir); err != nil {
			return fmt.Errorf("sync %s -> %s: %w", dest, newDir, err)
		}

		logf("Replacing symlink with synced directory ...")
		if err := os.Remove(src); err != nil {
			return fmt.Errorf("remove symlink %s: %w", src, err)
		}
		if err := os.Rename(newDir, src); err != nil {
			return fmt.Errorf("rename %s -> %s: %w", newDir, src, err)
		}

		// .orig is now redundant -- the on-disk directory is current.
		if err := os.RemoveAll(orig); err != nil {
			logf("WARN: could not remove .orig %s: %v", orig, err)
		}

		logf("Done: %s", dir)
	}

	fmt.Println()
	logf("Teardown complete. Cursor state is back on disk.")
	logf("You can now start Cursor or eject %s if it is still mounted.", ramdiskMount)
	return nil
}

func cmdStatus() error {
	cursorDir, err := cursorAppSupportDir()
	if err != nil {
		return err
	}
	return printStatus(cursorDir)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func logf(format string, args ...any) {
	ts := time.Now().Format("15:04:05")
	fmt.Printf("[%s] %s\n", ts, fmt.Sprintf(format, args...))
}

// confirm prints a y/N prompt to stderr and reads a line from stdin.
// It returns true only if the user types "y" or "yes" (case-insensitive).
func confirm(prompt string) bool {
	fmt.Fprintf(os.Stderr, "%s [y/N] ", prompt)
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return false
	}
	answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
	return answer == "y" || answer == "yes"
}

func cursorAppSupportDir() (string, error) {
	home, err := os.UserHomeDir()
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

func guardCursorNotRunning() error {
	cmd := exec.Command("pgrep", "-x", "Cursor")
	if err := cmd.Run(); err == nil {
		return errors.New("Cursor is still running -- quit it first")
	}
	logf("Cursor is not running. Proceeding.")
	return nil
}

// pendingDirs returns the subset of targetDirs that are not yet symlinks,
// i.e. still need to be moved to the RAM disk.
func pendingDirs(cursorDir string) []string {
	var pending []string
	for _, dir := range targetDirs {
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

// activeDirs returns the subset of targetDirs that are currently symlinks
// (i.e. on the RAM disk), used by teardown to know what to sync back.
func activeDirs(cursorDir string) []string {
	var active []string
	for _, dir := range targetDirs {
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
func measureDirs(cursorDir string, dirs []string) (totalMB int, sizes map[string]int, err error) {
	sizes = make(map[string]int, len(dirs))
	for _, dir := range dirs {
		src := filepath.Join(cursorDir, filepath.FromSlash(dir))
		mb, e := dirSizeMB(src)
		if e != nil {
			return 0, nil, fmt.Errorf("measure %s: %w", dir, e)
		}
		sizes[dir] = mb
		totalMB += mb
	}
	return totalMB, sizes, nil
}

// dirSizeMB returns the disk usage of path in whole megabytes using `du -sm`.
func dirSizeMB(path string) (int, error) {
	out, err := exec.Command("du", "-sm", path).Output()
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

// ensureRamdisk creates a RAM disk of sizeMB at ramdiskMount if one is not
// already there.
func ensureRamdisk(sizeMB int) error {
	if info, err := os.Stat(ramdiskMount); err == nil && info.IsDir() {
		logf("RAM disk already mounted at %s, skipping creation.", ramdiskMount)
		return nil
	}

	sectors := sizeMB * 2048
	logf("Creating %d MB RAM disk...", sizeMB)

	attachOut, err := exec.Command("hdiutil", "attach", "-nomount",
		fmt.Sprintf("ram://%d", sectors)).Output()
	if err != nil {
		return fmt.Errorf("hdiutil attach: %w", err)
	}

	dev := strings.TrimSpace(string(attachOut))
	logf("RAM disk device: %s", dev)

	if err := run("diskutil", "eraseDisk", "APFS", ramdiskVolName, dev); err != nil {
		return fmt.Errorf("diskutil eraseDisk: %w", err)
	}

	logf("RAM disk mounted at %s", ramdiskMount)
	return nil
}

func symlinkTargetExists(link string) bool {
	target, err := os.Readlink(link)
	if err != nil {
		return false
	}
	_, err = os.Stat(target)
	return err == nil
}

func printStatus(cursorDir string) error {
	for _, dir := range targetDirs {
		src := filepath.Join(cursorDir, filepath.FromSlash(dir))
		info, err := os.Lstat(src)
		if errors.Is(err, fs.ErrNotExist) {
			logf("  MISS  %s (not found)", dir)
			continue
		}
		if err != nil {
			return fmt.Errorf("stat %s: %w", src, err)
		}

		if info.Mode()&fs.ModeSymlink != 0 {
			target, _ := os.Readlink(src)
			valid := symlinkTargetExists(src)
			size := dirSize(src)
			if valid {
				logf("  RAM   %s -> %s (%s)", dir, target, size)
			} else {
				logf("  DANG  %s -> %s (DANGLING -- RAM disk gone, run teardown)", dir, target)
			}
		} else {
			logf("  DISK  %s (%s)", dir, dirSize(src))
		}
	}
	return nil
}

func dirSize(path string) string {
	out, err := exec.Command("du", "-sh", path).Output()
	if err != nil {
		return "?"
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "?"
	}
	return fields[0]
}

// run executes a command, streaming its stdout/stderr to the terminal.
func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// syncDir uses rsync to update dest from src, preserving all metadata.
// Only changed files are written, making it safe for incremental syncs.
func syncDir(src, dest string) error {
	srcSlash := strings.TrimRight(src, "/") + "/"
	cmd := exec.Command("rsync", "-a", "--delete", srcSlash, dest)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("rsync %s -> %s: %w", src, dest, err)
	}
	return nil
}

// copyDir recursively copies src to dest, preserving permissions and symlinks.
// It shells out to `cp -a` which handles all macOS-specific metadata correctly
// (xattrs, resource forks, etc.) and is substantially faster than a pure-Go
// walk for multi-gigabyte directories.
func copyDir(src, dest string) error {
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

	cmd := exec.Command("cp", "-a", src, dest)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cp -a %s %s: %w", src, dest, err)
	}
	return nil
}
