// Package protocol describes the JSON API between the Uley coordinator,
// volunteer nodes and API users. Both sides import it, so the wire format
// is defined in exactly one place.
package protocol

import "encoding/json"

// APIPrefix is the path prefix of every coordinator API endpoint.
const APIPrefix = "/api/v1"

// Limits shared by the coordinator and the client. The client enforces
// them again, so a misbehaving coordinator cannot ask for more.
//
// Agents such as Hermes Agent send long conversations with tool schemas
// (64k tokens of context), hence the generous prompt limit.
const (
	MaxPromptBytes   = 512 * 1024
	MaxToolsBytes    = 128 * 1024
	MaxOutputBytes   = 128 * 1024
	MaxTokensLimit   = 8192
	DefaultMaxTokens = 1024
	MaxMessages      = 1024
)

// Node states reported in heartbeats.
const (
	StateWorking     = "working"     // ready or busy with a job
	StatePaused      = "paused"      // paused by the user or by a rule
	StatePreparing   = "preparing"   // downloading a model / starting the engine
	StateUnsupported = "unsupported" // no suitable GPU
	StateChain       = "chain"       // lends its GPU to a chain running the big model
)

// Hive tells nodes how the hive runs right now.
type Hive struct {
	// Mode is "single" (every node runs a whole small model) or "chain"
	// (nodes hold layers of one big model).
	Mode  string `json:"mode"`
	Model string `json:"model"`
}

const (
	ModeSingle = "single"
	ModeChain  = "chain"
)

// ChainWait is the reply to a chain worker's long poll.
type ChainWait struct {
	Dial    string `json:"dial,omitempty"` // open a stream with this token
	InChain bool   `json:"in_chain"`
}

// ChainStats describe the big model's chains for the live dashboard.
type ChainStats struct {
	Model    string `json:"model"`
	State    string `json:"state"` // waiting, loading, ready
	Message  string `json:"message"`
	Members  int    `json:"members"`
	Spares   int    `json:"spares"`
	NeedMB   int    `json:"need_mb"`
	HaveMB   int    `json:"have_mb"` // video memory of all chain volunteers online
	ChainsUp int    `json:"chains_up"`
	Balance  string `json:"balance"` // fastest or equal
	// WordMS is the measured time per step through the whole chain
	// (one step ≈ one word).
	WordMS float64     `json:"word_ms"`
	Links  []ChainLink `json:"links,omitempty"`
}

// ChainLink is one volunteer computer as the dashboard shows it.
type ChainLink struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	GPU     string  `json:"gpu"`
	Layers  int     `json:"layers"`
	StepMS  float64 `json:"step_ms"` // its share of a step, network included
	NetMS   float64 `json:"net_ms"`
	InChain bool    `json:"in_chain"`
}

// GPU is everything a node tells the coordinator about its hardware.
type GPU struct {
	Device  string `json:"device,omitempty"` // engine device name, e.g. Vulkan0
	Name    string `json:"name"`
	Vendor  string `json:"vendor"` // nvidia, amd, intel, apple, other
	VRAMMB  int    `json:"vram_mb"`
	Backend string `json:"backend,omitempty"` // vulkan, cuda, metal
	// FreeMB is how much of the card was free when it was last looked at;
	// other programs (games, Ollama, LM Studio) may hold the rest. It stays
	// on the computer and is never sent.
	FreeMB int `json:"-"`
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

// Message is one chat message. Tool fields follow the OpenAI format; they
// are plain JSON for the model to read and write. Nothing in them is ever
// executed by a volunteer: an agent (e.g. Hermes) runs the tools itself.
type Message struct {
	Role       string          `json:"role"` // system, user, assistant, tool
	Content    string          `json:"content"`
	Name       string          `json:"name,omitempty"`
	ToolCalls  json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

// ValidRole reports whether a chat role is allowed.
func ValidRole(r string) bool {
	switch r {
	case "system", "user", "assistant", "tool":
		return true
	}
	return false
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
	// Tools and ToolChoice are OpenAI function definitions the model may
	// answer with; the caller's agent executes them, not the volunteer.
	Tools      json.RawMessage `json:"tools,omitempty"`
	ToolChoice json.RawMessage `json:"tool_choice,omitempty"`
}

type JobResult struct {
	Output       string          `json:"output"`
	ToolCalls    json.RawMessage `json:"tool_calls,omitempty"`
	FinishReason string          `json:"finish_reason,omitempty"`
	TokensOut    int             `json:"tokens_out"`
	DurationMS   int64           `json:"duration_ms"`
	Error        string          `json:"error,omitempty"`
}

// SubmitRequest is what an API user sends to put work into the hive.
type SubmitRequest struct {
	Model       string          `json:"model"`
	Messages    []Message       `json:"messages,omitempty"`
	Prompt      string          `json:"prompt,omitempty"` // shortcut for a single user message
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	Seed        int64           `json:"seed,omitempty"`
	Tools       json.RawMessage `json:"tools,omitempty"`
	ToolChoice  json.RawMessage `json:"tool_choice,omitempty"`
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
	ID           string          `json:"id"`
	Status       string          `json:"status"`
	Model        string          `json:"model"`
	Output       string          `json:"output,omitempty"`
	ToolCalls    json.RawMessage `json:"tool_calls,omitempty"`
	FinishReason string          `json:"finish_reason,omitempty"`
	Error        string          `json:"error,omitempty"`
	Verify       string          `json:"verify"`
	CreatedAt    int64           `json:"created_at"`
	CompletedAt  int64           `json:"completed_at,omitempty"`
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
	Chain         *ChainStats    `json:"chain,omitempty"`
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
