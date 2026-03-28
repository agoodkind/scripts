package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
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
	_ = os.MkdirAll(env.ramdiskDir, 0o755)

	dirs := append([]string(nil), defaultTargetDirs...)
	for _, d := range dirs {
		full := filepath.Join(env.cursorDir, filepath.FromSlash(d))
		_ = os.MkdirAll(full, 0o755)
		_ = os.WriteFile(filepath.Join(full, "state.db"), []byte("initial:"+d), 0o644)
	}
	return env
}

func (e *testEnv) setup(t *testing.T, dirs []string) {
	t.Helper()
	a := newApp()
	a.cursorDir = e.cursorDir
	a.ramdisk = e.ramdiskDir
	for _, dir := range dirs {
		src := filepath.Join(e.cursorDir, filepath.FromSlash(dir))
		dest := filepath.Join(e.ramdiskDir, flattenPath(dir))
		orig := src + ".orig"

		if err := a.copyDir(src, dest); err != nil {
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
	a := newApp()
	a.cursorDir = e.cursorDir
	a.ramdisk = e.ramdiskDir
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
				_ = os.Remove(src)
				_ = os.Rename(orig, src)
			}
			continue
		}

		if err := a.syncDir(dest, newDir); err != nil {
			t.Fatalf("teardown syncDir %s: %v", dir, err)
		}
		if err := os.Remove(src); err != nil {
			t.Fatalf("teardown remove symlink %s: %v", dir, err)
		}
		if err := os.Rename(newDir, src); err != nil {
			t.Fatalf("teardown rename .new -> src %s: %v", dir, err)
		}
		_ = os.RemoveAll(orig)
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
	baseDirs := append([]string(nil), defaultTargetDirs...)

	t.Run("all_real_dirs", func(t *testing.T) {
		a := newApp()
		a.cursorDir = cursorDir
		for _, d := range baseDirs {
			_ = os.MkdirAll(filepath.Join(cursorDir, filepath.FromSlash(d)), 0o755)
		}
		got := a.pendingDirs(cursorDir)
		if len(got) != len(baseDirs) {
			t.Fatalf("pendingDirs: got %v want all %d dirs", got, len(baseDirs))
		}
	})

	t.Run("all_symlinks", func(t *testing.T) {
		dir := t.TempDir()
		cd := filepath.Join(dir, "Cursor")
		a := newApp()
		a.cursorDir = cd
		for _, d := range baseDirs {
			full := filepath.Join(cd, filepath.FromSlash(d))
			_ = os.MkdirAll(filepath.Join(dir, "ram", flattenPath(d)), 0o755)
			_ = os.MkdirAll(filepath.Dir(full), 0o755)
			_ = os.Symlink(filepath.Join(dir, "ram", flattenPath(d)), full)
		}
		if len(a.pendingDirs(cd)) != 0 {
			t.Fatalf("expected no pending when all symlinks")
		}
	})

	t.Run("mix", func(t *testing.T) {
		dir := t.TempDir()
		cd := filepath.Join(dir, "Cursor")
		a := newApp()
		a.cursorDir = cd
		_ = os.MkdirAll(filepath.Join(cd, "User", "globalStorage"), 0o755)
		_ = os.MkdirAll(filepath.Join(dir, "ram", "User_globalStorage"), 0o755)
		gs := filepath.Join(cd, "User", "globalStorage")
		_ = os.RemoveAll(gs)
		_ = os.Symlink(filepath.Join(dir, "ram", "User_globalStorage"), gs)
		_ = os.MkdirAll(filepath.Join(cd, "Cache"), 0o755)
		got := a.pendingDirs(cd)
		if len(got) != 1 || got[0] != "Cache" {
			t.Fatalf("pendingDirs mix: got %v want [Cache]", got)
		}
	})

	t.Run("missing_skipped", func(t *testing.T) {
		dir := t.TempDir()
		cd := filepath.Join(dir, "Cursor")
		a := newApp()
		a.cursorDir = cd
		_ = os.MkdirAll(filepath.Join(cd, "Cache"), 0o755)
		got := a.pendingDirs(cd)
		if len(got) != 1 {
			t.Fatalf("got %v want one dir", got)
		}
	})
}

func TestActiveDirs(t *testing.T) {
	baseDirs := append([]string(nil), defaultTargetDirs...)

	t.Run("all_symlinks", func(t *testing.T) {
		dir := t.TempDir()
		cd := filepath.Join(dir, "Cursor")
		a := newApp()
		a.cursorDir = cd
		for _, d := range baseDirs {
			_ = os.MkdirAll(filepath.Join(dir, "ram", flattenPath(d)), 0o755)
			full := filepath.Join(cd, filepath.FromSlash(d))
			_ = os.MkdirAll(filepath.Dir(full), 0o755)
			_ = os.Symlink(filepath.Join(dir, "ram", flattenPath(d)), full)
		}
		got := a.activeDirs(cd)
		if len(got) != len(baseDirs) {
			t.Fatalf("activeDirs: got %v", got)
		}
	})

	t.Run("none", func(t *testing.T) {
		env := newTestEnv(t)
		a := newApp()
		a.cursorDir = env.cursorDir
		if len(a.activeDirs(env.cursorDir)) != 0 {
			t.Fatal("expected no active dirs")
		}
	})

	t.Run("missing_skipped", func(t *testing.T) {
		dir := t.TempDir()
		cd := filepath.Join(dir, "Cursor")
		a := newApp()
		a.cursorDir = cd
		_ = os.MkdirAll(filepath.Join(cd, "Cache"), 0o755)
		if len(a.activeDirs(cd)) != 0 {
			t.Fatal("expected none")
		}
	})
}

func TestMeasureDirs(t *testing.T) {
	env := newTestEnv(t)
	a := newApp()
	a.cursorDir = env.cursorDir
	pending := a.pendingDirs(env.cursorDir)
	totalMB, sizes, err := a.measureDirs(env.cursorDir, pending)
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
		tmb, m, err := a.measureDirs(env.cursorDir, nil)
		if err != nil || tmb != 0 || len(m) != 0 {
			t.Fatalf("empty dirs: tmb=%d m=%v err=%v", tmb, m, err)
		}
	})
}

func TestMeasureDirs_Error(t *testing.T) {
	tmp := t.TempDir()
	cursorDir := filepath.Join(tmp, "Cursor")
	a := newApp()
	a.cursorDir = cursorDir
	_, _, err := a.measureDirs(cursorDir, []string{"User/globalStorage"})
	if err == nil {
		t.Fatal("expected error for missing directory")
	}
}

func TestDirSizeMB(t *testing.T) {
	tmp := t.TempDir()
	_ = os.MkdirAll(filepath.Join(tmp, "d"), 0o755)
	_ = os.WriteFile(filepath.Join(tmp, "d", "f.txt"), []byte("hello"), 0o644)

	a := newApp()
	a.cursorDir = filepath.Join(tmp, "Cursor")
	mb, err := a.dirSizeMB(filepath.Join(tmp, "d"))
	if err != nil {
		t.Fatal(err)
	}
	if mb < 1 {
		t.Fatalf("expected at least 1 MB for du -sm, got %d", mb)
	}
}

func TestDirSizeMB_FakeDuEmpty(t *testing.T) {
	a := newApp()
	a.cursorDir = t.TempDir()
	a.runOutput = func(name string, args ...string) ([]byte, error) {
		if name == "du" {
			return []byte(""), nil
		}
		return nil, errors.New("unexpected")
	}
	_, err := a.dirSizeMB(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "empty output") {
		t.Fatalf("expected empty output error, got %v", err)
	}
}

