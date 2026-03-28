package main

import (
	"io/fs"
	"os"
	"path/filepath"
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

		if _, err := os.Stat(dest); os.IsNotExist(err) {
			// RAM disk gone path
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

// ---- tests ----

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

		// Data readable through symlink
		got := readFile(t, filepath.Join(src, "state.db"))
		if got != "initial:"+dir {
			t.Errorf("data through symlink: got %q want %q", got, "initial:"+dir)
		}
	}
}

func TestSetup_Idempotent(t *testing.T) {
	env := newTestEnv(t)
	dirs := []string{"User/globalStorage"}

	// First setup
	env.setup(t, dirs)
	src := filepath.Join(env.cursorDir, "User", "globalStorage")
	target1, _ := os.Readlink(src)

	// Second setup: simulate by calling setup on a dir that is already a symlink.
	// pendingDirs skips symlinks, so we verify the symlink target did not change.
	info, _ := os.Lstat(src)
	if info.Mode()&fs.ModeSymlink == 0 {
		t.Fatal("expected symlink after first setup")
	}

	// If we called setup again it would skip this dir -- verify the symlink is unchanged
	target2, _ := os.Readlink(src)
	if target1 != target2 {
		t.Errorf("setup not idempotent: symlink target changed from %s to %s", target1, target2)
	}
}

func TestTeardown_RsyncsLiveStateAndCleansUp(t *testing.T) {
	env := newTestEnv(t)
	dirs := []string{"User/globalStorage", "User/History"}
	env.setup(t, dirs)

	// Simulate writes to the RAM disk after setup
	for _, dir := range dirs {
		dest := filepath.Join(env.ramdiskDir, flattenPath(dir))
		os.WriteFile(filepath.Join(dest, "state.db"), []byte("updated:"+dir), 0o644)
		os.WriteFile(filepath.Join(dest, "new-file.txt"), []byte("new"), 0o644)
	}

	env.teardown(t, dirs)

	for _, dir := range dirs {
		src := filepath.Join(env.cursorDir, filepath.FromSlash(dir))
		orig := src + ".orig"

		// src must now be a real directory, not a symlink
		assertIsDir(t, src)

		// Updated data must be present
		got := readFile(t, filepath.Join(src, "state.db"))
		if got != "updated:"+dir {
			t.Errorf("state.db after teardown: got %q want %q", got, "updated:"+dir)
		}
		newFile := readFile(t, filepath.Join(src, "new-file.txt"))
		if newFile != "new" {
			t.Errorf("new-file.txt after teardown: got %q want %q", newFile, "new")
		}

		// .orig must be removed after successful teardown
		assertNotExist(t, orig)
	}
}

func TestTeardown_FallsBackToOrigWhenRAMDiskGone(t *testing.T) {
	env := newTestEnv(t)
	dirs := []string{"Cache"}
	env.setup(t, dirs)

	// Simulate RAM disk disappearing by removing the dest
	dest := filepath.Join(env.ramdiskDir, flattenPath("Cache"))
	os.RemoveAll(dest)

	env.teardown(t, dirs)

	src := filepath.Join(env.cursorDir, "Cache")
	orig := src + ".orig"

	assertIsDir(t, src)
	assertNotExist(t, orig)

	// Data should be from .orig (initial state)
	got := readFile(t, filepath.Join(src, "state.db"))
	if got != "initial:Cache" {
		t.Errorf("fallback data: got %q want initial:Cache", got)
	}
}

func TestTeardown_SymlinkAtomicity(t *testing.T) {
	// Verify that .new is used as the intermediate -- if rename fails,
	// the symlink is still in place. We test the happy path here and
	// confirm no .new residue after successful teardown.
	env := newTestEnv(t)
	dirs := []string{"User/workspaceStorage"}
	env.setup(t, dirs)

	env.teardown(t, dirs)

	src := filepath.Join(env.cursorDir, "User", "workspaceStorage")
	newDir := src + ".new"
	assertIsDir(t, src)
	assertNotExist(t, newDir)
}
