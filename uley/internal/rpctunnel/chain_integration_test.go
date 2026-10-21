package rpctunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestLlamaChain runs a real model split across two rpc-servers that are
// reachable only through the tunnel. It needs llama.cpp binaries and a
// small GGUF model:
//
//	ULEY_LLAMA_BIN=/path/to/llama.cpp/build/bin ULEY_TEST_MODEL=/path/qwen3-0.6b.gguf go test -run LlamaChain -v
func TestLlamaChain(t *testing.T) {
	bin, model := os.Getenv("ULEY_LLAMA_BIN"), os.Getenv("ULEY_TEST_MODEL")
	if bin == "" || model == "" {
		t.Skip("ULEY_LLAMA_BIN and ULEY_TEST_MODEL not set")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	relay := NewRelay()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /dial", relay.HandleUpgrade)
	mux.HandleFunc("GET /wait/{node}", func(w http.ResponseWriter, r *http.Request) {
		tok, err := relay.WaitDial(r.Context(), r.PathValue("node"), 5*time.Second)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		w.Write([]byte(tok))
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	tlsConf := srv.Client().Transport.(*http.Transport).TLSClientConfig

	var rpcAddrs []string
	traffic := make([]atomic.Int64, 2)
	for i, node := range []string{"gamer1", "gamer2"} {
		// The worker's rpc-server listens on its own loopback only.
		port := freePort(t)
		local := fmt.Sprintf("127.0.0.1:%d", port)
		cmd := exec.CommandContext(ctx, filepath.Join(bin, "ggml-rpc-server"), "-H", "127.0.0.1", "-p", fmt.Sprint(port), "-c")
		cmd.Env = append(os.Environ(), fmt.Sprintf("LLAMA_CACHE=%s", t.TempDir()))
		logf, _ := os.Create(filepath.Join(t.TempDir(), fmt.Sprintf("rpc%d.log", i)))
		cmd.Stdout, cmd.Stderr = logf, logf
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer cmd.Process.Kill()
		waitPort(t, local)

		addr, err := relay.Attach(node)
		if err != nil {
			t.Fatal(err)
		}
		rpcAddrs = append(rpcAddrs, addr)
		node := node
		go Serve(ctx, local,
			func(ctx context.Context) (string, error) {
				req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/wait/"+node, nil)
				resp, err := srv.Client().Do(req)
				if err != nil {
					return "", err
				}
				defer resp.Body.Close()
				b, _ := io.ReadAll(resp.Body)
				return string(b), nil
			},
			func(ctx context.Context, tok string) (net.Conn, error) {
				c, err := Dial(ctx, srv.URL, "/dial?t="+tok, "tok", tlsConf)
				if err != nil {
					return nil, err
				}
				return &countConn{Conn: c, n: &traffic[i]}, nil
			})
	}

	headPort := freePort(t)
	head := exec.CommandContext(ctx, filepath.Join(bin, "llama-server"),
		"-m", model, "--rpc", strings.Join(rpcAddrs, ","), "-ngl", "999", "--tensor-split", "1,1",
		"-c", "2048", "--host", "127.0.0.1", "--port", fmt.Sprint(headPort), "--jinja")
	var headLog bytes.Buffer
	head.Stdout, head.Stderr = &headLog, &headLog
	if err := head.Start(); err != nil {
		t.Fatal(err)
	}
	defer head.Process.Kill()

	base := fmt.Sprintf("http://127.0.0.1:%d", headPort)
	deadline := time.Now().Add(3 * time.Minute)
	for {
		resp, err := http.Get(base + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("head never became ready:\n%s", tail(headLog.String()))
		}
		time.Sleep(500 * time.Millisecond)
	}
	for _, l := range strings.Split(headLog.String(), "\n") {
		if strings.Contains(strings.ToLower(l), "rpc") || strings.Contains(l, "offload") || strings.Contains(l, "MiB") {
			t.Log(l)
		}
	}

	body := `{"messages":[{"role":"user","content":"What is the capital of France? Answer in one word."}],"max_tokens":20,"temperature":0,"chat_template_kwargs":{"enable_thinking":false}}`
	start := time.Now()
	resp, err := http.Post(base+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
		Timings map[string]float64 `json:"timings"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if len(out.Choices) == 0 || !strings.Contains(out.Choices[0].Message.Content, "Paris") {
		t.Fatalf("bad answer %+v\n%s", out, tail(headLog.String()))
	}
	for i := range traffic {
		mb := float64(traffic[i].Load()) / (1 << 20)
		t.Logf("worker %d: %.1f MiB went through the tunnel", i+1, mb)
		if mb < 100 {
			t.Errorf("worker %d got almost no weights: the model was not split", i+1)
		}
	}
	t.Logf("answer %q in %v, %.1f tokens/s", out.Choices[0].Message.Content, time.Since(start), out.Timings["predicted_per_second"])
}

type countConn struct {
	net.Conn
	n *atomic.Int64
}

func (c *countConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.n.Add(int64(n))
	return n, err
}

func (c *countConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.n.Add(int64(n))
	return n, err
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitPort(t *testing.T, addr string) {
	for i := 0; i < 100; i++ {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s never opened", addr)
}

func tail(s string) string {
	if len(s) > 3000 {
		return s[len(s)-3000:]
	}
	return s
}
