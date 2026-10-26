package vpn

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// failingResolverFS wraps the real file system and fails one named step, to prove the cleanup.
type failingResolverFS struct {
	osResolverFS
	failAt string // create | write | chmod | close | rename
}

var errInjected = errors.New("injected failure")

func (f failingResolverFS) CreateTemp(dir, pattern string) (resolverTempFile, error) {
	if f.failAt == "create" {
		return nil, errInjected
	}
	tmp, err := f.osResolverFS.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	return failingTempFile{resolverTempFile: tmp, failAt: f.failAt}, nil
}

func (f failingResolverFS) Rename(oldPath, newPath string) error {
	if f.failAt == "rename" {
		return errInjected
	}
	return f.osResolverFS.Rename(oldPath, newPath)
}

type failingTempFile struct {
	resolverTempFile
	failAt string
}

func (f failingTempFile) Write(p []byte) (int, error) {
	if f.failAt == "write" {
		return 0, errInjected
	}
	return f.resolverTempFile.Write(p)
}

func (f failingTempFile) Chmod(m os.FileMode) error {
	if f.failAt == "chmod" {
		return errInjected
	}
	return f.resolverTempFile.Chmod(m)
}

func (f failingTempFile) Close() error {
	err := f.resolverTempFile.Close()
	if f.failAt == "close" {
		return errInjected
	}
	return err
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// Every failing step leaves the old destination as it was and no temp file behind.
func TestWriteFileAtomicCleansUpAndKeepsTheOldFileWhenAStepFails(t *testing.T) {
	for _, step := range []string{"create", "write", "chmod", "close", "rename"} {
		t.Run(step, func(t *testing.T) {
			root := t.TempDir()
			resolverDir := filepath.Join(root, "resolver")
			if err := os.Mkdir(resolverDir, 0o755); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(resolverDir, "zone.example.test")
			if err := os.WriteFile(dest, []byte("old\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			err := writeFileAtomic(failingResolverFS{failAt: step}, root, dest, []byte("new\n"), 0o644)

			if !errors.Is(err, errInjected) {
				t.Fatalf("err = %v, want the injected failure", err)
			}
			if got, _ := os.ReadFile(dest); string(got) != "old\n" {
				t.Errorf("destination = %q, want the old content untouched", got)
			}
			for _, dir := range []string{root, resolverDir} {
				for _, n := range names(t, dir) {
					if strings.HasPrefix(n, resolverTempPrefix) {
						t.Errorf("temp file %s left behind in %s", n, dir)
					}
				}
			}
		})
	}
}

func TestWriteFileAtomicWritesTheContentWithMode0644(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "resolver", "zone.example.test")
	if err := os.Mkdir(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	content := resolverMarker + "\nnameserver 192.0.2.53\n"

	if err := writeFileAtomic(osResolverFS{}, root, dest, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(dest)
	if err != nil || string(got) != content {
		t.Fatalf("content = %q, %v", got, err)
	}
	if !isManagedResolverFile(got) {
		t.Error("the written file must carry the managed marker")
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(dest)
		if info.Mode().Perm() != 0o644 {
			t.Errorf("mode = %v, want 0644", info.Mode().Perm())
		}
	}
}

// The temp file is staged outside the directory a service watches: the watched directory only ever
// lists the destination.
func TestWriteFileAtomicNeverStagesInsideTheWatchedDirectory(t *testing.T) {
	root := t.TempDir()
	watched := filepath.Join(root, "resolver")
	if err := os.Mkdir(watched, 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(watched, "zone.example.test")

	var stop atomic.Bool
	var wg sync.WaitGroup
	var foreign atomic.Value
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			entries, err := os.ReadDir(watched)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.Name() != "zone.example.test" {
					foreign.CompareAndSwap(nil, e.Name())
				}
			}
		}
	}()
	for i := 0; i < 500; i++ {
		if err := writeFileAtomic(osResolverFS{}, root, dest, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stop.Store(true)
	wg.Wait()

	if v := foreign.Load(); v != nil {
		t.Fatalf("the watched directory listed %v besides the destination", v)
	}
}

func TestRemoveStaleResolverTempsRemovesOnlyOurTempFiles(t *testing.T) {
	root := t.TempDir()
	stale := filepath.Join(root, resolverTempPrefix+"123.tmp")
	other := filepath.Join(root, "hosts")
	resolverDir := filepath.Join(root, "resolver")
	for _, p := range []string{stale, other} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(resolverDir, 0o755); err != nil {
		t.Fatal(err)
	}

	removeStaleResolverTemps(osResolverFS{}, root)

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("the stale temp file should be gone")
	}
	for _, kept := range []string{other, resolverDir} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s must stay: %v", kept, err)
		}
	}
	removeStaleResolverTemps(osResolverFS{}, filepath.Join(root, "absent")) // quiet on a missing dir
}
