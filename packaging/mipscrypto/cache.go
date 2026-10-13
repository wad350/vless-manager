package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

const stagingSchema = 1

type stagingFile struct {
	Path   string      `json:"path"`
	Dir    bool        `json:"dir,omitempty"`
	Mode   fs.FileMode `json:"mode"`
	Size   int64       `json:"size,omitempty"`
	SHA256 string      `json:"sha256,omitempty"`
}

type stagingTree struct {
	Name  string        `json:"name"`
	Files []stagingFile `json:"files"`
}

type stagingInputs struct {
	Schema    int           `json:"schema"`
	Version   string        `json:"version"`
	Toolchain string        `json:"toolchain"`
	Trees     []stagingTree `json:"trees"`
}

type stagingManifest struct {
	Inputs stagingInputs `json:"inputs"`
	Key    string        `json:"key"`
	Files  []stagingFile `json:"files"`
}

// The key includes paths (including empty directories), bytes and modes, but
// no checkout paths, timestamps or owner IDs. Generator source and toolchain
// belong in the key because the Poly1305 variant is produced with go/format.
func readStagingInputs(root, upstream string) (stagingInputs, error) {
	inputs := stagingInputs{Schema: stagingSchema, Version: cryptoVersion, Toolchain: runtime.Version()}
	generator := filepath.Join(root, "packaging", "mipscrypto")
	for _, tree := range []struct{ name, path string }{
		{"upstream", upstream},
		{"overlay", filepath.Join(generator, "_overlay")},
		{"aead", filepath.Join(generator, "_aead")},
		{"poly1305", filepath.Join(generator, "_poly1305")},
	} {
		files, err := snapshotTree(tree.path)
		if err != nil {
			return inputs, fmt.Errorf("hash %s input: %w", tree.name, err)
		}
		inputs.Trees = append(inputs.Trees, stagingTree{Name: tree.name, Files: files})
	}
	entries, err := os.ReadDir(generator)
	if err != nil {
		return inputs, err
	}
	var files []stagingFile
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := snapshotFile(filepath.Join(generator, entry.Name()), entry.Name())
		if err != nil {
			return inputs, err
		}
		files = append(files, file)
	}
	if len(files) == 0 {
		return inputs, fmt.Errorf("MIPS crypto generator source not found")
	}
	inputs.Trees = append(inputs.Trees, stagingTree{Name: "generator", Files: files})
	return inputs, nil
}

func stagingKey(inputs stagingInputs) string {
	data, _ := json.Marshal(inputs) // Struct contains only JSON-supported values.
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func snapshotFile(path, relative string) (stagingFile, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return stagingFile{}, err
	}
	result := stagingFile{Path: filepath.ToSlash(relative), Mode: info.Mode().Perm(), Dir: info.IsDir()}
	if info.IsDir() {
		return result, nil
	}
	if !info.Mode().IsRegular() {
		return result, fmt.Errorf("non-regular staging source: %s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return result, err
	}
	hash := sha256.New()
	result.Size, err = io.Copy(hash, file)
	closeErr := file.Close()
	if err != nil {
		return result, err
	}
	if closeErr != nil {
		return result, closeErr
	}
	result.SHA256 = hex.EncodeToString(hash.Sum(nil))
	return result, nil
}

func snapshotTree(root string) ([]stagingFile, error) {
	var files []stagingFile
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		file, err := snapshotFile(path, relative)
		if err != nil {
			return err
		}
		files = append(files, file)
		return nil
	})
	return files, err
}

func stageCryptoTree(root, upstream, dst string) error {
	base := filepath.Join(root, "packaging", "mipscrypto")
	if err := copyTree(upstream, dst); err != nil {
		return err
	}
	if err := copyTree(filepath.Join(base, "_overlay"), filepath.Join(dst, "chacha20")); err != nil {
		return err
	}
	if err := copyTree(filepath.Join(base, "_aead"), filepath.Join(dst, "chacha20poly1305")); err != nil {
		return err
	}
	return stagePoly1305(filepath.Join(dst, "internal", "poly1305"), filepath.Join(base, "_poly1305"))
}

