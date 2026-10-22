package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func cacheFixture(t *testing.T) (root, upstream, out string) {
	t.Helper()
	root, upstream, out = t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "out")
	for path, source := range map[string]string{
		"go.mod":                       "module cachefixture\ngo 1.27.3\nrequire golang.org/x/crypto v0.58.0\n",
		"go.sum":                       "fixture-sum\n",
		"packaging/mipscrypto/main.go": "package main\n// generator input\n",
		"packaging/mipscrypto/_overlay/overlay.go":       "package chacha20\n",
		"packaging/mipscrypto/_aead/aead_test.go":        "package chacha20poly1305\n",
		"packaging/mipscrypto/_poly1305/update_noasm.go": "package poly1305\n",
	} {
		writeFixtureFile(t, filepath.Join(root, path), source)
	}
	writeFixtureFile(t, filepath.Join(upstream, "go.mod"), "module golang.org/x/crypto\n")
	writeFixtureFile(t, filepath.Join(upstream, "chacha20/upstream.go"), "package chacha20\n")
	writeFixtureFile(t, filepath.Join(upstream, "internal/poly1305/sum_generic.go"), polyProductsFixture)
	return root, upstream, out
}

func writeFixtureFile(t *testing.T, path, source string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
}

func fixtureStage(root, upstream, out string, builds *atomic.Int32) (string, error) {
	var staged string
	err := withStagingLock(out, func() error {
		inputs, err := readStagingInputs(root, upstream)
		if err != nil {
			return err
		}
		staged, err = cachedStage(out, inputs, func(dst string) error {
			builds.Add(1)
			return stageCryptoTree(root, upstream, dst)
		})
		return err
	})
	return staged, err
}

