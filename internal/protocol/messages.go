package protocol

import "time"

// Hello is the first client frame.
type Hello struct {
	Protocol      int    `json:"protocol"`
	ClientVersion string `json:"client_version"`
	// Root is the absolute remote application root (e.g. ~/.local/share/recom).
	Root string `json:"root"`
}

// Ack is the helper's reply to Hello.
type Ack struct {
	Protocol      int    `json:"protocol"`
	HelperVersion string `json:"helper_version"`
	HelperSHA256  string `json:"helper_sha256"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	Home          string `json:"home"`
	PID           int    `json:"pid"`
}

// Request is a client operation.
type Request struct {
	Op string `json:"op"`

	Open    *OpenRequest    `json:"open,omitempty"`
	Exec    *ExecRequest    `json:"exec,omitempty"`
	Mark    *MarkRequest    `json:"mark,omitempty"`
	Pending *PendingRequest `json:"pending,omitempty"`
	Stat    *StatRequest    `json:"stat,omitempty"`
	Job     *JobRequest     `json:"job,omitempty"`
	Env     *EnvRequest     `json:"env,omitempty"`
}

// Operation names.
const (
	OpOpen    = "open"    // select replica, take lease, list pending outputs
	OpExec    = "exec"    // snapshot + run (foreground or detached)
	OpMark    = "mark"    // record per-output transfer state
	OpPending = "pending" // list pending outputs without taking the lease
	OpStat    = "stat"    // resolve explicit pull paths
	OpJobs    = "jobs"    // list jobs
	OpJob     = "job"     // describe one job
	OpLogs    = "logs"    // stream job logs
	OpStop    = "stop"    // stop a job
	OpEnv     = "env"     // describe remote environment
	OpStatus  = "status"  // replica status
	OpClose   = "close"   // release lease and exit
)

// Response ends a request. Exactly one of the typed fields is set on success.
type Response struct {
	Error *Error `json:"error,omitempty"`

	Open    *OpenResult    `json:"open,omitempty"`
	Exit    *ExitResult    `json:"exit,omitempty"`
	Pending *PendingResult `json:"pending,omitempty"`
	Stat    *StatResult    `json:"stat,omitempty"`
	Jobs    []JobInfo      `json:"jobs,omitempty"`
	Job     *JobInfo       `json:"job,omitempty"`
	Env     *EnvResult     `json:"env,omitempty"`
	Status  *StatusResult  `json:"status,omitempty"`
}

// Error codes returned by the helper.
const (
	ErrBusy       = "busy"        // replica lease held by another run/job
	ErrNoReplica  = "no_replica"  // replica does not exist yet
	ErrBadRequest = "bad_request" // validation failure
	ErrNotFound   = "not_found"   // job/path not found
	ErrInternal   = "internal"
	ErrExec       = "exec" // command could not be started
)

// Error is a structured helper error.
type Error struct {
	Code    string     `json:"code"`
	Message string     `json:"message"`
	Holder  *LeaseInfo `json:"holder,omitempty"`
	// ExitCode is set for ErrExec (126 or 127).
	ExitCode int `json:"exit_code,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// OpenRequest selects a replica and optionally takes its lease.
type OpenRequest struct {
	ReplicaID string `json:"replica_id"`
	ProjectID string `json:"project_id"`
	// Create creates the replica directory if missing.
	Create bool `json:"create"`
	// Lock requests the exclusive replica lease.
	Lock bool `json:"lock"`
	// Wait blocks while another foreground run holds the lease.
	Wait bool `json:"wait"`
	// TryLock takes the lease only if free; no error when busy.
	TryLock bool   `json:"try_lock"`
	Owner   string `json:"owner"` // human description, e.g. "foreground run on laptop"
}

// OpenResult describes the opened replica.
type OpenResult struct {
	ReplicaPath   string     `json:"replica_path"`
	ReplicaExists bool       `json:"replica_exists"`
	Locked        bool       `json:"locked"`
	Holder        *LeaseInfo `json:"holder,omitempty"`
	Pending       []Output   `json:"pending,omitempty"`
	Warnings      []string   `json:"warnings,omitempty"`
	// Setup maps toolchain name to the fingerprint of its last successful
	// environment setup in this replica (see internal/toolchain).
	Setup map[string]string `json:"setup,omitempty"`
}

// LeaseInfo describes the holder of a replica lease.
type LeaseInfo struct {
	Kind    string    `json:"kind"` // "run" or "job"
	ID      string    `json:"id"`
	PID     int       `json:"pid"`
	Start   uint64    `json:"start"` // process start time (clock ticks since boot) for identity
	Owner   string    `json:"owner"`
	Command string    `json:"command"`
	Since   time.Time `json:"since"`
}

// OutputPolicy controls output detection on the helper.
type OutputPolicy struct {
	Exclude      []string `json:"exclude"`
	Include      []string `json:"include"`
	MaxFileSize  int64    `json:"max_file_size"`
	MaxRunSize   int64    `json:"max_run_size"`
	HashLimit    int64    `json:"hash_limit"`
	SkipSecrets  bool     `json:"skip_secrets"`
	NoCollection bool     `json:"no_collection"` // record nothing (lease not held)
}

// ExecRequest runs a command in the replica.
type ExecRequest struct {
	Argv    []string          `json:"argv"`
	Display string            `json:"display"` // redacted command for manifests/listings
	Cwd     string            `json:"cwd"`     // relative to replica ("." for root); "~" for remote home
	Env     map[string]string `json:"env"`
	Path    []string          `json:"path"` // PATH entries to prepend (relative = replica-relative)
	// RelEnv sets variables to replica-relative paths that exist (e.g.
	// VIRTUAL_ENV=.venv); Env takes precedence.
	RelEnv map[string]string `json:"rel_env,omitempty"`
	// PathFallback lists directories appended to PATH if they exist ("~/"
	// prefix and globs allowed; for a glob the highest version wins), e.g.
	// ~/.nvm/versions/node/*/bin for tools set up only by interactive shells.
	PathFallback []string     `json:"path_fallback,omitempty"`
	Wrapper      []string     `json:"wrapper"` // argv prefix
	Shell        bool         `json:"shell"`   // Argv[0] is a shell expression
	EnvMode      string       `json:"env_mode"`
	TTY          bool         `json:"tty"`
	Term         string       `json:"term"`
	Rows         int          `json:"rows"`
	Cols         int          `json:"cols"`
	Detach       bool         `json:"detach"`
	Tmux         string       `json:"tmux"` // auto|always|never (detached only)
	Policy       OutputPolicy `json:"policy"`
	Client       string       `json:"client"`
	Local        string       `json:"local"` // local project root (informational)
}

// Event is an informational helper message.
type Event struct {
	Kind    string     `json:"kind"` // "waiting", "started", "info", "warning"
	Message string     `json:"message"`
	RunID   string     `json:"run_id,omitempty"`
	Holder  *LeaseInfo `json:"holder,omitempty"`
}

// Signal asks the helper to deliver a signal to the foreground process group.
type Signal struct {
	Name string `json:"name"` // INT, TERM, HUP, QUIT, KILL
}

// Resize updates the PTY size.
type Resize struct {
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}

// Output status values.
const (
	StatusPending    = "pending"    // eligible for automatic download
	StatusHeld       = "held"       // recorded but not auto-downloaded (size/include policy)
	StatusCollected  = "collected"  // installed locally
	StatusConflict   = "conflict"   // local copy changed; remote version staged in .recom/conflicts
	StatusDismissed  = "dismissed"  // user stopped tracking it
	StatusSuperseded = "superseded" // a later run produced the same path
	StatusSkipped    = "skipped"    // unsafe or unsupported (e.g. escaping symlink)
)

// Output is one file produced or modified by a run.
type Output struct {
	RunID      string `json:"run_id"`
	Path       string `json:"path"` // slash-separated, relative to replica root
	Type       string `json:"type"` // "file" or "symlink"
	Size       int64  `json:"size"`
	Mode       uint32 `json:"mode"`
	MtimeNs    int64  `json:"mtime_ns"`
	SHA256     string `json:"sha256,omitempty"`
	PreExisted bool   `json:"pre_existed"`
	PreSHA256  string `json:"pre_sha256,omitempty"`
	Link       string `json:"link,omitempty"`
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
	Note       string `json:"note,omitempty"`
}

// Pending reports whether the output still awaits a transfer decision.
func (o Output) Pending() bool { return o.Status == StatusPending || o.Status == StatusHeld }

// ExitResult ends a foreground exec, or reports a detached launch.
type ExitResult struct {
	RunID    string   `json:"run_id"`
	ExitCode int      `json:"exit_code"`
	Signal   string   `json:"signal,omitempty"`
	Lost     bool     `json:"lost,omitempty"` // client disconnected; process was terminated
	Outputs  []Output `json:"outputs,omitempty"`
	Excluded int      `json:"excluded,omitempty"` // changed files skipped by exclude policy
	Deleted  int      `json:"deleted,omitempty"`  // files removed by the run (never propagated)
	Snapshot string   `json:"snapshot,omitempty"` // timing summary
	JobID    string   `json:"job_id,omitempty"`
	Tmux     bool     `json:"tmux,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// MarkRequest records transfer results.
type MarkRequest struct {
	ReplicaID string `json:"replica_id"`
	Items     []Mark `json:"items"`
}

// Mark is one transfer result.
type Mark struct {
	RunID  string `json:"run_id"`
	Path   string `json:"path"`
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
}

// PendingRequest lists pending outputs.
type PendingRequest struct {
	ReplicaID string `json:"replica_id"`
}

// PendingResult lists pending outputs.
type PendingResult struct {
	Outputs  []Output `json:"outputs"`
	Warnings []string `json:"warnings,omitempty"`
}

// StatRequest resolves explicit pull paths.
type StatRequest struct {
	ReplicaID   string   `json:"replica_id"`
	Paths       []string `json:"paths"`
	SkipSecrets bool     `json:"skip_secrets"`
}

// StatResult lists remote files for an explicit pull.
type StatResult struct {
	Files    []Output `json:"files"`
	Missing  []string `json:"missing,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// JobRequest addresses a job.
type JobRequest struct {
	ReplicaID string        `json:"replica_id"`
	JobID     string        `json:"job_id"`
	Follow    bool          `json:"follow"`
	Tail      int           `json:"tail"` // logs: last N lines (0 = all)
	Grace     time.Duration `json:"grace"`
	All       bool          `json:"all"`
}

// Job states.
const (
	JobStarting = "starting"
	JobRunning  = "running"
	JobExited   = "exited"
	JobStopped  = "stopped"
	JobLost     = "lost"   // supervisor vanished (reboot, kill -9)
	JobFailed   = "failed" // could not start
)

// JobInfo describes a detached job.
type JobInfo struct {
	ID        string    `json:"id"`
	ReplicaID string    `json:"replica_id"`
	State     string    `json:"state"`
	Command   string    `json:"command"`
	Cwd       string    `json:"cwd"`
	Started   time.Time `json:"started"`
	Ended     time.Time `json:"ended,omitzero"`
	ExitCode  *int      `json:"exit_code,omitempty"`
	Signal    string    `json:"signal,omitempty"`
	Tmux      bool      `json:"tmux"`
	TmuxPath  string    `json:"tmux_path,omitempty"`
	Session   string    `json:"session,omitempty"`
	Socket    string    `json:"socket,omitempty"`
	Prefix    string    `json:"tmux_prefix,omitempty"` // the backend's tmux prefix key, e.g. "C-b"
	PID       int       `json:"pid,omitempty"`
	Pending   int       `json:"pending"`
	Message   string    `json:"message,omitempty"`
}

// EnvRequest asks for an environment report.
type EnvRequest struct {
	ReplicaID string            `json:"replica_id"`
	EnvMode   string            `json:"env_mode"`
	Env       map[string]string `json:"env"`
	Path      []string          `json:"path"`
	// PathFallback is as in ExecRequest.
	PathFallback []string `json:"path_fallback,omitempty"`
}

// Tool is a resolved remote tool.
type Tool struct {
	Name    string `json:"name"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
}

// EnvResult describes the remote environment.
type EnvResult struct {
	Hostname   string   `json:"hostname"`
	OS         string   `json:"os"`
	Kernel     string   `json:"kernel"`
	Arch       string   `json:"arch"`
	Shell      string   `json:"shell"`
	EnvMode    string   `json:"env_mode"`
	PATH       string   `json:"path"`
	Tools      []Tool   `json:"tools"`
	GPUs       []string `json:"gpus,omitempty"`
	GPUNote    string   `json:"gpu_note,omitempty"`
	DiskFree   uint64   `json:"disk_free"`
	DiskTotal  uint64   `json:"disk_total"`
	Root       string   `json:"root"`
	Writable   bool     `json:"writable"`
	TmuxOK     bool     `json:"tmux_ok"`
	RsyncOK    bool     `json:"rsync_ok"`
	EnvWarning string   `json:"env_warning,omitempty"`
}

// StatusResult describes replica state on the backend.
type StatusResult struct {
	ReplicaPath   string     `json:"replica_path"`
	ReplicaExists bool       `json:"replica_exists"`
	Holder        *LeaseInfo `json:"holder,omitempty"`
	Pending       []Output   `json:"pending,omitempty"`
	Running       []JobInfo  `json:"running,omitempty"`
	LastRun       *RunInfo   `json:"last_run,omitempty"`
	Warnings      []string   `json:"warnings,omitempty"`
	// Setup is as in OpenResult.
	Setup map[string]string `json:"setup,omitempty"`
}

// RunInfo summarizes a run manifest.
type RunInfo struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"`
	Command  string    `json:"command"`
	Started  time.Time `json:"started"`
	Ended    time.Time `json:"ended,omitzero"`
	ExitCode *int      `json:"exit_code,omitempty"`
	State    string    `json:"state"`
}
