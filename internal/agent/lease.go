package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/chaseungjoon/recom/internal/protocol"
)

// The replica lease serializes everything that mutates a replica or attributes
// outputs to a run: upload, pre-run snapshot, execution, post-run snapshot and
// output collection. It is a small JSON record (lease.json) guarded by a short
// flock mutex (state.lock). A holder is identified by PID + process start
// time, so a crashed holder (kill -9, reboot) is detected as stale instead of
// blocking the replica forever.

const launchGrace = 60 * time.Second

func leasePath(stateDir string) string { return filepath.Join(stateDir, "lease.json") }

// withMutex runs fn while holding the replica state mutex.
func withMutex(stateDir string, fn func() error) error {
	f, err := os.OpenFile(filepath.Join(stateDir, "state.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("lock state: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
	return fn()
}

func readLease(stateDir string) (*protocol.LeaseInfo, error) {
	var h protocol.LeaseInfo
	err := readJSON(leasePath(stateDir), &h)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		// A corrupt lease is treated as held (fail closed) but reported.
		return &protocol.LeaseInfo{Kind: "unknown", ID: "corrupt", Owner: err.Error(), Since: time.Now()}, nil
	}
	return &h, nil
}

// leaseAlive reports whether the holder process still exists.
func leaseAlive(h *protocol.LeaseInfo) bool {
	if h == nil {
		return false
	}
	if h.Kind == "unknown" {
		return true
	}
	if h.PID <= 0 {
		return time.Since(h.Since) < launchGrace
	}
	start, ok := procStart(h.PID)
	if !ok {
		return false
	}
	return h.Start == 0 || start == h.Start
}

// selfLease builds a lease record for the current process.
func selfLease(kind, id, owner, command string) protocol.LeaseInfo {
	pid := os.Getpid()
	start, _ := procStart(pid)
	return protocol.LeaseInfo{Kind: kind, ID: id, PID: pid, Start: start, Owner: owner, Command: command, Since: time.Now().UTC()}
}

// errBusy is returned when the lease is held by someone else.
type errBusy struct{ holder *protocol.LeaseInfo }

func (e errBusy) Error() string { return "replica busy: " + describeHolder(e.holder) }

func describeHolder(h *protocol.LeaseInfo) string {
	if h == nil {
		return "unknown holder"
	}
	switch h.Kind {
	case "job":
		return fmt.Sprintf("detached job %s (%s) started %s", h.ID, h.Command, h.Since.Local().Format(time.DateTime))
	case "run":
		return fmt.Sprintf("run %s (%s) by %s since %s", h.ID, h.Command, h.Owner, h.Since.Local().Format(time.DateTime))
	default:
		return h.Owner
	}
}

// acquireLease takes the replica lease. If wait is set, it blocks while a
// foreground run holds it (calling onWait once); a detached job always
// returns errBusy immediately. When a stale job lease is replaced, its
// holder is returned so the caller can finalize the lost job.
func acquireLease(ctx context.Context, stateDir string, me protocol.LeaseInfo, wait bool, onWait func(*protocol.LeaseInfo)) (stale *protocol.LeaseInfo, err error) {
	notified := false
	for {
		var busy *protocol.LeaseInfo
		err = withMutex(stateDir, func() error {
			h, err := readLease(stateDir)
			if err != nil {
				return err
			}
			if h != nil && holderAlive(stateDir, h) {
				busy = h
				return nil
			}
			if h != nil && h.Kind == "job" {
				stale = h
			}
			return writeJSONAtomic(leasePath(stateDir), me)
		})
		if err != nil {
			return nil, err
		}
		if busy == nil {
			return stale, nil
		}
		if !wait || busy.Kind != "run" {
			return nil, errBusy{busy}
		}
		if !notified && onWait != nil {
			onWait(busy)
			notified = true
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// releaseLease drops the lease if it is still held under id.
func releaseLease(stateDir, id string) error {
	return withMutex(stateDir, func() error {
		h, err := readLease(stateDir)
		if err != nil || h == nil || h.ID != id {
			return err
		}
		return os.Remove(leasePath(stateDir))
	})
}

// transferLease rewrites the lease (same id or from run to job) to a new holder.
func transferLease(stateDir, fromID string, to protocol.LeaseInfo) error {
	return withMutex(stateDir, func() error {
		h, err := readLease(stateDir)
		if err != nil {
			return err
		}
		if h == nil || (h.ID != fromID && h.ID != to.ID) {
			return fmt.Errorf("lease no longer held by %s", fromID)
		}
		return writeJSONAtomic(leasePath(stateDir), to)
	})
}

// currentHolder returns the live lease holder, if any.
func currentHolder(stateDir string) *protocol.LeaseInfo {
	var out *protocol.LeaseInfo
	_ = withMutex(stateDir, func() error {
		h, err := readLease(stateDir)
		if err == nil && h != nil && holderAlive(stateDir, h) {
			out = h
		}
		return nil
	})
	return out
}
