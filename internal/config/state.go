package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// HostState is the per-(working copy, backend) record in .recom/state/<host>.json.
type HostState struct {
	Host        string    `json:"host"`
	ReplicaPath string    `json:"replica_path"`
	LastSync    time.Time `json:"last_sync,omitzero"`
	LastUpload  int       `json:"last_upload_files"`
	LastRunID   string    `json:"last_run_id,omitempty"`
	LastCommand string    `json:"last_command,omitempty"`
	LastExit    *int      `json:"last_exit,omitempty"`
	LastOutcome string    `json:"last_outcome,omitempty"`
	LastRunAt   time.Time `json:"last_run_at,omitzero"`
}

func (w *Workspace) statePath(host string) string { return w.LocalPath("state", host+".json") }

// LoadHostState returns the stored state (empty if none).
func (w *Workspace) LoadHostState(host string) *HostState {
	st := &HostState{Host: host}
	b, err := os.ReadFile(w.statePath(host))
	if err == nil {
		_ = json.Unmarshal(b, st)
	}
	return st
}

// SaveHostState stores the state atomically.
func (w *Workspace) SaveHostState(st *HostState) error {
	if err := os.MkdirAll(w.LocalPath("state"), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(w.statePath(st.Host), b, 0o600)
}

// Lock is an exclusive advisory lock on a file.
type Lock struct{ f *os.File }

// LockLocal takes the working copy's local install lock for host, waiting
// up to timeout. It serializes output installation between concurrent recom
// processes in the same working copy.
func (w *Workspace) LockLocal(host string, timeout time.Duration) (*Lock, error) {
	if err := os.MkdirAll(w.LocalPath("state"), 0o700); err != nil {
		return nil, err
	}
	p := w.LocalPath("state", host+".lock")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &Lock{f}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, err
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("another recom process is collecting outputs in this project (%s)", filepath.Dir(p))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Unlock releases the lock.
func (l *Lock) Unlock() {
	if l != nil && l.f != nil {
		_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
		l.f.Close()
	}
}