func TestCachedStageReuseAndRemovedOverlay(t *testing.T) {
	root, upstream, out := cacheFixture(t)
	removed := filepath.Join(root, "packaging/mipscrypto/_overlay/removed.go")
	writeFixtureFile(t, removed, "package chacha20\n// removed overlay\n")
	var builds atomic.Int32
	first, err := fixtureStage(root, upstream, out, &builds)
	if err != nil {
		t.Fatal(err)
	}
	before, err := snapshotTree(out)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(first, "chacha20/overlay.go"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixtureStage(root, upstream, out, &builds)
	if err != nil {
		t.Fatal(err)
	}
	after, err := snapshotTree(out)
	if err != nil || !reflect.DeepEqual(before, after) || first != second || builds.Load() != 1 {
		t.Fatalf("reuse changed the cache: %q / %q; builds=%d; err=%v", first, second, builds.Load(), err)
	}
	reusedInfo, err := os.Stat(filepath.Join(second, "chacha20/overlay.go"))
	if err != nil || !os.SameFile(info, reusedInfo) || !info.ModTime().Equal(reusedInfo.ModTime()) {
		t.Fatal("reuse rewrote the published file", err)
	}
	upstreamInfo, err := os.Stat(filepath.Join(upstream, "chacha20/upstream.go"))
	if err != nil {
		t.Fatal(err)
	}
	stagedInfo, err := os.Stat(filepath.Join(first, "chacha20/upstream.go"))
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(upstreamInfo, stagedInfo) {
		t.Fatal("staging must copy, not hardlink, upstream files")
	}
	if err := os.Remove(removed); err != nil {
		t.Fatal(err)
	}
	third, err := fixtureStage(root, upstream, out, &builds)
	if err != nil || third == first || builds.Load() != 2 {
		t.Fatalf("removed input did not invalidate: %q; builds=%d; err=%v", third, builds.Load(), err)
	}
	if _, err := os.Stat(filepath.Join(third, "chacha20/removed.go")); !os.IsNotExist(err) {
		t.Fatal("removed overlay survived in the new cache tree", err)
	}
	if _, err := os.Stat(filepath.Join(first, "chacha20/removed.go")); err != nil {
		t.Fatal("publishing a new key mutated an old tree", err)
	}
}

func TestStagingKeyCoversEveryInput(t *testing.T) {
	for _, path := range []string{
		"upstream/chacha20/upstream.go",
		"upstream/unrelated/testdata/data.bin",
		"generator/main.go",
		"generator/another.go",
		"overlay/overlay.go",
		"aead/aead_test.go",
		"poly1305/update_noasm.go",
	} {
		t.Run(path, func(t *testing.T) {
			root, upstream, _ := cacheFixture(t)
			before, err := readStagingInputs(root, upstream)
			if err != nil {
				t.Fatal(err)
			}
			parts := strings.SplitN(path, "/", 2)
			base := map[string]string{"upstream": upstream, "generator": filepath.Join(root, "packaging/mipscrypto"), "overlay": filepath.Join(root, "packaging/mipscrypto/_overlay"), "aead": filepath.Join(root, "packaging/mipscrypto/_aead"), "poly1305": filepath.Join(root, "packaging/mipscrypto/_poly1305")}[parts[0]]
			writeFixtureFile(t, filepath.Join(base, parts[1]), "changed input\n")
			after, err := readStagingInputs(root, upstream)
			if err != nil || stagingKey(before) == stagingKey(after) {
				t.Fatal("input change did not invalidate key", err)
			}
		})
	}
	root, upstream, _ := cacheFixture(t)
	inputs, err := readStagingInputs(root, upstream)
	if err != nil {
		t.Fatal(err)
	}
	original := stagingKey(inputs)
	for _, change := range []func(*stagingInputs){
		func(i *stagingInputs) { i.Schema++ },
		func(i *stagingInputs) { i.Version = "other-version" },
		func(i *stagingInputs) { i.Toolchain = "other-toolchain" },
	} {
		altered := inputs
		change(&altered)
		if stagingKey(altered) == original {
			t.Fatal("schema/version/toolchain change did not invalidate key")
		}
	}
	file := filepath.Join(upstream, "chacha20/upstream.go")
	if err := os.Chtimes(file, time.Unix(1, 0), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	timestampChanged, err := readStagingInputs(root, upstream)
	if err != nil || stagingKey(timestampChanged) != original {
		t.Fatal("timestamp invalidated content key", err)
	}
}

func TestCachedStageRejectsCorruption(t *testing.T) {
	for _, corruption := range []string{"content", "extra_file", "missing_file", "manifest", "symlink"} {
		t.Run(corruption, func(t *testing.T) {
			root, upstream, out := cacheFixture(t)
			var builds atomic.Int32
			staged, err := fixtureStage(root, upstream, out, &builds)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(staged, "chacha20/overlay.go")
			switch corruption {
			case "content":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
				writeFixtureFile(t, path, "corrupted\n")
			case "extra_file":
				writeFixtureFile(t, filepath.Join(staged, "extra.go"), "stale\n")
			case "missing_file":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "manifest":
				path = filepath.Join(filepath.Dir(staged), "manifest.json")
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
				writeFixtureFile(t, path, "{}\n")
			case "symlink":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(upstream, "chacha20/upstream.go"), path); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := fixtureStage(root, upstream, out, &builds); err == nil || builds.Load() != 1 {
				t.Fatalf("corruption was reused or overwritten: builds=%d err=%v", builds.Load(), err)
			}
		})
	}
}

func TestCachedStageFailedBuildDoesNotPublish(t *testing.T) {
	root, upstream, out := cacheFixture(t)
	inputs, err := readStagingInputs(root, upstream)
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("interrupted staging")
	err = withStagingLock(out, func() error {
		_, err := cachedStage(out, inputs, func(dst string) error {
			writeFixtureFile(t, filepath.Join(dst, "partial.go"), "partial\n")
			return want
		})
		return err
	})
	if !errors.Is(err, want) {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(out, "cache-v1"))
	if err != nil || len(entries) != 0 {
		t.Fatal("failed staging left a published/temporary entry", entries, err)
	}
}

func TestCachedStageConcurrentPublication(t *testing.T) {
	root, upstream, out := cacheFixture(t)
	var builds atomic.Int32
	const count = 8
	paths := make(chan string, count)
	errors := make(chan error, count)
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() {
			path, err := fixtureStage(root, upstream, out, &builds)
			paths <- path
			errors <- err
		})
	}
	wg.Wait()
	close(paths)
	close(errors)
	var first string
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	for path := range paths {
		if first == "" {
			first = path
		} else if path != first {
			t.Fatal("concurrent publications returned different paths")
		}
	}
	entries, err := os.ReadDir(filepath.Join(out, "cache-v1"))
	if err != nil || len(entries) != 1 || builds.Load() != 1 {
		t.Fatalf("publication entries=%v builds=%d err=%v", entries, builds.Load(), err)
	}
}

