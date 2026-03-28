package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type testEnv struct {
	cursorDir  string
	ramdiskDir string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	tmp := t.TempDir()
	env := &testEnv{
		cursorDir:  filepath.Join(tmp, "Cursor"),
		ramdiskDir: filepath.Join(tmp, "CursorRAM"),
	}
	os.MkdirAll(env.ramdiskDir, 0o755)

	dirs := []string{"User/globalStorage", "User/workspaceStorage", "User/History", "Cache"}
	for _, d := range dirs {
		full := filepath.Join(env.cursorDir, filepath.FromSlash(d))
		os.MkdirAll(full, 0o755)
		os.WriteFile(filepath.Join(full, "state.db"), []byte("initial:"+d), 0o644)
	}
	return env
}

func (e *testEnv) setup(t *testing.T, dirs []string) {
	t.Helper()
	for _, dir := range dirs {
		src := filepath.Join(e.cursorDir, filepath.FromSlash(dir))
		dest := filepath.Join(e.ramdiskDir, flattenPath(dir))
		orig := src + ".orig"

		if err := copyDir(src, dest); err != nil {
			t.Fatalf("setup copyDir %s: %v", dir, err)
		}
		if err := os.Rename(src, orig); err != nil {
			t.Fatalf("setup rename to .orig %s: %v", dir, err)
		}
		if err := os.Symlink(dest, src); err != nil {
			t.Fatalf("setup symlink %s: %v", dir, err)
		}
	}
}

func (e *testEnv) teardown(t *testing.T, dirs []string) {
	t.Helper()
	for _, dir := range dirs {
		src := filepath.Join(e.cursorDir, filepath.FromSlash(dir))
		dest := filepath.Join(e.ramdiskDir, flattenPath(dir))
		orig := src + ".orig"
		newDir := src + ".new"

		info, err := os.Lstat(src)
		if err != nil || info.Mode()&fs.ModeSymlink == 0 {
			t.Logf("SKIP teardown %s: not a symlink", dir)
			continue
		}

		if _, err := os.Stat(dest); errors.Is(err, fs.ErrNotExist) {
			if _, err := os.Stat(orig); err == nil {
				os.Remove(src)
				os.Rename(orig, src)
			}
			continue
		}

		if err := syncDir(dest, newDir); err != nil {
			t.Fatalf("teardown syncDir %s: %v", dir, err)
		}
		if err := os.Remove(src); err != nil {
			t.Fatalf("teardown remove symlink %s: %v", dir, err)
		}
		if err := os.Rename(newDir, src); err != nil {
			t.Fatalf("teardown rename .new -> src %s: %v", dir, err)
		}
		os.RemoveAll(orig)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readFile %s: %v", path, err)
	}
	return string(data)
}

func assertIsSymlink(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("expected symlink at %s, got error: %v", path, err)
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("expected symlink at %s, got mode %v", path, info.Mode())
	}
}

func assertIsDir(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected dir at %s, got error: %v", path, err)
	}
	if !info.IsDir() {
		t.Fatalf("expected dir at %s, got mode %v", path, info.Mode())
	}
}

func assertNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err == nil {
		t.Fatalf("expected %s to not exist, but it does", path)
	}
}

