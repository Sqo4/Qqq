package engine

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/sqo4/uley/client/procutil"
)

// RPCServer runs llama.cpp's rpc-server: it lends this GPU to a chain that
// runs one big model. It listens on 127.0.0.1 only; the coordinator reaches
// it through the tunnel the client opens, never directly.
type RPCServer struct {
	Bin      string
	CacheDir string // weights received from the chain are kept here
	LogDir   string
	Device   string // e.g. Vulkan0; empty = engine default

	mu     sync.Mutex
	cmd    *exec.Cmd
	exited chan struct{}
	addr   string
}

// FindRPCServer looks for rpc-server next to llama-server.
func FindRPCServer(llamaServer string) string {
	if llamaServer == "" {
		return ""
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	dir := filepath.Dir(llamaServer)
	for _, n := range []string{"ggml-rpc-server", "rpc-server", "llama-rpc-server"} {
		p := filepath.Join(dir, n+ext)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// Start launches the server unless it already runs and returns its address.
func (r *RPCServer) Start() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd != nil {
		select {
		case <-r.exited:
		default:
			return r.addr, nil
		}
	}
	if r.Bin == "" {
		return "", errors.New("rpc-server not found, reinstall Uley")
	}
	port, err := freePort()
	if err != nil {
		return "", err
	}
	addr := "127.0.0.1:" + strconv.Itoa(port)
	args := []string{"-H", "127.0.0.1", "-p", strconv.Itoa(port), "-c"}
	if r.Device != "" {
		args = append(args, "-d", r.Device)
	}
	cmd := exec.Command(r.Bin, args...)
	procutil.Background(cmd)
	cmd.Env = os.Environ()
	if r.CacheDir != "" {
		os.MkdirAll(r.CacheDir, 0o700)
		cmd.Env = append(cmd.Env, "LLAMA_CACHE="+r.CacheDir)
	}
	var logw io.Writer = io.Discard
	if r.LogDir != "" {
		os.MkdirAll(r.LogDir, 0o700)
		if f, err := os.Create(filepath.Join(r.LogDir, "rpc-server.log")); err == nil {
			defer f.Close()
			logw = f
		}
	}
	cmd.Stdout, cmd.Stderr = logw, logw
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start rpc-server: %w", err)
	}
	procutil.AfterStart(cmd.Process.Pid)
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	for i := 0; i < 100; i++ {
		select {
		case <-exited:
			return "", errors.New("rpc-server exited at start, see rpc-server.log")
		case <-time.After(100 * time.Millisecond):
		}
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			r.cmd, r.exited, r.addr = cmd, exited, addr
			return addr, nil
		}
	}
	cmd.Process.Kill()
	return "", errors.New("rpc-server did not open its port")
}

// Stop frees the video memory.
func (r *RPCServer) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd != nil && r.cmd.Process != nil {
		r.cmd.Process.Kill()
		select {
		case <-r.exited:
		case <-time.After(10 * time.Second):
		}
	}
	r.cmd = nil
}

// Running reports whether the server is up.
func (r *RPCServer) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd == nil {
		return false
	}
	select {
	case <-r.exited:
		return false
	default:
		return true
	}
}
