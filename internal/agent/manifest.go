package agent

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chaseungjoon/recom/internal/protocol"
)

const manifestVersion = 1

// keepResolved is how many fully-resolved run manifests are retained.
const keepResolved = 100

// Manifest is the durable record of one run or detached job.
type Manifest struct {
	Version   int               `json:"version"`
	RunID     string            `json:"run_id"`
	Kind      string            `json:"kind"` // "run" or "job"
	ReplicaID string            `json:"replica_id"`
	Command   string            `json:"command"`
	Cwd       string            `json:"cwd"`
	Client    string            `json:"client,omitempty"`
	Started   time.Time         `json:"started"`
	Ended     time.Time         `json:"ended,omitzero"`
	State     string            `json:"state"` // running, exited, stopped, lost, failed
	ExitCode  *int              `json:"exit_code,omitempty"`
	Signal    string            `json:"signal,omitempty"`
	Outputs   []protocol.Output `json:"outputs"`
	Deleted   int               `json:"deleted,omitempty"`
	Note      string            `json:"note,omitempty"`
}

func newRunID() string {
	var b [5]byte
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format("060102") + hex.EncodeToString(b[:])
}

func manifestPath(l Layout, replica, runID string) string {
	return filepath.Join(l.Runs(replica), runID+".json")
}

func saveManifest(l Layout, m *Manifest) error {
	m.Version = manifestVersion
	return writeJSONAtomic(manifestPath(l, m.ReplicaID, m.RunID), m)
}

func loadManifest(l Layout, replica, runID string) (*Manifest, error) {
	var m Manifest
	if err := readJSON(manifestPath(l, replica, runID), &m); err != nil {
		return nil, err
	}
	if m.Version != manifestVersion || m.RunID != runID {
		return nil, fmt.Errorf("manifest %s: unsupported version %d", runID, m.Version)
	}
	return &m, nil
}

// loadManifests loads every manifest of a replica. Corrupt manifests are
// reported as warnings and ignored (fail closed: nothing is transferred).
func loadManifests(l Layout, replica string) ([]*Manifest, []string) {
	ents, err := os.ReadDir(l.Runs(replica))
	if err != nil {
		return nil, nil
	}
	var out []*Manifest
	var warns []string
	for _, e := range ents {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, ".") {
			continue
		}
		m, err := loadManifest(l, replica, strings.TrimSuffix(name, ".json"))
		if err != nil {
			warns = append(warns, err.Error())
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out, warns
}

// pendingOutputs lists outputs awaiting transfer, newest run first per path.
func pendingOutputs(l Layout, replica string) ([]protocol.Output, []string) {
	ms, warns := loadManifests(l, replica)
	seen := map[string]bool{}
	var out []protocol.Output
	for i := len(ms) - 1; i >= 0; i-- {
		m := ms[i]
		if m.State == protocol.JobRunning || m.State == protocol.JobStarting {
			continue
		}
		for _, o := range m.Outputs {
			if !o.Pending() || seen[o.Path] {
				continue
			}
			seen[o.Path] = true
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, warns
}

// recordOutputs saves a finished manifest and marks older unresolved records
// of the same paths as superseded. Caller holds the state mutex.
func recordOutputs(l Layout, m *Manifest) error {
	paths := map[string]bool{}
	for _, o := range m.Outputs {
		paths[o.Path] = true
	}
	ms, _ := loadManifests(l, m.ReplicaID)
	for _, old := range ms {
		if old.RunID == m.RunID {
			continue
		}
		changed := false
		for i := range old.Outputs {
			o := &old.Outputs[i]
			if o.Pending() && paths[o.Path] {
				o.Status = protocol.StatusSuperseded
				o.Note = "superseded by run " + m.RunID
				changed = true
			}
		}
		if changed {
			if err := saveManifest(l, old); err != nil {
				return err
			}
		}
	}
	if err := saveManifest(l, m); err != nil {
		return err
	}
	pruneManifests(l, m.ReplicaID, ms)
	return nil
}

func resolved(m *Manifest) bool {
	if m.State == protocol.JobRunning || m.State == protocol.JobStarting {
		return false
	}
	for _, o := range m.Outputs {
		if o.Pending() {
			return false
		}
	}
	return true
}

func pruneManifests(l Layout, replica string, ms []*Manifest) {
	var done []*Manifest
	for _, m := range ms {
		if resolved(m) {
			done = append(done, m)
		}
	}
	if len(done) <= keepResolved {
		return
	}
	for _, m := range done[:len(done)-keepResolved] {
		os.Remove(manifestPath(l, replica, m.RunID))
	}
}

// markOutputs applies client transfer results.
func markOutputs(l Layout, replica string, items []protocol.Mark) error {
	byRun := map[string][]protocol.Mark{}
	for _, it := range items {
		switch it.Status {
		case protocol.StatusCollected, protocol.StatusConflict, protocol.StatusDismissed, protocol.StatusPending, protocol.StatusHeld:
		default:
			return fmt.Errorf("invalid mark status %q", it.Status)
		}
		byRun[it.RunID] = append(byRun[it.RunID], it)
	}
	return withMutex(l.State(replica), func() error {
		for runID, marks := range byRun {
			if !validRunID(runID) {
				return fmt.Errorf("invalid run id %q", runID)
			}
			m, err := loadManifest(l, replica, runID)
			if err != nil {
				return err
			}
			for _, mk := range marks {
				for i := range m.Outputs {
					o := &m.Outputs[i]
					if o.Path != mk.Path || !o.Pending() {
						continue
					}
					o.Status = mk.Status
					o.Note = mk.Note
				}
			}
			if err := saveManifest(l, m); err != nil {
				return err
			}
		}
		return nil
	})
}

func validRunID(id string) bool { return ValidJobID(id) }

func lastRun(l Layout, replica string) *protocol.RunInfo {
	ms, _ := loadManifests(l, replica)
	if len(ms) == 0 {
		return nil
	}
	m := ms[len(ms)-1]
	return &protocol.RunInfo{ID: m.RunID, Kind: m.Kind, Command: m.Command, Started: m.Started, Ended: m.Ended, ExitCode: m.ExitCode, State: m.State}
}
