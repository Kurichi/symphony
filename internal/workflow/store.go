package workflow

import (
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/fsnotify/fsnotify"
)

// Store watches a WORKFLOW.md file and keeps the latest successfully parsed
// LoadedWorkflow available via a lock-free atomic pointer. On every write event
// the file is re-parsed; if parsing fails the last known good value is retained.
type Store struct {
	current atomic.Pointer[LoadedWorkflow]
	path    string
	watcher *fsnotify.Watcher
	done    chan struct{}
}

// NewStore loads the initial workflow from path and starts an fsnotify watcher.
// The caller must call Close when the Store is no longer needed.
func NewStore(path string) (*Store, error) {
	wf, err := LoadFile(path)
	if err != nil {
		return nil, fmt.Errorf("workflow store: initial load: %w", err)
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("workflow store: create watcher: %w", err)
	}

	s := &Store{
		path:    path,
		watcher: w,
		done:    make(chan struct{}),
	}
	s.current.Store(wf)

	if err := w.Add(path); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("workflow store: watch %s: %w", path, err)
	}

	go s.loop()
	return s, nil
}

// Current returns the most recently parsed workflow. It never returns nil after
// a successful NewStore call.
func (s *Store) Current() (*LoadedWorkflow, error) {
	wf := s.current.Load()
	if wf == nil {
		return nil, fmt.Errorf("workflow store: no workflow loaded")
	}
	return wf, nil
}

// Close stops the file watcher and background goroutine.
func (s *Store) Close() error {
	err := s.watcher.Close()
	<-s.done
	return err
}

// loop processes fsnotify events until the watcher is closed.
func (s *Store) loop() {
	defer close(s.done)
	for {
		select {
		case event, ok := <-s.watcher.Events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
				s.reload()
			}
		case err, ok := <-s.watcher.Errors:
			if !ok {
				return
			}
			slog.Error("workflow store: watcher error", "error", err)
		}
	}
}

// reload re-reads and parses the file, swapping the atomic pointer on success.
func (s *Store) reload() {
	wf, err := LoadFile(s.path)
	if err != nil {
		slog.Error("workflow store: reload failed, keeping last known good config",
			"path", s.path, "error", err)
		return
	}
	s.current.Store(wf)
	slog.Debug("workflow store: reloaded", "path", s.path)
}
