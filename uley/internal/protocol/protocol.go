// Package protocol describes the JSON API between the Uley coordinator,
// volunteer nodes and API users. Both sides import it, so the wire format
// is defined in exactly one place.
package protocol

// APIPrefix is the path prefix of every coordinator API endpoint.
const APIPrefix = "/api/v1"

// Limits shared by the coordinator and the client. The client enforces
// them again, so a misbehaving coordinator cannot ask for more.
const (
	MaxPromptBytes   = 32 * 1024
	MaxOutputBytes   = 64 * 1024
	MaxTokensLimit   = 2048
	DefaultMaxTokens = 512
	MaxMessages      = 64
)

// Node states reported in heartbeats.
const (
	StateWorking     = "working"     // ready or busy with a job
	StatePaused      = "paused"      // paused by the user or by a rule
	StatePreparing   = "preparing"   // downloading a model / starting the engine
	StateUnsupported = "unsupported" // no suitable GPU
)

// GPU is everything a node tells the coordinator about its hardware.
type GPU struct {
	Name    string `json:"name"`
	Vendor  string `json:"vendor"` // nvidia, amd, intel, apple, other
	VRAMMB  int    `json:"vram_mb"`
	Backend string `json:"backend,omitempty"` // vulkan, cuda, metal
}

type RegisterRequest struct {
	NodeName      string `json:"node_name"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	ClientVersion string `json:"client_version"`
	GPUs          []GPU  `json:"gpus"`
	JoinKey       string `json:"join_key,omitempty"`
}

type RegisterResponse struct {
	NodeID string `json:"node_id"`
	Token  string `json:"token"`
}

type HeartbeatRequest struct {
	State         string   `json:"state"`
	StateReason   string   `json:"state_reason,omitempty"`
	ClientVersion string   `json:"client_version"`
	GPUs          []GPU    `json:"gpus"`
	Models        []string `json:"models"` // models loaded and ready to run
	VRAMBudgetMB  int      `json:"vram_budget_mb"`
}

type HeartbeatResponse struct {
	// WantedModels lists models that currently have queued work, so idle
	// nodes can prefer them.
	WantedModels []string `json:"wanted_models,omitempty"`
	Message      string   `json:"message,omitempty"`
}

type Message struct {
	Role    string `json:"role"` // system, user, assistant
	Content string `json:"content"`
}

// PollRequest asks for the next job a node can run.
type PollRequest struct {
	Models      []string `json:"models"`
	WaitSeconds int      `json:"wait_seconds"`
}

// Job is one unit of work handed to a node. It only ever contains a model
// id from the built-in whitelist and plain text, never code.
type Job struct {
	ID           string    `json:"id"` // assignment id
	TaskID       string    `json:"task_id"`
	Model        string    `json:"model"`
	Messages     []Message `json:"messages"`
	MaxTokens    int       `json:"max_tokens"`
	Temperature  float64   `json:"temperature"`
	Seed         int64     `json:"seed"`
	DeadlineUnix int64     `json:"deadline_unix"`
}

type JobResult struct {
	Output     string `json:"output"`
	TokensOut  int    `json:"tokens_out"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

// SubmitRequest is what an API user sends to put work into the hive.
type SubmitRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages,omitempty"`
	Prompt      string    `json:"prompt,omitempty"` // shortcut for a single user message
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	Seed        int64     `json:"seed,omitempty"`
}

// Task statuses.
const (
	TaskQueued  = "queued"
	TaskRunning = "running"
	TaskDone    = "done"
	TaskFailed  = "failed"
)

// Verification results of a task.
const (
	VerifyNone     = "none"       // ran on a single node
	VerifyOK       = "verified"   // two nodes agreed
	VerifyMismatch = "mismatch"   // nodes disagreed, no majority
	VerifyPartial  = "unverified" // second node never came, result from one node
)

type Task struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Model       string `json:"model"`
	Output      string `json:"output,omitempty"`
	Error       string `json:"error,omitempty"`
	Verify      string `json:"verify"`
	CreatedAt   int64  `json:"created_at"`
	CompletedAt int64  `json:"completed_at,omitempty"`
}

type Stats struct {
	HiveModel     string         `json:"hive_model"`
	NodesBusy     int            `json:"nodes_busy"`
	CapacityTPS   float64        `json:"capacity_tps"` // tokens/s all working nodes can do
	UsedTPS       float64        `json:"used_tps"`     // tokens/s of nodes busy right now
	Utilization   float64        `json:"utilization"`  // 0..1 share of working nodes that are busy
	UsersOnline   int            `json:"users_online"`
	UsersTotal    int            `json:"users_total"`
	ProjectsTotal int            `json:"projects_total"`
	MessagesToday int            `json:"messages_today"`
	NodesOnline   int            `json:"nodes_online"`
	NodesWorking  int            `json:"nodes_working"`
	NodesTotal    int            `json:"nodes_total"`
	VRAMOnlineMB  int            `json:"vram_online_mb"`
	TasksQueued   int            `json:"tasks_queued"`
	TasksRunning  int            `json:"tasks_running"`
	TasksDone     int            `json:"tasks_done"`
	TasksFailed   int            `json:"tasks_failed"`
	TokensTotal   int64          `json:"tokens_total"`
	TokensPerSec  float64        `json:"tokens_per_sec"` // over the last 10 minutes
	GPUVendors    map[string]int `json:"gpu_vendors"`
	ModelsOnline  map[string]int `json:"models_online"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}
