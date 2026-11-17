package migration

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// localFile is one file to upload. Path uses forward slashes, relative to the folder.
type localFile struct {
	Path      string
	Abs       string
	Size      int64
	MTimeNano int64
}

// walk lists the regular files below dir in path order. Hidden files and folders (a leading dot) are skipped,
// which covers .uploads and the resume state file. Symbolic links are skipped.
func walk(dir string) ([]localFile, error) {
	var files []localFile
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == dir {
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files = append(files, localFile{
			Path:      filepath.ToSlash(rel),
			Abs:       path,
			Size:      info.Size(),
			MTimeNano: info.ModTime().UnixNano(),
		})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, err
}