func TestDirSizeMB_FakeDuBadParse(t *testing.T) {
	a := newApp()
	a.cursorDir = t.TempDir()
	a.runOutput = func(name string, args ...string) ([]byte, error) {
		if name == "du" {
			return []byte("notanumber\n"), nil
		}
		return nil, errors.New("unexpected")
	}
	_, err := a.dirSizeMB(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

func TestSymlinkTargetExists(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "target")
	_ = os.MkdirAll(target, 0o755)
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
		a := newApp()
		a.cursorDir = t.TempDir()
		if err := a.printStatus(t.TempDir()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("disk", func(t *testing.T) {
		tmp := t.TempDir()
		a := newApp()
		a.cursorDir = tmp
		_ = os.MkdirAll(filepath.Join(tmp, "User", "globalStorage"), 0o755)
		_ = os.WriteFile(filepath.Join(tmp, "User", "globalStorage", "x"), []byte("a"), 0o644)
		for _, d := range []string{"User/workspaceStorage", "User/History", "Cache"} {
			_ = os.MkdirAll(filepath.Join(tmp, filepath.FromSlash(d)), 0o755)
		}
		if err := a.printStatus(tmp); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("ram_valid", func(t *testing.T) {
		tmp := t.TempDir()
		ram := filepath.Join(tmp, "ram")
		_ = os.MkdirAll(ram, 0o755)
		_ = os.WriteFile(filepath.Join(ram, "z"), []byte("z"), 0o644)
		src := filepath.Join(tmp, "User", "globalStorage")
		_ = os.MkdirAll(filepath.Join(tmp, "User"), 0o755)
		_ = os.Symlink(ram, src)
		for _, d := range []string{"User/workspaceStorage", "User/History", "Cache"} {
			_ = os.MkdirAll(filepath.Join(tmp, filepath.FromSlash(d)), 0o755)
		}
		a := newApp()
		a.cursorDir = tmp
		if err := a.printStatus(tmp); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("dang", func(t *testing.T) {
		tmp := t.TempDir()
		src := filepath.Join(tmp, "User", "globalStorage")
		_ = os.MkdirAll(filepath.Join(tmp, "User"), 0o755)
		_ = os.Symlink(filepath.Join(tmp, "missing"), src)
		for _, d := range []string{"User/workspaceStorage", "User/History", "Cache"} {
			_ = os.MkdirAll(filepath.Join(tmp, filepath.FromSlash(d)), 0o755)
		}
		a := newApp()
		a.cursorDir = tmp
		if err := a.printStatus(tmp); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("stat_error", func(t *testing.T) {
		tmp := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmp, "User"), []byte("x"), 0o644)
		a := newApp()
		a.cursorDir = tmp
		err := a.printStatus(tmp)
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
			a := newApp()
			a.stdin = strings.NewReader(tc.in)
			a.stdout = io.Discard
			a.stderr = io.Discard
			if got := a.confirm("ok"); got != tc.want {
				t.Fatalf("confirm(%q): got %v want %v", tc.in, got, tc.want)
			}
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
		a := newApp()
		a.stdin = os.Stdin
		a.stdout = io.Discard
		a.stderr = io.Discard
		if a.confirm("x") {
			t.Fatal("expected false on EOF")
		}
	})
}

// doTestSetup mirrors cmdSetup but uses a.ramdisk instead of the default mount
// and skips guardCursorNotRunning, tmutil, and ensureRamdisk.
func doTestSetup(t *testing.T, a *app, yes bool) error {
	t.Helper()
	cursorDir := a.cursorDir
	ramdiskRoot := a.ramdisk
	pending := a.pendingDirs(cursorDir)
	if len(pending) == 0 {
		a.logf("All directories already on RAM disk. Nothing to do.")
		return a.printStatus(cursorDir)
	}

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

	for _, dir := range pending {
		src := filepath.Join(cursorDir, filepath.FromSlash(dir))
		dest := filepath.Join(ramdiskRoot, flattenPath(dir))
		orig := src + ".orig"

		a.logf("Copying %s -> %s ...", dir, dest)
		if err := a.copyDir(src, dest); err != nil {
			return fmt.Errorf("copy %s -> %s: %w", src, dest, err)
		}

		a.logf("Saving original as %s ...", orig)
		if err := os.Rename(src, orig); err != nil {
			return fmt.Errorf("rename %s -> %s: %w", src, orig, err)
		}

		a.logf("Symlinking %s -> %s ...", src, dest)
		if err := os.Symlink(dest, src); err != nil {
			return fmt.Errorf("symlink %s -> %s: %w", dest, src, err)
		}

		a.logf("Done: %s", dir)
	}

	fmt.Fprintln(a.stdout)
	return a.printStatus(cursorDir)
}

// doTestTeardown mirrors cmdTeardown but uses a.ramdisk instead of the default
// mount and skips guardCursorNotRunning.
func doTestTeardown(t *testing.T, a *app, yes bool) error {
	t.Helper()
	cursorDir := a.cursorDir
	ramdiskRoot := a.ramdisk
	active := a.activeDirs(cursorDir)
	if len(active) == 0 {
		a.logf("No directories are on the RAM disk. Nothing to do.")
		return a.printStatus(cursorDir)
	}

	a.logf("Directories to sync back to disk:")
	for _, dir := range active {
		src := filepath.Join(cursorDir, filepath.FromSlash(dir))
		dest := filepath.Join(ramdiskRoot, flattenPath(dir))
		if _, err := os.Stat(dest); errors.Is(err, fs.ErrNotExist) {
			a.logf("  %-30s  (RAM disk gone -- will restore from .orig)", dir)
		} else {
			a.logf("  %-30s  %s", dir, a.dirSize(src))
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
		dest := filepath.Join(ramdiskRoot, flattenPath(dir))
		orig := src + ".orig"

		if _, err := os.Stat(dest); errors.Is(err, fs.ErrNotExist) {
			a.logf("WARN: RAM disk dest %s missing -- RAM disk may be gone", dest)
			a.logf("      Restoring from .orig if available...")
			if _, statErr := os.Stat(orig); statErr == nil {
				if err := os.Remove(src); err != nil {
					return fmt.Errorf("remove dangling symlink %s: %w", src, err)
				}
				if err := os.Rename(orig, src); err != nil {
					return fmt.Errorf("restore orig %s -> %s: %w", orig, src, err)
				}
				a.logf("  Restored from .orig: %s", dir)
			} else {
				a.logf("  ERROR: no .orig found either -- %s is unrecoverable", dir)
			}
			continue
		}

		newDir := src + ".new"
		a.logf("Syncing %s -> %s ...", dest, newDir)
		if err := a.syncDir(dest, newDir); err != nil {
			return fmt.Errorf("sync %s -> %s: %w", dest, newDir, err)
		}

		a.logf("Replacing symlink with synced directory ...")
		if err := os.Remove(src); err != nil {
			return fmt.Errorf("remove symlink %s: %w", src, err)
		}
		if err := os.Rename(newDir, src); err != nil {
			return fmt.Errorf("rename %s -> %s: %w", newDir, src, err)
		}

		if err := os.RemoveAll(orig); err != nil {
			a.logf("WARN: could not remove .orig %s: %v", orig, err)
		}

		a.logf("Done: %s", dir)
	}

	fmt.Fprintln(a.stdout)
	a.logf("Teardown complete. Cursor state is back on disk.")
	a.logf("You can now start Cursor or eject %s if it is still mounted.", a.ramdisk)
	return nil
}

func testAppForDoTest(t *testing.T) *app {
	t.Helper()
	a := newApp()
	a.stdout = io.Discard
	a.stderr = io.Discard
	return a
}

func TestDoTestSetup_FullFlow(t *testing.T) {
	env := newTestEnv(t)
	a := testAppForDoTest(t)
	a.cursorDir = env.cursorDir
	a.ramdisk = env.ramdiskDir
	if err := doTestSetup(t, a, true); err != nil {
		t.Fatal(err)
	}
	for _, d := range defaultTargetDirs {
		src := filepath.Join(env.cursorDir, filepath.FromSlash(d))
		assertIsSymlink(t, src)
	}
}

func TestDoTestSetup_Idempotent(t *testing.T) {
	env := newTestEnv(t)
	a := testAppForDoTest(t)
	a.cursorDir = env.cursorDir
	a.ramdisk = env.ramdiskDir
	if err := doTestSetup(t, a, true); err != nil {
		t.Fatal(err)
	}
	t1, _ := os.Readlink(filepath.Join(env.cursorDir, "User", "globalStorage"))
	if err := doTestSetup(t, a, true); err != nil {
		t.Fatal(err)
	}
	t2, _ := os.Readlink(filepath.Join(env.cursorDir, "User", "globalStorage"))
	if t1 != t2 {
		t.Fatalf("symlink target changed: %s vs %s", t1, t2)
	}
}

func TestDoTestSetup_Aborted(t *testing.T) {
	env := newTestEnv(t)
	a := newApp()
	a.cursorDir = env.cursorDir
	a.ramdisk = env.ramdiskDir
	a.stdout = io.Discard
	a.stderr = io.Discard
	withStdin(t, "n\n", func() {
		a.stdin = os.Stdin
		if err := doTestSetup(t, a, false); err != nil {
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
	a := testAppForDoTest(t)
	a.cursorDir = env.cursorDir
	a.ramdisk = env.ramdiskDir
	if err := doTestSetup(t, a, true); err != nil {
		t.Fatal(err)
	}
	for _, d := range defaultTargetDirs {
		dest := filepath.Join(env.ramdiskDir, flattenPath(d))
		_ = os.WriteFile(filepath.Join(dest, "live.txt"), []byte("live:"+d), 0o644)
	}
	if err := doTestTeardown(t, a, true); err != nil {
		t.Fatal(err)
	}
	for _, d := range defaultTargetDirs {
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
	a := testAppForDoTest(t)
	a.cursorDir = env.cursorDir
	a.ramdisk = env.ramdiskDir
	for _, d := range defaultTargetDirs {
		if d != "Cache" {
			_ = os.RemoveAll(filepath.Join(env.cursorDir, filepath.FromSlash(d)))
		}
	}
	if err := doTestSetup(t, a, true); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(env.ramdiskDir, flattenPath("Cache"))
	_ = os.RemoveAll(dest)

	if err := doTestTeardown(t, a, true); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(env.cursorDir, "Cache")
	assertIsDir(t, src)
}

func TestDoTestTeardown_RAMGoneNoOrig(t *testing.T) {
	env := newTestEnv(t)
	a := testAppForDoTest(t)
	a.cursorDir = env.cursorDir
	a.ramdisk = env.ramdiskDir
	for _, d := range defaultTargetDirs {
		if d != "Cache" {
			_ = os.RemoveAll(filepath.Join(env.cursorDir, filepath.FromSlash(d)))
		}
	}
	if err := doTestSetup(t, a, true); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(env.cursorDir, "Cache")
	orig := src + ".orig"
	_ = os.RemoveAll(orig)
	dest := filepath.Join(env.ramdiskDir, flattenPath("Cache"))
	_ = os.RemoveAll(dest)

	if err := doTestTeardown(t, a, true); err != nil {
		t.Fatal(err)
	}
	assertIsSymlink(t, src)
}

func TestDoTestTeardown_Aborted(t *testing.T) {
	env := newTestEnv(t)
	a := newApp()
	a.cursorDir = env.cursorDir
	a.ramdisk = env.ramdiskDir
	a.stdout = io.Discard
	a.stderr = io.Discard
	if err := doTestSetup(t, a, true); err != nil {
		t.Fatal(err)
	}
	withStdin(t, "n\n", func() {
		a.stdin = os.Stdin
		if err := doTestTeardown(t, a, false); err != nil {
			t.Fatal(err)
		}
	})
	assertIsSymlink(t, filepath.Join(env.cursorDir, "User", "globalStorage"))
}

func TestDoTestTeardown_NoActiveDirs(t *testing.T) {
	env := newTestEnv(t)
	a := testAppForDoTest(t)
	a.cursorDir = env.cursorDir
	a.ramdisk = env.ramdiskDir
	if err := doTestTeardown(t, a, true); err != nil {
		t.Fatal(err)
	}
}

func TestDoTestTeardown_RemoveOrigWarn(t *testing.T) {
	env := newTestEnv(t)
	a := testAppForDoTest(t)
	a.cursorDir = env.cursorDir
	a.ramdisk = env.ramdiskDir
	for _, d := range defaultTargetDirs {
		if d != "User/globalStorage" {
			_ = os.RemoveAll(filepath.Join(env.cursorDir, filepath.FromSlash(d)))
		}
	}
	if err := doTestSetup(t, a, true); err != nil {
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
	if err := doTestTeardown(t, a, true); err != nil {
		t.Fatal(err)
	}
}

func TestCopyDir(t *testing.T) {
	t.Run("dest_new", func(t *testing.T) {
		tmp := t.TempDir()
		src := filepath.Join(tmp, "src")
		_ = os.MkdirAll(filepath.Join(src, "a"), 0o755)
		_ = os.WriteFile(filepath.Join(src, "a", "f"), []byte("x"), 0o644)
		dest := filepath.Join(tmp, "dst")
		a := newApp()
		a.cursorDir = tmp
		if err := a.copyDir(src, dest); err != nil {
			t.Fatal(err)
		}
		if readFile(t, filepath.Join(dest, "a", "f")) != "x" {
			t.Fatal("copy failed")
		}
	})

	t.Run("dest_exists_removed", func(t *testing.T) {
		tmp := t.TempDir()
		src := filepath.Join(tmp, "src")
		_ = os.MkdirAll(filepath.Join(src, "a"), 0o755)
		_ = os.WriteFile(filepath.Join(src, "a", "f"), []byte("new"), 0o644)
		dest := filepath.Join(tmp, "dst")
		_ = os.MkdirAll(filepath.Join(dest, "old"), 0o755)
		_ = os.WriteFile(filepath.Join(dest, "old", "x"), []byte("old"), 0o644)
		a := newApp()
		a.cursorDir = tmp
		if err := a.copyDir(src, dest); err != nil {
			t.Fatal(err)
		}
		if readFile(t, filepath.Join(dest, "a", "f")) != "new" {
			t.Fatal("expected replaced dest")
		}
	})

	t.Run("mkdir_fails", func(t *testing.T) {
		tmp := t.TempDir()
		file := filepath.Join(tmp, "notadir")
		_ = os.WriteFile(file, []byte("x"), 0o644)
		src := filepath.Join(tmp, "src")
		_ = os.MkdirAll(src, 0o755)
		dest := filepath.Join(file, "nested")
		a := newApp()
		a.cursorDir = tmp
		if err := a.copyDir(src, dest); err == nil {
			t.Fatal("expected mkdir error")
		}
	})

	t.Run("cp_fails", func(t *testing.T) {
		tmp := t.TempDir()
		dest := filepath.Join(tmp, "dst")
		a := newApp()
		a.cursorDir = tmp
		if err := a.copyDir(filepath.Join(tmp, "nope"), dest); err == nil {
			t.Fatal("expected cp error")
		}
	})

	t.Run("remove_dest_fails", func(t *testing.T) {
		tmp := t.TempDir()
		src := filepath.Join(tmp, "src")
		_ = os.MkdirAll(filepath.Join(src, "a"), 0o755)
		dest := filepath.Join(tmp, "dst")
		_ = os.MkdirAll(filepath.Join(dest, "old"), 0o755)
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
		a := newApp()
		a.cursorDir = tmp
		err := a.copyDir(src, dest)
		if err == nil || !strings.Contains(err.Error(), "remove existing dest") {
			t.Fatalf("expected remove existing dest error, got %v", err)
		}
	})
}

func TestSyncDir(t *testing.T) {
	t.Run("happy", func(t *testing.T) {
		tmp := t.TempDir()
		src := filepath.Join(tmp, "src")
		_ = os.MkdirAll(filepath.Join(src, "sub"), 0o755)
		_ = os.WriteFile(filepath.Join(src, "sub", "f"), []byte("data"), 0o644)
		dest := filepath.Join(tmp, "dest")
		a := newApp()
		a.cursorDir = tmp
		if err := a.syncDir(src, dest); err != nil {
			t.Fatal(err)
		}
		if readFile(t, filepath.Join(dest, "sub", "f")) != "data" {
			t.Fatal("rsync failed")
		}
	})

	t.Run("error", func(t *testing.T) {
		tmp := t.TempDir()
		a := newApp()
		a.cursorDir = tmp
		if err := a.syncDir(filepath.Join(tmp, "missing"), filepath.Join(tmp, "out")); err == nil {
			t.Fatal("expected rsync error")
		}
	})
}

func TestDirSize(t *testing.T) {
	tmp := t.TempDir()
	_ = os.MkdirAll(filepath.Join(tmp, "d"), 0o755)
	_ = os.WriteFile(filepath.Join(tmp, "d", "f"), []byte("abc"), 0o644)
	a := newApp()
	a.cursorDir = tmp
	s := a.dirSize(filepath.Join(tmp, "d"))
	if s == "?" {
		t.Fatal("expected human size")
	}
	if a.dirSize(filepath.Join(tmp, "nope")) != "?" {
		t.Fatal("expected ? for missing path")
	}
}

func TestDirSize_FakeDuEmptyFields(t *testing.T) {
	a := newApp()
	a.cursorDir = t.TempDir()
	a.runOutput = func(name string, args ...string) ([]byte, error) {
		if name == "du" {
			return []byte("\n"), nil
		}
		return nil, errors.New("unexpected")
	}
	if a.dirSize(t.TempDir()) != "?" {
		t.Fatal("expected ? when du yields no fields")
	}
}

func TestAppRun(t *testing.T) {
	a := newApp()
	a.cursorDir = t.TempDir()
	if err := a.run("true"); err != nil {
		t.Fatal(err)
	}
}

func TestUsage(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	a := newApp()
	a.stderr = w
	a.usage()
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	_ = r.Close()
	if !strings.Contains(buf.String(), "cursor-ramdisk") {
		t.Fatalf("usage stderr unexpected: %q", buf.String())
	}
}

func TestCmdStatus(t *testing.T) {
	tmp := t.TempDir()
	a := newApp()
	a.cursorDir = filepath.Join(tmp, "Library", "Application Support", "Cursor")
	_ = os.MkdirAll(filepath.Join(a.cursorDir, "Cache"), 0o755)
	a.stdout = io.Discard
	if err := a.cmdStatus(); err != nil {
		t.Fatal(err)
	}
}

func TestCursorAppSupportDir(t *testing.T) {
	tmp := t.TempDir()
	a := newApp()
	a.cursorDir = filepath.Join(tmp, "Library", "Application Support", "Cursor")
	got, err := a.cursorAppSupportDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != a.cursorDir {
		t.Fatalf("got %q want %q", got, a.cursorDir)
	}
}

func TestCursorAppSupportDir_UserHomeError(t *testing.T) {
	a := newApp()
	a.cursorDir = ""
	a.userHome = func() (string, error) {
		return "", errors.New("no home")
	}
	_, err := a.cursorAppSupportDir()
	if err == nil || !strings.Contains(err.Error(), "home dir") {
		t.Fatalf("expected home error, got %v", err)
	}
}

func TestRunArgs(t *testing.T) {
	t.Run("no_args", func(t *testing.T) {
		a := newApp()
		a.stderr = io.Discard
		if c := a.runArgs(nil); c != 1 {
			t.Fatalf("exit %d want 1", c)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		a := newApp()
		a.stderr = io.Discard
		if c := a.runArgs([]string{"nope"}); c != 1 {
			t.Fatalf("exit %d want 1", c)
		}
	})

	t.Run("status_ok", func(t *testing.T) {
		tmp := t.TempDir()
		a := newApp()
		a.cursorDir = filepath.Join(tmp, "Library", "Application Support", "Cursor")
		_ = os.MkdirAll(filepath.Join(a.cursorDir, "Cache"), 0o755)
		a.stdout = io.Discard
		a.stderr = io.Discard
		if c := a.runArgs([]string{"status"}); c != 0 {
			t.Fatalf("exit %d want 0", c)
		}
	})

	t.Run("status_err", func(t *testing.T) {
		tmp := t.TempDir()
		a := newApp()
		a.cursorDir = filepath.Join(tmp, "Library", "Application Support", "Cursor")
		_ = os.MkdirAll(a.cursorDir, 0o755)
		_ = os.WriteFile(filepath.Join(a.cursorDir, "User"), []byte("x"), 0o644)
		a.stdout = io.Discard
		a.stderr = io.Discard
		if c := a.runArgs([]string{"status"}); c != 1 {
			t.Fatalf("exit %d want 1", c)
		}
	})

	t.Run("y_flag", func(t *testing.T) {
		tmp := t.TempDir()
		a := newApp()
		a.cursorDir = filepath.Join(tmp, "Library", "Application Support", "Cursor")
		_ = os.MkdirAll(filepath.Join(a.cursorDir, "Cache"), 0o755)
		a.stdout = io.Discard
		a.stderr = io.Discard
		if c := a.runArgs([]string{"-y", "status"}); c != 0 {
			t.Fatalf("exit %d want 0", c)
		}
	})
}

func TestMain(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	_ = os.MkdirAll(filepath.Join(tmp, "Library", "Application Support", "Cursor", "Cache"), 0o755)

	var code int
	oldExit := osExit
	oldArgs := os.Args
	osExit = func(c int) { code = c }
	defer func() {
		osExit = oldExit
		os.Args = oldArgs
	}()

	os.Args = []string{"cursor-ramdisk", "status"}
	main()
	if code != 0 {
		t.Fatalf("main exit %d want 0", code)
	}
}

func TestGuardCursorNotRunning(t *testing.T) {
	t.Run("running", func(t *testing.T) {
		a := newApp()
		a.runOutput = func(name string, args ...string) ([]byte, error) {
			if name == "pgrep" {
				return []byte("123\n"), nil
			}
			return nil, errors.New("unexpected")
		}
		a.stdout = io.Discard
		err := a.guardCursorNotRunning()
		if err == nil {
			t.Fatal("expected error when Cursor running")
		}
	})

	t.Run("not_running", func(t *testing.T) {
		a := newApp()
		a.runOutput = func(name string, args ...string) ([]byte, error) {
			if name == "pgrep" {
				return nil, errors.New("exit status 1")
			}
			return nil, errors.New("unexpected")
		}
		a.stdout = io.Discard
		if err := a.guardCursorNotRunning(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestPhysRAMAvailableMB(t *testing.T) {
	t.Run("happy", func(t *testing.T) {
		a := newApp()
		a.runOutput = func(name string, args ...string) ([]byte, error) {
			if name == "sysctl" && len(args) == 2 && args[1] == "vm.page_free_count" {
				return []byte("65536\n"), nil
			}
			if name == "sysctl" && len(args) == 2 && args[1] == "vm.pagesize" {
				return []byte("16384\n"), nil
			}
			return nil, fmt.Errorf("unexpected sysctl %v", args)
		}
		mb, err := a.physRAMAvailableMB()
		if err != nil {
			t.Fatal(err)
		}
		// 65536 pages * 16384 bytes / 1024 / 1024 = 1024 MB
		if mb != 1024 {
			t.Fatalf("expected 1024 MB, got %d", mb)
		}
	})

	t.Run("page_free_count_error", func(t *testing.T) {
		a := newApp()
		a.runOutput = func(name string, args ...string) ([]byte, error) {
			if name == "sysctl" && len(args) == 2 && args[1] == "vm.page_free_count" {
				return nil, errors.New("sysctl failed")
			}
			return nil, errors.New("unexpected")
		}
		_, err := a.physRAMAvailableMB()
		if err == nil || !strings.Contains(err.Error(), "vm.page_free_count") {
			t.Fatalf("expected page_free_count error, got %v", err)
		}
	})

	t.Run("page_free_count_parse_error", func(t *testing.T) {
		a := newApp()
		a.runOutput = func(name string, args ...string) ([]byte, error) {
			if name == "sysctl" && len(args) == 2 && args[1] == "vm.page_free_count" {
				return []byte("notanumber\n"), nil
			}
			return nil, errors.New("unexpected")
		}
		_, err := a.physRAMAvailableMB()
		if err == nil || !strings.Contains(err.Error(), "parse vm.page_free_count") {
			t.Fatalf("expected parse error, got %v", err)
		}
	})

	t.Run("pagesize_error", func(t *testing.T) {
		a := newApp()
		a.runOutput = func(name string, args ...string) ([]byte, error) {
			if name == "sysctl" && len(args) == 2 && args[1] == "vm.page_free_count" {
				return []byte("65536\n"), nil
			}
			if name == "sysctl" && len(args) == 2 && args[1] == "vm.pagesize" {
				return nil, errors.New("sysctl failed")
			}
			return nil, errors.New("unexpected")
		}
		_, err := a.physRAMAvailableMB()
		if err == nil || !strings.Contains(err.Error(), "vm.pagesize") {
			t.Fatalf("expected pagesize error, got %v", err)
		}
	})

	t.Run("pagesize_parse_error", func(t *testing.T) {
		a := newApp()
		a.runOutput = func(name string, args ...string) ([]byte, error) {
			if name == "sysctl" && len(args) == 2 && args[1] == "vm.page_free_count" {
				return []byte("65536\n"), nil
			}
			if name == "sysctl" && len(args) == 2 && args[1] == "vm.pagesize" {
				return []byte("notanumber\n"), nil
			}
			return nil, errors.New("unexpected")
		}
		_, err := a.physRAMAvailableMB()
		if err == nil || !strings.Contains(err.Error(), "parse vm.pagesize") {
			t.Fatalf("expected parse pagesize error, got %v", err)
		}
	})
}

func TestRamdiskSizeMB(t *testing.T) {
	t.Run("happy", func(t *testing.T) {
		a := newApp()
		a.ramdisk = "/Volumes/CursorRAM"
		a.runOutput = func(name string, args ...string) ([]byte, error) {
			if name == "df" && args[0] == "-m" {
				return []byte("Filesystem  1M-blocks  Used  Available  Capacity  Mounted on\n/dev/disk5s1  15360  10240  5120  67%  /Volumes/CursorRAM\n"), nil
			}
			return nil, errors.New("unexpected")
		}
		mb, err := a.ramdiskSizeMB()
		if err != nil {
			t.Fatal(err)
		}
		if mb != 15360 {
			t.Fatalf("expected 15360, got %d", mb)
		}
	})

	t.Run("df_error", func(t *testing.T) {
		a := newApp()
		a.ramdisk = "/Volumes/CursorRAM"
		a.runOutput = func(name string, args ...string) ([]byte, error) {
			return nil, errors.New("df failed")
		}
		_, err := a.ramdiskSizeMB()
		if err == nil || !strings.Contains(err.Error(), "df -m") {
			t.Fatalf("expected df error, got %v", err)
		}
	})

	t.Run("single_line_output", func(t *testing.T) {
		a := newApp()
		a.ramdisk = "/Volumes/CursorRAM"
		a.runOutput = func(name string, args ...string) ([]byte, error) {
			return []byte("Filesystem  1M-blocks\n"), nil
		}
		_, err := a.ramdiskSizeMB()
		if err == nil || !strings.Contains(err.Error(), "unexpected output") {
			t.Fatalf("expected unexpected output error, got %v", err)
		}
	})

	t.Run("parse_error", func(t *testing.T) {
		a := newApp()
		a.ramdisk = "/Volumes/CursorRAM"
		a.runOutput = func(name string, args ...string) ([]byte, error) {
			return []byte("Filesystem  1M-blocks\n/dev/disk5s1  notanumber\n"), nil
		}
		_, err := a.ramdiskSizeMB()
		if err == nil || !strings.Contains(err.Error(), "parse") {
			t.Fatalf("expected parse error, got %v", err)
		}
	})
}

func TestEnsureRamdisk(t *testing.T) {
	t.Run("already_mounted", func(t *testing.T) {
		tmp := t.TempDir()
		a := newApp()
		a.ramdisk = tmp
		a.stdout = io.Discard
		if err := a.ensureRamdisk(100); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("already_mounted_large_enough", func(t *testing.T) {
		tmp := t.TempDir()
		a := newApp()
		a.ramdisk = tmp
		a.stdout = io.Discard
		a.runOutput = func(name string, args ...string) ([]byte, error) {
			if name == "df" {
				return []byte("Filesystem  1M-blocks  Used  Available  Capacity  Mounted on\n/dev/diskX  16000  8000  8000  50%  " + tmp + "\n"), nil
			}
			return nil, errors.New("unexpected")
		}
		if err := a.ensureRamdisk(15000); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("already_mounted_too_small", func(t *testing.T) {
		tmp := t.TempDir()
		a := newApp()
		a.ramdisk = tmp
		a.stdout = io.Discard
		a.runOutput = func(name string, args ...string) ([]byte, error) {
			if name == "df" {
				return []byte("Filesystem  1M-blocks  Used  Available  Capacity  Mounted on\n/dev/diskX  12000  8000  4000  67%  " + tmp + "\n"), nil
			}
			return nil, errors.New("unexpected")
		}
		err := a.ensureRamdisk(15000)
		if err == nil || !strings.Contains(err.Error(), "run teardown first") {
			t.Fatalf("expected undersized error, got %v", err)
		}
	})

	t.Run("check_avail_ram_error", func(t *testing.T) {
		tmp := t.TempDir()
		a := newApp()
		a.ramdisk = filepath.Join(tmp, "missing-mount")
		a.runOutput = func(name string, args ...string) ([]byte, error) {
			if name == "sysctl" {
				return nil, errors.New("sysctl unavailable")
			}
			return nil, errors.New("unexpected")
		}
		a.stdout = io.Discard
		err := a.ensureRamdisk(100)
		if err == nil || !strings.Contains(err.Error(), "check available RAM") {
			t.Fatalf("expected check available RAM error, got %v", err)
		}
	})

	t.Run("not_enough_ram", func(t *testing.T) {
		tmp := t.TempDir()
		a := newApp()
		a.ramdisk = filepath.Join(tmp, "missing-mount")
		a.stdout = io.Discard
		// Report only 1 free page * 4096 bytes = 4 KB free -> 0 MB
		a.runOutput = func(name string, args ...string) ([]byte, error) {
			if name == "sysctl" && len(args) == 2 && args[1] == "vm.page_free_count" {
				return []byte("1\n"), nil
			}
			if name == "sysctl" && len(args) == 2 && args[1] == "vm.pagesize" {
				return []byte("4096\n"), nil
			}
			return nil, errors.New("unexpected")
		}
		err := a.ensureRamdisk(9999)
		if err == nil || !strings.Contains(err.Error(), "not enough free physical RAM") {
			t.Fatalf("expected not enough RAM error, got %v", err)
		}
	})

	t.Run("hdiutil_error", func(t *testing.T) {
		tmp := t.TempDir()
		a := newApp()
		a.ramdisk = filepath.Join(tmp, "missing-mount")
		a.runOutput = func(name string, args ...string) ([]byte, error) {
			if name == "sysctl" && len(args) == 2 && args[1] == "vm.page_free_count" {
				return []byte("2097152\n"), nil
			}
			if name == "sysctl" && len(args) == 2 && args[1] == "vm.pagesize" {
				return []byte("16384\n"), nil
			}
			return nil, errors.New("unexpected")
		}
		a.stdout = io.Discard
		err := a.ensureRamdisk(64)
		if err == nil || !strings.Contains(err.Error(), "hdiutil") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("diskutil_error", func(t *testing.T) {
		tmp := t.TempDir()
		a := newApp()
		a.ramdisk = filepath.Join(tmp, "missing-mount")
		a.runOutput = func(name string, args ...string) ([]byte, error) {
			if name == "sysctl" && len(args) == 2 && args[1] == "vm.page_free_count" {
				return []byte("65536\n"), nil
			}
			if name == "sysctl" && len(args) == 2 && args[1] == "vm.pagesize" {
				return []byte("16384\n"), nil
			}
			if name == "hdiutil" {
				return []byte("/dev/disk999\n"), nil
			}
			return nil, errors.New("unexpected")
		}
		a.run = func(name string, args ...string) error {
			if name == "diskutil" {
				return errors.New("diskutil failed")
			}
			return nil
		}
		a.stdout = io.Discard
		err := a.ensureRamdisk(64)
		if err == nil || !strings.Contains(err.Error(), "diskutil") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("success", func(t *testing.T) {
		tmp := t.TempDir()
		a := newApp()
		a.ramdisk = filepath.Join(tmp, "missing-mount")
		a.runOutput = func(name string, args ...string) ([]byte, error) {
			if name == "sysctl" && len(args) == 2 && args[1] == "vm.page_free_count" {
				return []byte("65536\n"), nil
			}
			if name == "sysctl" && len(args) == 2 && args[1] == "vm.pagesize" {
				return []byte("16384\n"), nil
			}
			if name == "hdiutil" {
				return []byte("/dev/disk42\n"), nil
			}
			return nil, errors.New("unexpected")
		}
		a.run = func(name string, args ...string) error {
			if name == "diskutil" {
				return nil
			}
			return fmt.Errorf("unexpected run %s", name)
		}
		a.stdout = io.Discard
		if err := a.ensureRamdisk(128); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCmdSetup_Integration(t *testing.T) {
	tmp := t.TempDir()
	cursorDir := filepath.Join(tmp, "Cursor")
	ramdisk := filepath.Join(tmp, "RAM")
	_ = os.MkdirAll(ramdisk, 0o755)
	for _, d := range defaultTargetDirs {
		_ = os.MkdirAll(filepath.Join(cursorDir, filepath.FromSlash(d)), 0o755)
		_ = os.WriteFile(filepath.Join(cursorDir, filepath.FromSlash(d), "f"), []byte("x"), 0o644)
	}

	a := newApp()
	a.cursorDir = cursorDir
	a.ramdisk = ramdisk
	a.stdout = io.Discard
	a.stderr = io.Discard
	a.run = func(name string, args ...string) error {
		if name == "tmutil" {
			return nil
		}
		if name == "diskutil" {
			return nil
		}
		return newApp().run(name, args...)
	}
	a.runOutput = func(name string, args ...string) ([]byte, error) {
		if name == "pgrep" {
			return nil, errors.New("not running")
		}
		return newApp().runOutput(name, args...)
	}

	if err := a.cmdSetup(true); err != nil {
		t.Fatal(err)
	}
	for _, d := range defaultTargetDirs {
		assertIsSymlink(t, filepath.Join(cursorDir, filepath.FromSlash(d)))
	}
}

func TestCmdSetup_IdempotentEarlyStatus(t *testing.T) {
	tmp := t.TempDir()
	cursorDir := filepath.Join(tmp, "Cursor")
	ramdisk := filepath.Join(tmp, "RAM")
	_ = os.MkdirAll(ramdisk, 0o755)
	for _, d := range defaultTargetDirs {
		_ = os.MkdirAll(filepath.Join(tmp, "t", flattenPath(d)), 0o755)
		full := filepath.Join(cursorDir, filepath.FromSlash(d))
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.Symlink(filepath.Join(tmp, "t", flattenPath(d)), full)
	}

	a := newApp()
	a.cursorDir = cursorDir
	a.ramdisk = ramdisk
	a.stdout = io.Discard
	a.stderr = io.Discard
	a.run = func(name string, args ...string) error {
		return errors.New("unexpected run")
	}
	a.runOutput = func(name string, args ...string) ([]byte, error) {
		if name == "pgrep" {
			return nil, errors.New("not running")
		}
		return newApp().runOutput(name, args...)
	}

	if err := a.cmdSetup(true); err != nil {
		t.Fatal(err)
	}
}

func TestCmdSetup_Errors(t *testing.T) {
	t.Run("cursor_dir_error", func(t *testing.T) {
		a := newApp()
		a.cursorDir = ""
		a.userHome = func() (string, error) {
			return "", errors.New("boom")
		}
		a.stdout = io.Discard
		err := a.cmdSetup(true)
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("tmutil_error", func(t *testing.T) {
		tmp := t.TempDir()
		cursorDir := filepath.Join(tmp, "Cursor")
		ramdisk := filepath.Join(tmp, "RAM")
		_ = os.MkdirAll(ramdisk, 0o755)
		_ = os.MkdirAll(filepath.Join(cursorDir, "Cache"), 0o755)

		a := newApp()
		a.cursorDir = cursorDir
		a.ramdisk = ramdisk
		a.stdout = io.Discard
		a.stderr = io.Discard
		a.dirs = []string{"Cache"}
		a.runOutput = func(name string, args ...string) ([]byte, error) {
			if name == "pgrep" {
				return nil, errors.New("not running")
			}
			return newApp().runOutput(name, args...)
		}
		a.run = func(name string, args ...string) error {
			if name == "tmutil" {
				return errors.New("tmutil failed")
			}
			return nil
		}

		err := a.cmdSetup(true)
		if err == nil || !strings.Contains(err.Error(), "tmutil") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("copy_error", func(t *testing.T) {
		tmp := t.TempDir()
		cursorDir := filepath.Join(tmp, "Cursor")
		ramdisk := filepath.Join(tmp, "RAM")
		_ = os.MkdirAll(ramdisk, 0o755)
		_ = os.MkdirAll(filepath.Join(cursorDir, "Cache"), 0o755)

		a := newApp()
		a.cursorDir = cursorDir
		a.ramdisk = ramdisk
		a.stdout = io.Discard
		a.stderr = io.Discard
		a.dirs = []string{"Cache"}
		a.runOutput = func(name string, args ...string) ([]byte, error) {
			if name == "pgrep" {
				return nil, errors.New("not running")
			}
			if name == "du" && args[0] == "-sm" {
				return []byte("1\t.\n"), nil
			}
			return nil, errors.New("unexpected")
		}
		a.run = func(name string, args ...string) error {
			if name == "tmutil" {
				return nil
			}
			if name == "cp" {
				return errors.New("cp failed")
			}
			return fmt.Errorf("unexpected %s", name)
		}

		err := a.cmdSetup(true)
		if err == nil || !strings.Contains(err.Error(), "copy") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("guard_running", func(t *testing.T) {
		tmp := t.TempDir()
		a := newApp()
		a.cursorDir = filepath.Join(tmp, "Cursor")
		_ = os.MkdirAll(filepath.Join(a.cursorDir, "Cache"), 0o755)
		a.stdout = io.Discard
		a.runOutput = func(name string, args ...string) ([]byte, error) {
			if name == "pgrep" {
				return []byte("1\n"), nil
			}
			return nil, errors.New("unexpected")
		}
		err := a.cmdSetup(true)
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("aborted", func(t *testing.T) {
		tmp := t.TempDir()
		cursorDir := filepath.Join(tmp, "Cursor")
		ramdisk := filepath.Join(tmp, "RAM")
		_ = os.MkdirAll(ramdisk, 0o755)
		_ = os.MkdirAll(filepath.Join(cursorDir, "Cache"), 0o755)

		a := newApp()
		a.cursorDir = cursorDir
		a.ramdisk = ramdisk
		a.stdout = io.Discard
		a.stderr = io.Discard
		a.dirs = []string{"Cache"}
		a.stdin = strings.NewReader("n\n")
		a.runOutput = func(name string, args ...string) ([]byte, error) {
			if name == "pgrep" {
				return nil, errors.New("not running")
			}
			return newApp().runOutput(name, args...)
		}

		err := a.cmdSetup(false)
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestCmdTeardown_Integration(t *testing.T) {
	tmp := t.TempDir()
	cursorDir := filepath.Join(tmp, "Cursor")
	ramdisk := filepath.Join(tmp, "RAM")
	for _, d := range defaultTargetDirs {
		_ = os.MkdirAll(filepath.Join(ramdisk, flattenPath(d)), 0o755)
		full := filepath.Join(cursorDir, filepath.FromSlash(d))
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.Symlink(filepath.Join(ramdisk, flattenPath(d)), full)
	}

	a := newApp()
	a.cursorDir = cursorDir
	a.ramdisk = ramdisk
	a.stdout = io.Discard
	a.stderr = io.Discard
	a.runOutput = func(name string, args ...string) ([]byte, error) {
		if name == "pgrep" {
			return nil, errors.New("not running")
		}
		return newApp().runOutput(name, args...)
	}

	if err := a.cmdTeardown(true); err != nil {
		t.Fatal(err)
	}
}

func TestCmdStatus_CursorDirError(t *testing.T) {
	a := newApp()
	a.cursorDir = ""
	a.userHome = func() (string, error) {
		return "", errors.New("no home")
	}
	a.stdout = io.Discard
	err := a.cmdStatus()
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdSetup_MeasureDirsError(t *testing.T) {
	tmp := t.TempDir()
	cursorDir := filepath.Join(tmp, "Cursor")
	ramdisk := filepath.Join(tmp, "RAM")
	_ = os.MkdirAll(ramdisk, 0o755)
	_ = os.MkdirAll(filepath.Join(cursorDir, "Cache"), 0o755)

	a := newApp()
	a.cursorDir = cursorDir
	a.ramdisk = ramdisk
	a.dirs = []string{"Cache"}
	a.stdout = io.Discard
	a.stderr = io.Discard
	a.runOutput = func(name string, args ...string) ([]byte, error) {
		if name == "pgrep" {
			return nil, errors.New("not running")
		}
		if name == "du" && args[0] == "-sm" {
			return nil, errors.New("du failed")
		}
		return nil, errors.New("unexpected")
	}

	err := a.cmdSetup(true)
	if err == nil || !strings.Contains(err.Error(), "measure") {
		t.Fatalf("got %v", err)
	}
}

func TestCmdSetup_EnsureRamdiskError(t *testing.T) {
	tmp := t.TempDir()
	cursorDir := filepath.Join(tmp, "Cursor")
	ramdisk := filepath.Join(tmp, "RAM")
	_ = os.MkdirAll(filepath.Join(cursorDir, "Cache"), 0o755)

	a := newApp()
	a.cursorDir = cursorDir
	a.ramdisk = ramdisk
	a.dirs = []string{"Cache"}
	a.stdout = io.Discard
	a.stderr = io.Discard
	a.runOutput = func(name string, args ...string) ([]byte, error) {
		if name == "pgrep" {
			return nil, errors.New("not running")
		}
		if name == "du" && args[0] == "-sm" {
			return []byte("1\t.\n"), nil
		}
		if name == "sysctl" && len(args) == 2 && args[1] == "vm.page_free_count" {
			return []byte("524288\n"), nil
		}
		if name == "sysctl" && len(args) == 2 && args[1] == "vm.pagesize" {
			return []byte("16384\n"), nil
		}
		if name == "hdiutil" {
			return nil, errors.New("hdiutil failed")
		}
		return nil, errors.New("unexpected")
	}
	a.run = func(name string, args ...string) error {
		if name == "tmutil" {
			return nil
		}
		return fmt.Errorf("unexpected %s", name)
	}

	err := a.cmdSetup(true)
	if err == nil || !strings.Contains(err.Error(), "hdiutil") {
		t.Fatalf("got %v", err)
	}
}

func TestCmdSetup_RenameFails(t *testing.T) {
	tmp := t.TempDir()
	cursorDir := filepath.Join(tmp, "Cursor")
	ramdisk := filepath.Join(tmp, "RAM")
	_ = os.MkdirAll(ramdisk, 0o755)
	_ = os.MkdirAll(filepath.Join(cursorDir, "Cache"), 0o755)

	a := newApp()
	a.cursorDir = cursorDir
	a.ramdisk = ramdisk
	a.dirs = []string{"Cache"}
	a.stdout = io.Discard
	a.stderr = io.Discard
	a.runOutput = func(name string, args ...string) ([]byte, error) {
		if name == "pgrep" {
			return nil, errors.New("not running")
		}
		if name == "du" && args[0] == "-sm" {
			return []byte("1\t.\n"), nil
		}
		return nil, errors.New("unexpected")
	}
	a.run = func(name string, args ...string) error {
		if name == "tmutil" {
			return nil
		}
		if name == "diskutil" {
			return nil
		}
		if name == "cp" {
			return nil
		}
		return fmt.Errorf("unexpected %s", name)
	}
	a.rename = func(oldpath, newpath string) error {
		return errors.New("rename failed")
	}

	err := a.cmdSetup(true)
	if err == nil || !strings.Contains(err.Error(), "rename") {
		t.Fatalf("got %v", err)
	}
}

func TestCmdSetup_SymlinkFails(t *testing.T) {
	tmp := t.TempDir()
	cursorDir := filepath.Join(tmp, "Cursor")
	ramdisk := filepath.Join(tmp, "RAM")
	_ = os.MkdirAll(ramdisk, 0o755)
	_ = os.MkdirAll(filepath.Join(cursorDir, "Cache"), 0o755)

	a := newApp()
	a.cursorDir = cursorDir
	a.ramdisk = ramdisk
	a.dirs = []string{"Cache"}
	a.stdout = io.Discard
	a.stderr = io.Discard
	a.runOutput = func(name string, args ...string) ([]byte, error) {
		if name == "pgrep" {
			return nil, errors.New("not running")
		}
		if name == "du" && args[0] == "-sm" {
			return []byte("1\t.\n"), nil
		}
		return nil, errors.New("unexpected")
	}
	a.run = func(name string, args ...string) error {
		if name == "tmutil" {
			return nil
		}
		if name == "diskutil" {
			return nil
		}
		if name == "cp" {
			return nil
		}
		return fmt.Errorf("unexpected %s", name)
	}
	a.symlink = func(oldpath, newpath string) error {
		return errors.New("symlink failed")
	}

	err := a.cmdSetup(true)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("got %v", err)
	}
}

func TestCmdTeardown_CursorDirError(t *testing.T) {
	a := newApp()
	a.cursorDir = ""
	a.userHome = func() (string, error) {
		return "", errors.New("no home")
	}
	a.stdout = io.Discard
	err := a.cmdTeardown(true)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCmdTeardown_GuardRunning(t *testing.T) {
	t.Run("running", func(t *testing.T) {
		a := newApp()
		a.cursorDir = t.TempDir()
		a.stdout = io.Discard
		a.runOutput = func(name string, args ...string) ([]byte, error) {
			if name == "pgrep" {
				return []byte("1\n"), nil
			}
			return nil, errors.New("unexpected")
		}
		err := a.cmdTeardown(true)
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestCmdTeardown_NoActiveDirs(t *testing.T) {
	env := newTestEnv(t)
	a := testAppForDoTest(t)
	a.cursorDir = env.cursorDir
	a.ramdisk = env.ramdiskDir
	a.runOutput = func(name string, args ...string) ([]byte, error) {
		if name == "pgrep" {
			return nil, errors.New("not running")
		}
		return newApp().runOutput(name, args...)
	}
	if err := a.cmdTeardown(true); err != nil {
		t.Fatal(err)
	}
}

func TestCmdTeardown_Aborted(t *testing.T) {
	tmp := t.TempDir()
	cursorDir := filepath.Join(tmp, "Cursor")
	ramdisk := filepath.Join(tmp, "RAM")
	_ = os.MkdirAll(filepath.Join(ramdisk, "Cache"), 0o755)
	src := filepath.Join(cursorDir, "Cache")
	_ = os.MkdirAll(filepath.Dir(src), 0o755)
	_ = os.Symlink(filepath.Join(ramdisk, "Cache"), src)

	a := newApp()
	a.cursorDir = cursorDir
	a.ramdisk = ramdisk
	a.dirs = []string{"Cache"}
	a.stdout = io.Discard
	a.stderr = io.Discard
	a.stdin = strings.NewReader("n\n")
	a.runOutput = func(name string, args ...string) ([]byte, error) {
		if name == "pgrep" {
			return nil, errors.New("not running")
		}
		return newApp().runOutput(name, args...)
	}

	if err := a.cmdTeardown(false); err != nil {
		t.Fatal(err)
	}
}

func TestCmdTeardown_RAMGoneRestore(t *testing.T) {
	env := newTestEnv(t)
	for _, d := range defaultTargetDirs {
		if d != "Cache" {
			_ = os.RemoveAll(filepath.Join(env.cursorDir, filepath.FromSlash(d)))
		}
	}
	a := testAppForDoTest(t)
	a.cursorDir = env.cursorDir
	a.ramdisk = env.ramdiskDir
	if err := doTestSetup(t, a, true); err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(filepath.Join(env.ramdiskDir, flattenPath("Cache")))

	a2 := newApp()
	a2.cursorDir = env.cursorDir
	a2.ramdisk = env.ramdiskDir
	a2.stdout = io.Discard
	a2.stderr = io.Discard
	a2.runOutput = func(name string, args ...string) ([]byte, error) {
		if name == "pgrep" {
			return nil, errors.New("not running")
		}
		return newApp().runOutput(name, args...)
	}
	if err := a2.cmdTeardown(true); err != nil {
		t.Fatal(err)
	}
}

func TestCmdTeardown_RAMGoneNoOrig(t *testing.T) {
	env := newTestEnv(t)
	for _, d := range defaultTargetDirs {
		if d != "Cache" {
			_ = os.RemoveAll(filepath.Join(env.cursorDir, filepath.FromSlash(d)))
		}
	}
	a := testAppForDoTest(t)
	a.cursorDir = env.cursorDir
	a.ramdisk = env.ramdiskDir
	if err := doTestSetup(t, a, true); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(env.cursorDir, "Cache")
	_ = os.RemoveAll(src + ".orig")
	_ = os.RemoveAll(filepath.Join(env.ramdiskDir, flattenPath("Cache")))

	a2 := newApp()
	a2.cursorDir = env.cursorDir
	a2.ramdisk = env.ramdiskDir
	a2.stdout = io.Discard
	a2.stderr = io.Discard
	a2.runOutput = func(name string, args ...string) ([]byte, error) {
		if name == "pgrep" {
			return nil, errors.New("not running")
		}
		return newApp().runOutput(name, args...)
	}
	if err := a2.cmdTeardown(true); err != nil {
		t.Fatal(err)
	}
}

func TestCmdTeardown_SyncError(t *testing.T) {
	tmp := t.TempDir()
	cursorDir := filepath.Join(tmp, "Cursor")
	ramdisk := filepath.Join(tmp, "RAM")
	_ = os.MkdirAll(filepath.Join(ramdisk, "Cache"), 0o755)
	src := filepath.Join(cursorDir, "Cache")
	_ = os.MkdirAll(filepath.Dir(src), 0o755)
	_ = os.Symlink(filepath.Join(ramdisk, "Cache"), src)

	a := newApp()
	a.cursorDir = cursorDir
	a.ramdisk = ramdisk
	a.dirs = []string{"Cache"}
	a.stdout = io.Discard
	a.stderr = io.Discard
	a.runOutput = func(name string, args ...string) ([]byte, error) {
		if name == "pgrep" {
			return nil, errors.New("not running")
		}
		return newApp().runOutput(name, args...)
	}
	a.run = func(name string, args ...string) error {
		if name == "rsync" {
			return errors.New("rsync failed")
		}
		return nil
	}

	err := a.cmdTeardown(true)
	if err == nil || !strings.Contains(err.Error(), "sync") {
		t.Fatalf("got %v", err)
	}
}

func TestCmdTeardown_RemoveSymlinkError(t *testing.T) {
	tmp := t.TempDir()
	cursorDir := filepath.Join(tmp, "Cursor")
	ramdisk := filepath.Join(tmp, "RAM")
	_ = os.MkdirAll(filepath.Join(ramdisk, "Cache"), 0o755)
	src := filepath.Join(cursorDir, "Cache")
	_ = os.MkdirAll(filepath.Dir(src), 0o755)
	_ = os.Symlink(filepath.Join(ramdisk, "Cache"), src)
	newDir := src + ".new"
	_ = os.MkdirAll(filepath.Join(newDir, "x"), 0o755)

	a := newApp()
	a.cursorDir = cursorDir
	a.ramdisk = ramdisk
	a.dirs = []string{"Cache"}
	a.stdout = io.Discard
	a.stderr = io.Discard
	a.runOutput = func(name string, args ...string) ([]byte, error) {
		if name == "pgrep" {
			return nil, errors.New("not running")
		}
		return newApp().runOutput(name, args...)
	}
	syncDone := false
	a.run = func(name string, args ...string) error {
		if name == "rsync" {
			syncDone = true
			return nil
		}
		return nil
	}
	a.remove = func(name string) error {
		if syncDone && name == src {
			return errors.New("remove failed")
		}
		return os.Remove(name)
	}

	err := a.cmdTeardown(true)
	if err == nil || !strings.Contains(err.Error(), "remove symlink") {
		t.Fatalf("got %v", err)
	}
}

func TestCmdTeardown_RenameAfterSyncError(t *testing.T) {
	tmp := t.TempDir()
	cursorDir := filepath.Join(tmp, "Cursor")
	ramdisk := filepath.Join(tmp, "RAM")
	_ = os.MkdirAll(filepath.Join(ramdisk, "Cache"), 0o755)
	src := filepath.Join(cursorDir, "Cache")
	_ = os.MkdirAll(filepath.Dir(src), 0o755)
	_ = os.Symlink(filepath.Join(ramdisk, "Cache"), src)
	newDir := src + ".new"
	_ = os.MkdirAll(filepath.Join(newDir, "x"), 0o755)

	a := newApp()
	a.cursorDir = cursorDir
	a.ramdisk = ramdisk
	a.dirs = []string{"Cache"}
	a.stdout = io.Discard
	a.stderr = io.Discard
	a.runOutput = func(name string, args ...string) ([]byte, error) {
		if name == "pgrep" {
			return nil, errors.New("not running")
		}
		return newApp().runOutput(name, args...)
	}
	syncDone := false
	a.run = func(name string, args ...string) error {
		if name == "rsync" {
			syncDone = true
			return nil
		}
		return nil
	}
	a.remove = func(name string) error {
		return os.Remove(name)
	}
	a.rename = func(oldpath, newpath string) error {
		if syncDone && oldpath == newDir {
			return errors.New("rename failed")
		}
		return os.Rename(oldpath, newpath)
	}

	err := a.cmdTeardown(true)
	if err == nil || !strings.Contains(err.Error(), "rename") {
		t.Fatalf("got %v", err)
	}
}

func TestCmdTeardown_RemoveDanglingError(t *testing.T) {
	tmp := t.TempDir()
	cursorDir := filepath.Join(tmp, "Cursor")
	ramdisk := filepath.Join(tmp, "RAM")
	src := filepath.Join(cursorDir, "Cache")
	orig := src + ".orig"
	_ = os.MkdirAll(filepath.Join(orig, "x"), 0o755)
	_ = os.MkdirAll(filepath.Dir(src), 0o755)
	_ = os.Symlink(filepath.Join(ramdisk, "missing"), src)

	a := newApp()
	a.cursorDir = cursorDir
	a.ramdisk = ramdisk
	a.dirs = []string{"Cache"}
	a.stdout = io.Discard
	a.stderr = io.Discard
	a.runOutput = func(name string, args ...string) ([]byte, error) {
		if name == "pgrep" {
			return nil, errors.New("not running")
		}
		return newApp().runOutput(name, args...)
	}
	a.remove = func(name string) error {
		if name == src {
			return errors.New("remove failed")
		}
		return os.Remove(name)
	}

	err := a.cmdTeardown(true)
	if err == nil || !strings.Contains(err.Error(), "remove dangling") {
		t.Fatalf("got %v", err)
	}
}

func TestCmdTeardown_RenameRestoreError(t *testing.T) {
	tmp := t.TempDir()
	cursorDir := filepath.Join(tmp, "Cursor")
	ramdisk := filepath.Join(tmp, "RAM")
	src := filepath.Join(cursorDir, "Cache")
	orig := src + ".orig"
	_ = os.MkdirAll(filepath.Join(orig, "x"), 0o755)
	_ = os.MkdirAll(filepath.Dir(src), 0o755)
	_ = os.Symlink(filepath.Join(ramdisk, "missing"), src)

	a := newApp()
	a.cursorDir = cursorDir
	a.ramdisk = ramdisk
	a.dirs = []string{"Cache"}
	a.stdout = io.Discard
	a.stderr = io.Discard
	a.runOutput = func(name string, args ...string) ([]byte, error) {
		if name == "pgrep" {
			return nil, errors.New("not running")
		}
		return newApp().runOutput(name, args...)
	}
	a.remove = func(name string) error {
		return os.Remove(name)
	}
	a.rename = func(oldpath, newpath string) error {
		if oldpath == orig {
			return errors.New("rename restore failed")
		}
		return os.Rename(oldpath, newpath)
	}

	err := a.cmdTeardown(true)
	if err == nil || !strings.Contains(err.Error(), "restore orig") {
		t.Fatalf("got %v", err)
	}
}

func TestCmdTeardown_RemoveOrigWarn(t *testing.T) {
	env := newTestEnv(t)
	for _, d := range defaultTargetDirs {
		if d != "User/globalStorage" {
			_ = os.RemoveAll(filepath.Join(env.cursorDir, filepath.FromSlash(d)))
		}
	}
	a := testAppForDoTest(t)
	a.cursorDir = env.cursorDir
	a.ramdisk = env.ramdiskDir
	if err := doTestSetup(t, a, true); err != nil {
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

	a2 := newApp()
	a2.cursorDir = env.cursorDir
	a2.ramdisk = env.ramdiskDir
	a2.stdout = io.Discard
	a2.stderr = io.Discard
	a2.runOutput = func(name string, args ...string) ([]byte, error) {
		if name == "pgrep" {
			return nil, errors.New("not running")
		}
		return newApp().runOutput(name, args...)
	}
	if err := a2.cmdTeardown(true); err != nil {
		t.Fatal(err)
	}
}

func TestRunArgs_SetupTeardownCommandErrors(t *testing.T) {
	t.Run("setup_returns_error", func(t *testing.T) {
		a := newApp()
		a.cursorDir = ""
		a.userHome = func() (string, error) {
			return "", errors.New("no home")
		}
		a.stdout = io.Discard
		a.stderr = io.Discard
		if c := a.runArgs([]string{"setup", "-y"}); c != 1 {
			t.Fatalf("exit %d want 1", c)
		}
	})

	t.Run("teardown_returns_error", func(t *testing.T) {
		a := newApp()
		a.cursorDir = ""
		a.userHome = func() (string, error) {
			return "", errors.New("no home")
		}
		a.stdout = io.Discard
		a.stderr = io.Discard
		if c := a.runArgs([]string{"teardown", "-y"}); c != 1 {
			t.Fatalf("exit %d want 1", c)
		}
	})
}

func TestRunArgs_SetupTeardownExit(t *testing.T) {
	tmp := t.TempDir()
	cursorDir := filepath.Join(tmp, "Cursor")
	ramdisk := filepath.Join(tmp, "RAM")
	_ = os.MkdirAll(ramdisk, 0o755)
	_ = os.MkdirAll(filepath.Join(cursorDir, "Cache"), 0o755)

	a := newApp()
	a.cursorDir = cursorDir
	a.ramdisk = ramdisk
	a.stdout = io.Discard
	a.stderr = io.Discard
	a.dirs = []string{"Cache"}
	a.runOutput = func(name string, args ...string) ([]byte, error) {
		if name == "pgrep" {
			return nil, errors.New("not running")
		}
		if name == "du" && args[0] == "-sm" {
			return []byte("1\t.\n"), nil
		}
		return nil, errors.New("unexpected")
	}
	a.run = func(name string, args ...string) error {
		if name == "tmutil" {
			return nil
		}
		if name == "diskutil" {
			return nil
		}
		if name == "cp" {
			return nil
		}
		return fmt.Errorf("unexpected %s", name)
	}

	if c := a.runArgs([]string{"-y", "setup"}); c != 0 {
		t.Fatalf("setup exit %d", c)
	}

	a2 := newApp()
	a2.cursorDir = cursorDir
	a2.ramdisk = ramdisk
	a2.stdout = io.Discard
	a2.stderr = io.Discard
	a2.dirs = []string{"Cache"}
	a2.runOutput = func(name string, args ...string) ([]byte, error) {
		if name == "pgrep" {
			return nil, errors.New("not running")
		}
		return newApp().runOutput(name, args...)
	}
	a2.run = func(name string, args ...string) error {
		if name == "rsync" {
			return nil
		}
		return fmt.Errorf("unexpected %s", name)
	}

	if c := a2.runArgs([]string{"-y", "teardown"}); c != 0 {
		t.Fatalf("teardown exit %d", c)
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
	_, _ = os.Readlink(src)

	info, _ := os.Lstat(src)
	if info.Mode()&fs.ModeSymlink == 0 {
		t.Fatal("expected symlink after first setup")
	}

	_, _ = os.Readlink(src)
}

func TestTeardown_RsyncsLiveStateAndCleansUp(t *testing.T) {
	env := newTestEnv(t)
	dirs := []string{"User/globalStorage", "User/History"}
	env.setup(t, dirs)

	for _, dir := range dirs {
		dest := filepath.Join(env.ramdiskDir, flattenPath(dir))
		_ = os.WriteFile(filepath.Join(dest, "state.db"), []byte("updated:"+dir), 0o644)
		_ = os.WriteFile(filepath.Join(dest, "new-file.txt"), []byte("new"), 0o644)
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
	_ = os.RemoveAll(dest)

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