// Caller owns the output lock. Published trees are never edited; corruption
// fails closed instead of replacing a tree another go build may still read.
func cachedStage(out string, inputs stagingInputs, build func(string) error) (string, error) {
	key := stagingKey(inputs)
	cache := filepath.Join(out, "cache-v1")
	if info, err := os.Lstat(cache); err == nil && !info.IsDir() {
		return "", fmt.Errorf("invalid MIPS crypto cache directory: %s", cache)
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	entry := filepath.Join(cache, key)
	module := filepath.Join(entry, "module")
	if info, err := os.Lstat(entry); err == nil {
		if !info.IsDir() {
			return "", fmt.Errorf("invalid MIPS crypto cache entry: %s", entry)
		}
		manifestPath := filepath.Join(entry, "manifest.json")
		manifestInfo, err := os.Lstat(manifestPath)
		if err != nil || !manifestInfo.Mode().IsRegular() || manifestInfo.Size() > 16<<20 {
			return "", fmt.Errorf("invalid MIPS crypto cache manifest; remove %s and retry", entry)
		}
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			return "", fmt.Errorf("read MIPS crypto cache manifest: %w", err)
		}
		var manifest stagingManifest
		if err := json.Unmarshal(data, &manifest); err != nil || manifest.Key != key || stagingKey(manifest.Inputs) != key {
			return "", fmt.Errorf("invalid MIPS crypto cache manifest; remove %s and retry", entry)
		}
		files, err := snapshotTree(module)
		if err != nil || !slices.Equal(files, manifest.Files) {
			return "", fmt.Errorf("corrupt MIPS crypto cache tree; remove %s and retry", entry)
		}
		return module, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(cache, 0755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(cache, ".stage-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	tmpModule := filepath.Join(tmp, "module")
	if err := os.Mkdir(tmpModule, 0755); err != nil {
		return "", err
	}
	if err := build(tmpModule); err != nil {
		return "", err
	}
	// Read-only files prevent accidental in-place patching. Directory owner
	// write permission permits make clean; the exact directory set is verified.
	if err := filepath.WalkDir(tmpModule, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Chmod(path, 0755)
		}
		return os.Chmod(path, 0444)
	}); err != nil {
		return "", err
	}
	files, err := snapshotTree(tmpModule)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(stagingManifest{Inputs: inputs, Key: key, Files: files})
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(tmp, "manifest.json"), data, 0444); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, entry); err != nil {
		return "", fmt.Errorf("publish MIPS crypto cache: %w", err)
	}
	return module, nil
}

func withStagingLock(out string, prepare func() error) error {
	if err := os.MkdirAll(out, 0755); err != nil {
		return err
	}
	lockPath := filepath.Join(out, ".staging.lock")
	if info, err := os.Lstat(lockPath); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("invalid MIPS crypto lock file: %s", lockPath)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	unlock, err := lockStaging(file)
	if err != nil {
		return err
	}
	defer unlock()
	return prepare()
}

// Prepare both files privately before serialized, atomic replacements.
// Concurrent callers must use the same root manifests for a shared BUILD_DIR;
// a go build reading crypto.mod after this process exits does not hold our lock.
func writeRootModules(root, out, staged string) error {
	tmp, err := os.MkdirTemp(out, ".modules-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	for _, name := range []string{"go.mod", "go.sum"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(tmp, "crypto."+name[3:]), data, 0644); err != nil {
			return err
		}
	}
	cmd := exec.Command("go", "mod", "edit", "-modfile="+filepath.Join(tmp, "crypto.mod"), "-replace=golang.org/x/crypto="+staged)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("stage module replacement: %w: %s", err, output)
	}
	for _, name := range []string{"crypto.sum", "crypto.mod"} {
		data, err := os.ReadFile(filepath.Join(tmp, name))
		if err != nil {
			return err
		}
		current, readErr := os.ReadFile(filepath.Join(out, name))
		if readErr == nil && string(current) == string(data) {
			continue
		}
		if err := os.Rename(filepath.Join(tmp, name), filepath.Join(out, name)); err != nil {
			return err
		}
	}
	return nil
}

// Resolve existing ancestors as well as an existing output directory, so an
// output symlink cannot redirect staging into GOMODCACHE.
func canonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) || filepath.Dir(absolute) == absolute {
		return "", err
	}
	parent, err := canonicalPath(filepath.Dir(absolute))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(absolute)), nil
}

func outsideModuleCache(out, cache string) error {
	output, err := canonicalPath(out)
	if err != nil {
		return err
	}
	moduleCache, err := canonicalPath(cache)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(moduleCache, output)
	if err != nil || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("staging directory must be outside the module cache")
	}
	return nil
}
