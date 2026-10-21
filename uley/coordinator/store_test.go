package coordinator

import (
	"testing"
	"time"

	"github.com/sqo4/uley/internal/protocol"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestStore(t *testing.T) (*Store, *clock) {
	t.Helper()
	s, err := OpenStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	s.now = c.now
	return s, c
}

func addNode(t *testing.T, s *Store) string {
	t.Helper()
	r, err := s.RegisterNode(protocol.RegisterRequest{NodeName: "n", OS: "linux", Arch: "amd64", GPUs: []protocol.GPU{{Vendor: "amd", VRAMMB: 8192}}})
	if err != nil {
		t.Fatal(err)
	}
	return r.NodeID
}

var greedy = taskPayload{Messages: []protocol.Message{{Role: "user", Content: "hi"}}, MaxTokens: 16}

func mustAssign(t *testing.T, s *Store, node string) *protocol.Job {
	t.Helper()
	j, err := s.Assign(node, []string{"qwen3-0.6b"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestSingleReplicaFlow(t *testing.T) {
	s, _ := newTestStore(t)
	n := addNode(t, s)
	if j := mustAssign(t, s, n); j != nil {
		t.Fatal("empty queue must give no job")
	}
	id, _ := s.CreateTask("qwen3-0.6b", greedy, 1)
	if j, _ := s.Assign(n, []string{"qwen3-8b"}, time.Minute); j != nil {
		t.Fatal("node without the model must not get the task")
	}
	j := mustAssign(t, s, n)
	if j == nil || j.TaskID != id || j.Messages[0].Content != "hi" {
		t.Fatalf("bad job %+v", j)
	}
	if mustAssign(t, s, n) != nil {
		t.Fatal("task must not be handed out twice")
	}
	if err := s.Complete(n, j.ID, protocol.JobResult{Output: "hello", TokensOut: 2}); err != nil {
		t.Fatal(err)
	}
	task, _ := s.GetTask(id)
	if task.Status != protocol.TaskDone || task.Output != "hello" || task.Verify != protocol.VerifyNone {
		t.Fatalf("bad task %+v", task)
	}
	if err := s.Complete(n, j.ID, protocol.JobResult{Output: "again"}); err != ErrNotYours {
		t.Fatalf("second completion must be rejected, got %v", err)
	}
}

func TestOtherNodeCannotComplete(t *testing.T) {
	s, _ := newTestStore(t)
	a, b := addNode(t, s), addNode(t, s)
	s.CreateTask("qwen3-0.6b", greedy, 1)
	j := mustAssign(t, s, a)
	if err := s.Complete(b, j.ID, protocol.JobResult{Output: "x"}); err != ErrNotYours {
		t.Fatalf("got %v", err)
	}
}

func TestVerificationAgreement(t *testing.T) {
	s, _ := newTestStore(t)
	a, b := addNode(t, s), addNode(t, s)
	id, _ := s.CreateTask("qwen3-0.6b", greedy, 2)
	ja, jb := mustAssign(t, s, a), mustAssign(t, s, b)
	if ja == nil || jb == nil {
		t.Fatal("both nodes must get a replica")
	}
	s.Complete(a, ja.ID, protocol.JobResult{Output: "the answer is 42"})
	if task, _ := s.GetTask(id); task.Status == protocol.TaskDone {
		t.Fatal("must wait for the second replica")
	}
	s.Complete(b, jb.ID, protocol.JobResult{Output: "the answer is 42"})
	task, _ := s.GetTask(id)
	if task.Status != protocol.TaskDone || task.Verify != protocol.VerifyOK {
		t.Fatalf("bad task %+v", task)
	}
}

func TestVerificationTieBreakAndBan(t *testing.T) {
	s, _ := newTestStore(t)
	honest1, honest2, cheat := addNode(t, s), addNode(t, s), addNode(t, s)
	answer := "Paris is the capital of France and its largest city by population"
	for i := 0; i < 4; i++ {
		id, _ := s.CreateTask("qwen3-0.6b", greedy, 2)
		jc, j1 := mustAssign(t, s, cheat), mustAssign(t, s, honest1)
		s.Complete(cheat, jc.ID, protocol.JobResult{Output: "buy cheap watches now"})
		s.Complete(honest1, j1.ID, protocol.JobResult{Output: answer})
		j2 := mustAssign(t, s, honest2)
		if j2 == nil {
			t.Fatal("mismatch must create a third replica")
		}
		s.Complete(honest2, j2.ID, protocol.JobResult{Output: answer})
		task, _ := s.GetTask(id)
		if task.Output != answer || task.Verify != protocol.VerifyOK {
			t.Fatalf("round %d: majority must win, got %+v", i, task)
		}
	}
	var banned bool
	s.db.QueryRow(`SELECT banned FROM nodes WHERE id=?`, cheat).Scan(&banned)
	if !banned {
		t.Fatal("cheating node must be banned")
	}
	s.db.QueryRow(`SELECT banned FROM nodes WHERE id=?`, honest1).Scan(&banned)
	if banned {
		t.Fatal("honest node must not be banned")
	}
}

func TestTimeoutRequeuesAndEventuallyFails(t *testing.T) {
	s, c := newTestStore(t)
	id, _ := s.CreateTask("qwen3-0.6b", greedy, 1)
	for i := 0; i < maxFailures; i++ {
		n := addNode(t, s)
		if mustAssign(t, s, n) == nil {
			t.Fatalf("attempt %d: task must be back in the queue", i)
		}
		c.t = c.t.Add(2 * time.Minute)
		if _, err := s.Sweep(time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	task, _ := s.GetTask(id)
	if task.Status != protocol.TaskFailed {
		t.Fatalf("task must fail after %d timeouts, got %+v", maxFailures, task)
	}
}

func TestLonelyReplicaAcceptedUnverified(t *testing.T) {
	s, c := newTestStore(t)
	n := addNode(t, s)
	id, _ := s.CreateTask("qwen3-0.6b", greedy, 2)
	j := mustAssign(t, s, n)
	s.Complete(n, j.ID, protocol.JobResult{Output: "ok"})
	c.t = c.t.Add(3 * time.Minute)
	s.Sweep(2 * time.Minute)
	task, _ := s.GetTask(id)
	if task.Status != protocol.TaskDone || task.Verify != protocol.VerifyPartial {
		t.Fatalf("got %+v", task)
	}
}

func TestStats(t *testing.T) {
	s, _ := newTestStore(t)
	n := addNode(t, s)
	s.Heartbeat(n, protocol.HeartbeatRequest{State: protocol.StateWorking, Models: []string{"qwen3-0.6b"},
		GPUs: []protocol.GPU{{Vendor: "nvidia", VRAMMB: 12288}}})
	s.CreateTask("qwen3-0.6b", greedy, 1)
	st, err := s.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if st.NodesOnline != 1 || st.NodesWorking != 1 || st.VRAMOnlineMB != 12288 || st.TasksQueued != 1 || st.GPUVendors["nvidia"] != 1 {
		t.Fatalf("bad stats %+v", st)
	}
}

func TestSimilar(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"", "", true},
		{"a b c", "a  b\nc", true},
		{"one two three four five six seven eight nine ten", "one two three four five six seven eight nine eleven", true},
		{"yes", "no", false},
		{"one two three four five six seven eight nine ten", "totally different text here and there", false},
	}
	for _, c := range cases {
		if got := Similar(c.a, c.b); got != c.want {
			t.Errorf("Similar(%q,%q)=%v", c.a, c.b, got)
		}
	}
}
