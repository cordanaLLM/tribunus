package releasewatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type SeenFile struct {
	Version int               `json:"version"`
	Seen    map[string]string `json:"seen"`
}

type State struct {
	mu       sync.Mutex
	stateDir string
	seen     map[string]string
}

func LoadState(stateDir string) (*State, error) {
	if stateDir == "" {
		return nil, errors.New("state_dir is required")
	}
	s := &State{
		stateDir: stateDir,
		seen:     make(map[string]string),
	}
	filePath := filepath.Join(stateDir, SeenFileName)
	// #nosec G304 -- path is derived from operator-configured state directory and fixed filename.
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read seen-set %s: %w", filePath, err)
	}
	var sf SeenFile
	if err = json.Unmarshal(data, &sf); err != nil {
		return nil, fmt.Errorf("parse seen-set %s: %w", filePath, err)
	}
	if sf.Seen != nil {
		s.seen = sf.Seen
	}
	return s, nil
}

func (s *State) Has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.seen[id]
	return ok
}

func (s *State) MarkSeen(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.seen[id]; !ok {
		s.seen[id] = time.Now().UTC().Format(time.RFC3339)
	}
}

func (s *State) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seen)
}

func (s *State) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.stateDir, 0o750); err != nil {
		return fmt.Errorf("create state_dir %s: %w", s.stateDir, err)
	}
	s.pruneSeenLocked()

	sf := SeenFile{
		Version: 1,
		Seen:    s.seen,
	}
	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal seen-set: %w", err)
	}
	data = append(data, '\n')
	return writeTempFileAtomically(s.stateDir, SeenFileName, data)
}

func (s *State) pruneSeenLocked() {
	if len(s.seen) <= MaxSeenRecords {
		return
	}
	type entry struct {
		id   string
		seen time.Time
	}
	entries := make([]entry, 0, len(s.seen))
	for id, tsStr := range s.seen {
		t, err := time.Parse(time.RFC3339, tsStr)
		if err != nil {
			t = time.Unix(0, 0)
		}
		entries = append(entries, entry{id: id, seen: t})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].seen.Before(entries[j].seen)
	})
	dropCount := len(entries) - MaxSeenRecords
	for i := 0; i < dropCount; i++ {
		delete(s.seen, entries[i].id)
	}
}

func writeTempFileAtomically(dir string, filename string, data []byte) (err error) {
	tempFile, err := os.CreateTemp(dir, filename+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tempPath := tempFile.Name()
	defer func() {
		if err != nil {
			if remErr := os.Remove(tempPath); remErr != nil && !os.IsNotExist(remErr) {
				err = errors.Join(err, remErr)
			}
		}
	}()
	if _, writeErr := tempFile.Write(data); writeErr != nil {
		if closeErr := tempFile.Close(); closeErr != nil {
			return errors.Join(fmt.Errorf("write temp file %s: %w", tempPath, writeErr), closeErr)
		}
		return fmt.Errorf("write temp file %s: %w", tempPath, writeErr)
	}
	if syncErr := tempFile.Sync(); syncErr != nil {
		if closeErr := tempFile.Close(); closeErr != nil {
			return errors.Join(fmt.Errorf("fsync temp file %s: %w", tempPath, syncErr), closeErr)
		}
		return fmt.Errorf("fsync temp file %s: %w", tempPath, syncErr)
	}
	if closeErr := tempFile.Close(); closeErr != nil {
		return fmt.Errorf("close temp file %s: %w", tempPath, closeErr)
	}
	finalPath := filepath.Join(dir, filename)
	if renErr := os.Rename(tempPath, finalPath); renErr != nil {
		return fmt.Errorf("rename %s to %s: %w", tempPath, finalPath, renErr)
	}
	return nil
}
