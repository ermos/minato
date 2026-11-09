package render

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WriteResult describes what Write changed on disk.
type WriteResult struct {
	// Fresh is true when the deploy folder did not exist yet.
	Fresh bool
	// Changed lists bundle paths created, updated or removed.
	Changed []string
}

// Write syncs the bundle to OutDir.
//
// Existing files are rewritten in place (same inode) so single file bind
// mounts keep working. Only files previously written by minato are removed,
// runtime data created by containers next to the compose file is left alone.
func (b *Bundle) Write() (*WriteResult, error) {
	res := &WriteResult{}
	if _, err := os.Stat(b.OutDir); errors.Is(err, os.ErrNotExist) {
		res.Fresh = true
	}
	if err := os.MkdirAll(b.OutDir, 0o755); err != nil {
		return nil, err
	}

	paths := make([]string, 0, len(b.Files))
	for p := range b.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, p := range paths {
		f := b.Files[p]
		changed, err := writeInPlace(filepath.Join(b.OutDir, filepath.FromSlash(p)), f)
		if err != nil {
			return nil, err
		}
		if changed {
			res.Changed = append(res.Changed, p)
		}
	}

	manifestPath := filepath.Join(b.OutDir, filepath.FromSlash(manifestFile))
	if previous, err := os.ReadFile(manifestPath); err == nil {
		for _, p := range strings.Split(strings.TrimSpace(string(previous)), "\n") {
			if p == "" || strings.Contains(p, "..") {
				continue
			}
			if _, keep := b.Files[p]; keep {
				continue
			}
			full := filepath.Join(b.OutDir, filepath.FromSlash(p))
			if err := os.Remove(full); err == nil {
				res.Changed = append(res.Changed, p)
				removeEmptyParents(b.OutDir, filepath.Dir(full))
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(manifestPath, []byte(strings.Join(paths, "\n")+"\n"), 0o644); err != nil {
		return nil, err
	}
	sort.Strings(res.Changed)
	return res, nil
}

func writeInPlace(path string, f File) (bool, error) {
	current, err := os.ReadFile(path)
	if err == nil {
		info, statErr := os.Stat(path)
		if statErr == nil && bytes.Equal(current, f.Data) && info.Mode().Perm() == f.Mode {
			return false, nil
		}
		// Make read only files (secrets) writable for the update.
		_ = os.Chmod(path, 0o600)
		if err := writeTruncate(path, f.Data); err != nil {
			return false, err
		}
		return true, os.Chmod(path, f.Mode)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, f.Data, f.Mode); err != nil {
		return false, err
	}
	return true, os.Chmod(path, f.Mode)
}

func writeTruncate(path string, data []byte) error {
	fh, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	if _, err := fh.Write(data); err != nil {
		_ = fh.Close()
		return err
	}
	return fh.Close()
}

func removeEmptyParents(root, dir string) {
	for dir != root && strings.HasPrefix(dir, root) {
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}