func TestRootModulesRefreshAndConcurrentWriters(t *testing.T) {
	root, upstream, out := cacheFixture(t)
	var builds atomic.Int32
	staged, err := fixtureStage(root, upstream, out, &builds)
	if err != nil {
		t.Fatal(err)
	}
	refresh := func() error { return withStagingLock(out, func() error { return writeRootModules(root, out, staged) }) }
	if err := refresh(); err != nil {
		t.Fatal(err)
	}
	mod, err := os.ReadFile(filepath.Join(out, "crypto.mod"))
	if err != nil || !strings.Contains(string(mod), "=> "+staged) {
		t.Fatal("replacement missing from module file", err)
	}
	writeFixtureFile(t, filepath.Join(root, "go.mod"), "module updatedfixture\ngo 1.27.3\nrequire golang.org/x/crypto v0.58.0\n")
	writeFixtureFile(t, filepath.Join(root, "go.sum"), "new-fixture-sum\n")
	const count = 4
	var wg sync.WaitGroup
	errors := make(chan error, count)
	for range count {
		wg.Go(func() { errors <- refresh() })
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	mod, err = os.ReadFile(filepath.Join(out, "crypto.mod"))
	if err != nil || !strings.Contains(string(mod), "module updatedfixture") || !strings.Contains(string(mod), "=> "+staged) {
		t.Fatal("root module update was lost or partial", err)
	}
	sum, err := os.ReadFile(filepath.Join(out, "crypto.sum"))
	if err != nil || string(sum) != "new-fixture-sum\n" {
		t.Fatal("root sum update was lost or partial", err)
	}
	for _, name := range []string{"crypto.mod", "crypto.sum"} {
		info, err := os.Stat(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := refresh(); err != nil {
			t.Fatal(err)
		}
		after, err := os.Stat(filepath.Join(out, name))
		if err != nil || !os.SameFile(info, after) || !info.ModTime().Equal(after.ModTime()) {
			t.Fatal("unchanged root module file was rewritten", err)
		}
	}
}

func TestOutsideModuleCacheResolvesSymlinks(t *testing.T) {
	cache, outside := t.TempDir(), t.TempDir()
	for _, out := range []string{cache, filepath.Join(cache, "not-created", "stage")} {
		if err := outsideModuleCache(out, cache); err == nil {
			t.Fatal("module-cache output allowed", out)
		}
	}
	link := filepath.Join(outside, "cache-link")
	if err := os.Symlink(cache, link); err != nil {
		t.Fatal(err)
	}
	if err := outsideModuleCache(filepath.Join(link, "not-created", "stage"), cache); err == nil {
		t.Fatal("symlink redirected staging into module cache")
	}
	if err := outsideModuleCache(filepath.Join(outside, "safe-stage"), cache); err != nil {
		t.Fatal("outside staging rejected", err)
	}
}

func TestStagingRejectsOutputSymlinks(t *testing.T) {
	for _, name := range []string{"cache-v1", ".staging.lock"} {
		t.Run(name, func(t *testing.T) {
			root, upstream, out := cacheFixture(t)
			target := t.TempDir()
			if err := os.MkdirAll(out, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(out, name)); err != nil {
				t.Fatal(err)
			}
			var builds atomic.Int32
			if _, err := fixtureStage(root, upstream, out, &builds); err == nil || builds.Load() != 0 {
				t.Fatal("output symlink followed", err)
			}
			entries, err := os.ReadDir(target)
			if err != nil || len(entries) != 0 {
				t.Fatal("output symlink target was modified", entries, err)
			}
		})
	}
}

func TestStagingLockAcrossProcesses(t *testing.T) {
	if os.Getenv("MIPS_CACHE_LOCK_HELPER") == "1" {
		out := os.Getenv("MIPS_CACHE_LOCK_OUT")
		if err := os.WriteFile(filepath.Join(out, "child-ready"), []byte("ready"), 0644); err != nil {
			t.Fatal(err)
		}
		err := withStagingLock(out, func() error {
			return os.WriteFile(filepath.Join(out, "child-acquired"), []byte("ready"), 0644)
		})
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	out := t.TempDir()
	var cmd *exec.Cmd
	err := withStagingLock(out, func() error {
		cmd = exec.Command(os.Args[0], "-test.run=^TestStagingLockAcrossProcesses$")
		cmd.Env = append(os.Environ(), "MIPS_CACHE_LOCK_HELPER=1", "MIPS_CACHE_LOCK_OUT="+out)
		if err := cmd.Start(); err != nil {
			return err
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(out, "child-ready")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				return errors.New("lock helper process did not become ready")
			}
			time.Sleep(time.Millisecond)
		}
		// A process-side marker distinguishes a real lock from in-process mutexes.
		time.Sleep(50 * time.Millisecond)
		if _, err := os.Stat(filepath.Join(out, "child-acquired")); !os.IsNotExist(err) {
			return errors.New("child acquired staging lock before its owner released it")
		}
		return nil
	})
	if err != nil {
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatal("child did not acquire released staging lock")
	}
	if _, err := os.Stat(filepath.Join(out, "child-acquired")); err != nil {
		t.Fatal(err)
	}
}