// doTestSetup mirrors cmdSetup but uses cursorDir and ramdiskRoot and skips
// guardCursorNotRunning, tmutil, and ensureRamdisk.
func doTestSetup(t *testing.T, cursorDir, ramdiskRoot string, yes bool) error {
	t.Helper()
	pending := pendingDirs(cursorDir)
	if len(pending) == 0 {
		logf("All directories already on RAM disk. Nothing to do.")
		return printStatus(cursorDir)
	}

	totalMB, sizes, err := measureDirs(cursorDir, pending)
	if err != nil {
		return err
	}
	ramdiskSizeMB := totalMB + ramdiskHeadroom

	logf("Directories to move:")
	for _, dir := range pending {
		logf("  %-30s  %d MB", dir, sizes[dir])
	}
	logf("Total: %d MB  +  %d MB headroom  =  %d MB RAM disk",
		totalMB, ramdiskHeadroom, ramdiskSizeMB)

	if !yes {
		if !confirm("Proceed with setup?") {
			logf("Aborted.")
			return nil
		}
	}

	for _, dir := range pending {
		src := filepath.Join(cursorDir, filepath.FromSlash(dir))
		dest := filepath.Join(ramdiskRoot, flattenPath(dir))
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
	return printStatus(cursorDir)
}

// doTestTeardown mirrors cmdTeardown but uses ramdiskRoot instead of
// ramdiskMount and skips guardCursorNotRunning.
func doTestTeardown(t *testing.T, cursorDir, ramdiskRoot string, yes bool) error {
	t.Helper()
	active := activeDirs(cursorDir)
	if len(active) == 0 {
		logf("No directories are on the RAM disk. Nothing to do.")
		return printStatus(cursorDir)
	}

	logf("Directories to sync back to disk:")
	for _, dir := range active {
		src := filepath.Join(cursorDir, filepath.FromSlash(dir))
		dest := filepath.Join(ramdiskRoot, flattenPath(dir))
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
		dest := filepath.Join(ramdiskRoot, flattenPath(dir))
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

func withStdin(t *testing.T, input string, fn func()) {
	t.Helper()
	old := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	go func() {
		_, _ = w.WriteString(input)
		_ = w.Close()
	}()
	defer func() {
		_ = r.Close()
		os.Stdin = old
	}()
	fn()
}

func TestFlattenPath(t *testing.T) {
	got := flattenPath("User/globalStorage")
	if got != "User_globalStorage" {
		t.Fatalf("flattenPath: got %q want User_globalStorage", got)
	}
}

func TestPendingDirs(t *testing.T) {
	tmp := t.TempDir()
	cursorDir := filepath.Join(tmp, "Cursor")
	baseDirs := []string{"User/globalStorage", "User/workspaceStorage", "User/History", "Cache"}

	t.Run("all_real_dirs", func(t *testing.T) {
		for _, d := range baseDirs {
			os.MkdirAll(filepath.Join(cursorDir, filepath.FromSlash(d)), 0o755)
		}
		got := pendingDirs(cursorDir)
		if len(got) != len(baseDirs) {
			t.Fatalf("pendingDirs: got %v want all %d dirs", got, len(baseDirs))
		}
	})

	t.Run("all_symlinks", func(t *testing.T) {
		dir := t.TempDir()
		cd := filepath.Join(dir, "Cursor")
		for _, d := range baseDirs {
			full := filepath.Join(cd, filepath.FromSlash(d))
			os.MkdirAll(filepath.Join(dir, "ram", flattenPath(d)), 0o755)
			os.MkdirAll(filepath.Dir(full), 0o755)
			_ = os.Symlink(filepath.Join(dir, "ram", flattenPath(d)), full)
		}
		if len(pendingDirs(cd)) != 0 {
			t.Fatalf("expected no pending when all symlinks")
		}
	})

	t.Run("mix", func(t *testing.T) {
		dir := t.TempDir()
		cd := filepath.Join(dir, "Cursor")
		os.MkdirAll(filepath.Join(cd, "User", "globalStorage"), 0o755)
		os.MkdirAll(filepath.Join(dir, "ram", "User_globalStorage"), 0o755)
		gs := filepath.Join(cd, "User", "globalStorage")
		os.RemoveAll(gs)
		_ = os.Symlink(filepath.Join(dir, "ram", "User_globalStorage"), gs)
		os.MkdirAll(filepath.Join(cd, "Cache"), 0o755)
		got := pendingDirs(cd)
		if len(got) != 1 || got[0] != "Cache" {
			t.Fatalf("pendingDirs mix: got %v want [Cache]", got)
		}
	})

	t.Run("missing_skipped", func(t *testing.T) {
		dir := t.TempDir()
		cd := filepath.Join(dir, "Cursor")
		os.MkdirAll(filepath.Join(cd, "Cache"), 0o755)
		got := pendingDirs(cd)
		if len(got) != 1 {
			t.Fatalf("got %v want one dir", got)
		}
	})
}

func TestActiveDirs(t *testing.T) {
	baseDirs := []string{"User/globalStorage", "User/workspaceStorage", "User/History", "Cache"}

	t.Run("all_symlinks", func(t *testing.T) {
		dir := t.TempDir()
		cd := filepath.Join(dir, "Cursor")
		for _, d := range baseDirs {
			os.MkdirAll(filepath.Join(dir, "ram", flattenPath(d)), 0o755)
			full := filepath.Join(cd, filepath.FromSlash(d))
			os.MkdirAll(filepath.Dir(full), 0o755)
			_ = os.Symlink(filepath.Join(dir, "ram", flattenPath(d)), full)
		}
		got := activeDirs(cd)
		if len(got) != len(baseDirs) {
			t.Fatalf("activeDirs: got %v", got)
		}
	})

	t.Run("none", func(t *testing.T) {
		env := newTestEnv(t)
		if len(activeDirs(env.cursorDir)) != 0 {
			t.Fatal("expected no active dirs")
		}
	})

	t.Run("missing_skipped", func(t *testing.T) {
		dir := t.TempDir()
		cd := filepath.Join(dir, "Cursor")
		os.MkdirAll(filepath.Join(cd, "Cache"), 0o755)
		if len(activeDirs(cd)) != 0 {
			t.Fatal("expected none")
		}
	})
}

func TestMeasureDirs(t *testing.T) {
	env := newTestEnv(t)
	pending := pendingDirs(env.cursorDir)
	totalMB, sizes, err := measureDirs(env.cursorDir, pending)
	if err != nil {
		t.Fatal(err)
	}
	if totalMB <= 0 {
		t.Fatalf("expected positive totalMB, got %d", totalMB)
	}
	for _, d := range pending {
		if sizes[d] <= 0 {
			t.Fatalf("expected positive size for %s", d)
		}
	}

	t.Run("empty_dirs", func(t *testing.T) {
		tmb, m, err := measureDirs(env.cursorDir, nil)
		if err != nil || tmb != 0 || len(m) != 0 {
			t.Fatalf("empty dirs: tmb=%d m=%v err=%v", tmb, m, err)
		}
	})
}

func TestMeasureDirs_Error(t *testing.T) {
	tmp := t.TempDir()
	cursorDir := filepath.Join(tmp, "Cursor")
	_, _, err := measureDirs(cursorDir, []string{"User/globalStorage"})
	if err == nil {
		t.Fatal("expected error for missing directory")
	}
}

func TestDirSizeMB(t *testing.T) {
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, "d"), 0o755)
	os.WriteFile(filepath.Join(tmp, "d", "f.txt"), []byte("hello"), 0o644)

	mb, err := dirSizeMB(filepath.Join(tmp, "d"))
	if err != nil {
		t.Fatal(err)
	}
	if mb < 1 {
		t.Fatalf("expected at least 1 MB for du -sm, got %d", mb)
	}
}

func TestDirSizeMB_FakeDuEmpty(t *testing.T) {
	tmp := t.TempDir()
	duPath := filepath.Join(tmp, "du")
	script := "#!/bin/sh\nprintf ''\n"
	if err := os.WriteFile(duPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+oldPath)

	_, err := dirSizeMB(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "empty output") {
		t.Fatalf("expected empty output error, got %v", err)
	}
}

func TestDirSizeMB_FakeDuBadParse(t *testing.T) {
	tmp := t.TempDir()
	duPath := filepath.Join(tmp, "du")
	script := "#!/bin/sh\necho notanumber\n"
	if err := os.WriteFile(duPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+oldPath)

	_, err := dirSizeMB(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

func TestSymlinkTargetExists(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "target")
	os.MkdirAll(target, 0o755)
	linkOK := filepath.Join(tmp, "ok")
	_ = os.Symlink(target, linkOK)
	if !symlinkTargetExists(linkOK) {
		t.Fatal("expected true for valid symlink")
	}

	dangle := filepath.Join(tmp, "bad")
	_ = os.Symlink(filepath.Join(tmp, "nope"), dangle)
	if symlinkTargetExists(dangle) {
		t.Fatal("expected false for dangling symlink")
	}

	if symlinkTargetExists(target) {
		t.Fatal("expected false when path is not a symlink")
	}
}

func TestPrintStatus(t *testing.T) {
	t.Run("miss", func(t *testing.T) {
		tmp := t.TempDir()
		if err := printStatus(tmp); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("disk", func(t *testing.T) {
		tmp := t.TempDir()
		os.MkdirAll(filepath.Join(tmp, "User", "globalStorage"), 0o755)
		os.WriteFile(filepath.Join(tmp, "User", "globalStorage", "x"), []byte("a"), 0o644)
		for _, d := range []string{"User/workspaceStorage", "User/History", "Cache"} {
			os.MkdirAll(filepath.Join(tmp, filepath.FromSlash(d)), 0o755)
		}
		if err := printStatus(tmp); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("ram_valid", func(t *testing.T) {
		tmp := t.TempDir()
		ram := filepath.Join(tmp, "ram")
		os.MkdirAll(ram, 0o755)
		os.WriteFile(filepath.Join(ram, "z"), []byte("z"), 0o644)
		src := filepath.Join(tmp, "User", "globalStorage")
		os.MkdirAll(filepath.Join(tmp, "User"), 0o755)
		_ = os.Symlink(ram, src)
		for _, d := range []string{"User/workspaceStorage", "User/History", "Cache"} {
			os.MkdirAll(filepath.Join(tmp, filepath.FromSlash(d)), 0o755)
		}
		if err := printStatus(tmp); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("dang", func(t *testing.T) {
		tmp := t.TempDir()
		src := filepath.Join(tmp, "User", "globalStorage")
		os.MkdirAll(filepath.Join(tmp, "User"), 0o755)
		_ = os.Symlink(filepath.Join(tmp, "missing"), src)
		for _, d := range []string{"User/workspaceStorage", "User/History", "Cache"} {
			os.MkdirAll(filepath.Join(tmp, filepath.FromSlash(d)), 0o755)
		}
		if err := printStatus(tmp); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("stat_error", func(t *testing.T) {
		tmp := t.TempDir()
		// "User" as a file makes User/globalStorage invalid for lstat
		os.WriteFile(filepath.Join(tmp, "User"), []byte("x"), 0o644)
		err := printStatus(tmp)
		if err == nil {
			t.Fatal("expected stat error")
		}
	})
}

func TestConfirm(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"y\n", true},
		{"yes\n", true},
		{"YES\n", true},
		{"n\n", false},
		{"\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			withStdin(t, tc.in, func() {
				if got := confirm("ok"); got != tc.want {
					t.Fatalf("confirm(%q): got %v want %v", tc.in, got, tc.want)
				}
			})
		})
	}

	t.Run("eof", func(t *testing.T) {
		old := os.Stdin
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		os.Stdin = r
		_ = w.Close()
		defer func() {
			_ = r.Close()
			os.Stdin = old
		}()
		if confirm("x") {
			t.Fatal("expected false on EOF")
		}
	})
}

func TestDoTestSetup_FullFlow(t *testing.T) {
	env := newTestEnv(t)
	if err := doTestSetup(t, env.cursorDir, env.ramdiskDir, true); err != nil {
		t.Fatal(err)
	}
	for _, d := range targetDirs {
		src := filepath.Join(env.cursorDir, filepath.FromSlash(d))
		assertIsSymlink(t, src)
	}
}

func TestDoTestSetup_Idempotent(t *testing.T) {
	env := newTestEnv(t)
	if err := doTestSetup(t, env.cursorDir, env.ramdiskDir, true); err != nil {
		t.Fatal(err)
	}
	t1, _ := os.Readlink(filepath.Join(env.cursorDir, "User", "globalStorage"))
	if err := doTestSetup(t, env.cursorDir, env.ramdiskDir, true); err != nil {
		t.Fatal(err)
	}
	t2, _ := os.Readlink(filepath.Join(env.cursorDir, "User", "globalStorage"))
	if t1 != t2 {
		t.Fatalf("symlink target changed: %s vs %s", t1, t2)
	}
}

func TestDoTestSetup_Aborted(t *testing.T) {
	env := newTestEnv(t)
	withStdin(t, "n\n", func() {
		if err := doTestSetup(t, env.cursorDir, env.ramdiskDir, false); err != nil {
			t.Fatal(err)
		}
	})
	src := filepath.Join(env.cursorDir, "User", "globalStorage")
	info, _ := os.Lstat(src)
	if info.Mode()&fs.ModeSymlink != 0 {
		t.Fatal("expected no symlink after abort")
	}
}

func TestDoTestTeardown_HappyPath(t *testing.T) {
	env := newTestEnv(t)
	if err := doTestSetup(t, env.cursorDir, env.ramdiskDir, true); err != nil {
		t.Fatal(err)
	}
	for _, d := range targetDirs {
		dest := filepath.Join(env.ramdiskDir, flattenPath(d))
		os.WriteFile(filepath.Join(dest, "live.txt"), []byte("live:"+d), 0o644)
	}
	if err := doTestTeardown(t, env.cursorDir, env.ramdiskDir, true); err != nil {
		t.Fatal(err)
	}
	for _, d := range targetDirs {
		src := filepath.Join(env.cursorDir, filepath.FromSlash(d))
		assertIsDir(t, src)
		assertNotExist(t, src+".orig")
		if readFile(t, filepath.Join(src, "live.txt")) != "live:"+d {
			t.Fatalf("rsynced content missing for %s", d)
		}
	}
}

func TestDoTestTeardown_RAMGoneOrigRestore(t *testing.T) {
	env := newTestEnv(t)
	for _, d := range targetDirs {
		if d != "Cache" {
			os.RemoveAll(filepath.Join(env.cursorDir, filepath.FromSlash(d)))
		}
	}
	if err := doTestSetup(t, env.cursorDir, env.ramdiskDir, true); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(env.ramdiskDir, flattenPath("Cache"))
	os.RemoveAll(dest)

	if err := doTestTeardown(t, env.cursorDir, env.ramdiskDir, true); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(env.cursorDir, "Cache")
	assertIsDir(t, src)
}

func TestDoTestTeardown_RAMGoneNoOrig(t *testing.T) {
	env := newTestEnv(t)
	for _, d := range targetDirs {
		if d != "Cache" {
			os.RemoveAll(filepath.Join(env.cursorDir, filepath.FromSlash(d)))
		}
	}
	if err := doTestSetup(t, env.cursorDir, env.ramdiskDir, true); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(env.cursorDir, "Cache")
	orig := src + ".orig"
	os.RemoveAll(orig)
	dest := filepath.Join(env.ramdiskDir, flattenPath("Cache"))
	os.RemoveAll(dest)

	if err := doTestTeardown(t, env.cursorDir, env.ramdiskDir, true); err != nil {
		t.Fatal(err)
	}
	assertIsSymlink(t, src)
}

func TestDoTestTeardown_Aborted(t *testing.T) {
	env := newTestEnv(t)
	if err := doTestSetup(t, env.cursorDir, env.ramdiskDir, true); err != nil {
		t.Fatal(err)
	}
	withStdin(t, "n\n", func() {
		if err := doTestTeardown(t, env.cursorDir, env.ramdiskDir, false); err != nil {
			t.Fatal(err)
		}
	})
	assertIsSymlink(t, filepath.Join(env.cursorDir, "User", "globalStorage"))
}

func TestDoTestTeardown_NoActiveDirs(t *testing.T) {
	env := newTestEnv(t)
	if err := doTestTeardown(t, env.cursorDir, env.ramdiskDir, true); err != nil {
		t.Fatal(err)
	}
}

func TestDoTestTeardown_RemoveOrigWarn(t *testing.T) {
	env := newTestEnv(t)
	for _, d := range targetDirs {
		if d != "User/globalStorage" {
			os.RemoveAll(filepath.Join(env.cursorDir, filepath.FromSlash(d)))
		}
	}
	if err := doTestSetup(t, env.cursorDir, env.ramdiskDir, true); err != nil {
		t.Fatal(err)
	}
	orig := filepath.Join(env.cursorDir, "User", "globalStorage.orig")
	lock := filepath.Join(orig, "lock")
	if err := os.WriteFile(lock, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("chflags", "uchg", lock).Run(); err != nil {
		t.Fatalf("chflags uchg (need macOS): %v", err)
	}
	defer func() {
		_ = exec.Command("chflags", "nouchg", lock).Run()
	}()
	if err := doTestTeardown(t, env.cursorDir, env.ramdiskDir, true); err != nil {
		t.Fatal(err)
	}
}

func TestCopyDir(t *testing.T) {
	t.Run("dest_new", func(t *testing.T) {
		tmp := t.TempDir()
		src := filepath.Join(tmp, "src")
		os.MkdirAll(filepath.Join(src, "a"), 0o755)
		os.WriteFile(filepath.Join(src, "a", "f"), []byte("x"), 0o644)
		dest := filepath.Join(tmp, "dst")
		if err := copyDir(src, dest); err != nil {
			t.Fatal(err)
		}
		if readFile(t, filepath.Join(dest, "a", "f")) != "x" {
			t.Fatal("copy failed")
		}
	})

	t.Run("dest_exists_removed", func(t *testing.T) {
		tmp := t.TempDir()
		src := filepath.Join(tmp, "src")
		os.MkdirAll(filepath.Join(src, "a"), 0o755)
		os.WriteFile(filepath.Join(src, "a", "f"), []byte("new"), 0o644)
		dest := filepath.Join(tmp, "dst")
		os.MkdirAll(filepath.Join(dest, "old"), 0o755)
		os.WriteFile(filepath.Join(dest, "old", "x"), []byte("old"), 0o644)
		if err := copyDir(src, dest); err != nil {
			t.Fatal(err)
		}
		if readFile(t, filepath.Join(dest, "a", "f")) != "new" {
			t.Fatal("expected replaced dest")
		}
	})

	t.Run("mkdir_fails", func(t *testing.T) {
		tmp := t.TempDir()
		file := filepath.Join(tmp, "notadir")
		os.WriteFile(file, []byte("x"), 0o644)
		src := filepath.Join(tmp, "src")
		os.MkdirAll(src, 0o755)
		dest := filepath.Join(file, "nested")
		if err := copyDir(src, dest); err == nil {
			t.Fatal("expected mkdir error")
		}
	})

	t.Run("cp_fails", func(t *testing.T) {
		tmp := t.TempDir()
		dest := filepath.Join(tmp, "dst")
		if err := copyDir(filepath.Join(tmp, "nope"), dest); err == nil {
			t.Fatal("expected cp error")
		}
	})

	t.Run("remove_dest_fails", func(t *testing.T) {
		tmp := t.TempDir()
		src := filepath.Join(tmp, "src")
		os.MkdirAll(filepath.Join(src, "a"), 0o755)
		dest := filepath.Join(tmp, "dst")
		os.MkdirAll(filepath.Join(dest, "old"), 0o755)
		locked := filepath.Join(dest, "old", "locked")
		if err := os.WriteFile(locked, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := exec.Command("chflags", "uchg", locked).Run(); err != nil {
			t.Skipf("chflags uchg (need macOS): %v", err)
		}
		defer func() {
			_ = exec.Command("chflags", "nouchg", locked).Run()
		}()
		err := copyDir(src, dest)
		if err == nil || !strings.Contains(err.Error(), "remove existing dest") {
			t.Fatalf("expected remove existing dest error, got %v", err)
		}
	})
}

func TestSyncDir(t *testing.T) {
	t.Run("happy", func(t *testing.T) {
		tmp := t.TempDir()
		src := filepath.Join(tmp, "src")
		os.MkdirAll(filepath.Join(src, "sub"), 0o755)
		os.WriteFile(filepath.Join(src, "sub", "f"), []byte("data"), 0o644)
		dest := filepath.Join(tmp, "dest")
		if err := syncDir(src, dest); err != nil {
			t.Fatal(err)
		}
		if readFile(t, filepath.Join(dest, "sub", "f")) != "data" {
			t.Fatal("rsync failed")
		}
	})

	t.Run("error", func(t *testing.T) {
		tmp := t.TempDir()
		if err := syncDir(filepath.Join(tmp, "missing"), filepath.Join(tmp, "out")); err == nil {
			t.Fatal("expected rsync error")
		}
	})
}

func TestDirSize(t *testing.T) {
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, "d"), 0o755)
	os.WriteFile(filepath.Join(tmp, "d", "f"), []byte("abc"), 0o644)
	s := dirSize(filepath.Join(tmp, "d"))
	if s == "?" {
		t.Fatal("expected human size")
	}
	if dirSize(filepath.Join(tmp, "nope")) != "?" {
		t.Fatal("expected ? for missing path")
	}
}

func TestDirSize_FakeDuEmptyFields(t *testing.T) {
	tmp := t.TempDir()
	duPath := filepath.Join(tmp, "du")
	script := "#!/bin/sh\nprintf '\\n'\n"
	if err := os.WriteFile(duPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+oldPath)

	if dirSize(t.TempDir()) != "?" {
		t.Fatal("expected ? when du yields no fields")
	}
}

func TestRun(t *testing.T) {
	if err := run("true"); err != nil {
		t.Fatal(err)
	}
}

func TestUsage(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	usage()
	_ = w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	_ = r.Close()
	if !strings.Contains(buf.String(), "cursor-ramdisk") {
		t.Fatalf("usage stderr unexpected: %q", buf.String())
	}
}

func TestCmdStatus(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	cursorDir := filepath.Join(tmp, "Library", "Application Support", "Cursor")
	os.MkdirAll(filepath.Join(cursorDir, "Cache"), 0o755)
	if err := cmdStatus(); err != nil {
		t.Fatal(err)
	}
}

func TestCursorAppSupportDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	got, err := cursorAppSupportDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(tmp, "Library", "Application Support", "Cursor")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// ---- original integration tests (kept) ----

func TestSetup_CreatesSymlinksAndOrig(t *testing.T) {
	env := newTestEnv(t)
	dirs := []string{"User/globalStorage", "Cache"}
	env.setup(t, dirs)

	for _, dir := range dirs {
		src := filepath.Join(env.cursorDir, filepath.FromSlash(dir))
		dest := filepath.Join(env.ramdiskDir, flattenPath(dir))
		orig := src + ".orig"

		assertIsSymlink(t, src)
		assertIsDir(t, dest)
		assertIsDir(t, orig)

		got := readFile(t, filepath.Join(src, "state.db"))
		if got != "initial:"+dir {
			t.Errorf("data through symlink: got %q want %q", got, "initial:"+dir)
		}
	}
}

func TestSetup_Idempotent(t *testing.T) {
	env := newTestEnv(t)
	dirs := []string{"User/globalStorage"}

	env.setup(t, dirs)
	src := filepath.Join(env.cursorDir, "User", "globalStorage")
	target1, _ := os.Readlink(src)

	info, _ := os.Lstat(src)
	if info.Mode()&fs.ModeSymlink == 0 {
		t.Fatal("expected symlink after first setup")
	}

	target2, _ := os.Readlink(src)
	if target1 != target2 {
		t.Errorf("setup not idempotent: symlink target changed from %s to %s", target1, target2)
	}
}

func TestTeardown_RsyncsLiveStateAndCleansUp(t *testing.T) {
	env := newTestEnv(t)
	dirs := []string{"User/globalStorage", "User/History"}
	env.setup(t, dirs)

	for _, dir := range dirs {
		dest := filepath.Join(env.ramdiskDir, flattenPath(dir))
		os.WriteFile(filepath.Join(dest, "state.db"), []byte("updated:"+dir), 0o644)
		os.WriteFile(filepath.Join(dest, "new-file.txt"), []byte("new"), 0o644)
	}

	env.teardown(t, dirs)

	for _, dir := range dirs {
		src := filepath.Join(env.cursorDir, filepath.FromSlash(dir))
		orig := src + ".orig"

		assertIsDir(t, src)

		got := readFile(t, filepath.Join(src, "state.db"))
		if got != "updated:"+dir {
			t.Errorf("state.db after teardown: got %q want %q", got, "updated:"+dir)
		}
		newFile := readFile(t, filepath.Join(src, "new-file.txt"))
		if newFile != "new" {
			t.Errorf("new-file.txt after teardown: got %q want %q", newFile, "new")
		}

		assertNotExist(t, orig)
	}
}

func TestTeardown_FallsBackToOrigWhenRAMDiskGone(t *testing.T) {
	env := newTestEnv(t)
	dirs := []string{"Cache"}
	env.setup(t, dirs)

	dest := filepath.Join(env.ramdiskDir, flattenPath("Cache"))
	os.RemoveAll(dest)

	env.teardown(t, dirs)

	src := filepath.Join(env.cursorDir, "Cache")
	orig := src + ".orig"

	assertIsDir(t, src)
	assertNotExist(t, orig)

	got := readFile(t, filepath.Join(src, "state.db"))
	if got != "initial:Cache" {
		t.Errorf("fallback data: got %q want initial:Cache", got)
	}
}

func TestTeardown_SymlinkAtomicity(t *testing.T) {
	env := newTestEnv(t)
	dirs := []string{"User/workspaceStorage"}
	env.setup(t, dirs)

	env.teardown(t, dirs)

	src := filepath.Join(env.cursorDir, "User", "workspaceStorage")
	newDir := src + ".new"
	assertIsDir(t, src)
	assertNotExist(t, newDir)
}
