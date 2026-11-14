package migration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// StateFileName is kept in the uploaded folder. It starts with a dot, so the walk skips it.
const StateFileName = ".yggy-upload-state.json"

// fileState remembers one file: which version of it was seen and where its upload lives.
type fileState struct {
	Size      int64  `json:"size"`
	MTimeNano int64  `json:"mtimeNano"`
	UploadURL string `json:"uploadUrl"`
	Sha256    string `json:"sha256"`
	Complete  bool   `json:"complete,omitempty"`
}

type stateData struct {
	ProjectID  string               `json:"projectId"`
	SnapshotID string               `json:"snapshotId"`
	Files      map[string]fileState `json:"files"`
}

// stateStore is the resume state of one run. Safe for use from several workers.
type stateStore struct {
	mu   sync.Mutex
	path string
	data stateData
}

// loadState reads the state file; a missing, unreadable or foreign file starts empty.
func loadState(dir, projectID, snapshotID string) *stateStore {
	store := &stateStore{
		path: filepath.Join(dir, StateFileName),
		data: stateData{ProjectID: projectID, SnapshotID: snapshotID, Files: map[string]fileState{}},
	}
	raw, err := os.ReadFile(store.path)
	if err != nil {
		return store
	}
	var saved stateData
	if json.Unmarshal(raw, &saved) != nil || saved.ProjectID != projectID || saved.SnapshotID != snapshotID || saved.Files == nil {
		return store
	}
	store.data = saved
	return store
}

// get returns the entry for path when it still describes the same file version.
func (s *stateStore) get(path string, size, mtimeNano int64) (fileState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.data.Files[path]
	if !ok || entry.Size != size || entry.MTimeNano != mtimeNano {
		return fileState{}, false
	}
	return entry, true
}

func (s *stateStore) put(path string, entry fileState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Files[path] = entry
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".yggy-upload-state-*.tmp")
	if err != nil {
		return fmt.Errorf("save the resume state: %w", err)
	}
	_, err = tmp.Write(raw)
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), s.path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("save the resume state: %w", err)
	}
	return nil
}
