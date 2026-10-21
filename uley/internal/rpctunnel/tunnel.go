// Package rpctunnel connects the coordinator to GPU workers that sit behind
// home routers. A worker never accepts connections from the internet:
// its llama.cpp rpc-server listens on 127.0.0.1 only, and the worker itself
// dials out to the coordinator over HTTPS whenever the coordinator asks.
//
//	head llama-server ──tcp──► Relay port (127.0.0.1 on the coordinator)
//	     Relay ──"dial T"──► worker (long poll)
//	     worker ──HTTPS upgrade /rpc/dial?t=T──► Relay   (spliced together)
//	     worker ──tcp──► rpc-server on the worker's 127.0.0.1
package rpctunnel

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// UpgradeProto is the protocol name used in the HTTP Upgrade header.
const UpgradeProto = "uley-rpc"

// Relay runs on the coordinator.
type Relay struct {
	mu      sync.Mutex
	workers map[string]*worker
	pending map[string]chan net.Conn
}

type worker struct {
	ln       net.Listener
	dials    chan string
	conns    map[net.Conn]bool
	lastPoll time.Time
}

func NewRelay() *Relay {
	return &Relay{workers: map[string]*worker{}, pending: map[string]chan net.Conn{}}
}

// Attach opens a local port that leads to the worker's GPU and returns it.
func (r *Relay) Attach(nodeID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if w, ok := r.workers[nodeID]; ok {
		return w.ln.Addr().String(), nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	w := &worker{ln: ln, dials: make(chan string, 16), conns: map[net.Conn]bool{}, lastPoll: time.Now()}
	r.workers[nodeID] = w
	go r.acceptLoop(nodeID, w)
	return ln.Addr().String(), nil
}

// Detach closes the worker's port and every open stream to it.
func (r *Relay) Detach(nodeID string) {
	r.mu.Lock()
	w, ok := r.workers[nodeID]
	delete(r.workers, nodeID)
	r.mu.Unlock()
	if !ok {
		return
	}
	w.ln.Close()
	r.mu.Lock()
	for c := range w.conns {
		c.Close()
	}
	r.mu.Unlock()
}

// LastPoll is when the worker last asked for work; a silent worker is gone.
func (r *Relay) LastPoll(nodeID string) (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.workers[nodeID]
	if !ok {
		return time.Time{}, false
	}
	return w.lastPoll, true
}

func randToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (r *Relay) acceptLoop(nodeID string, w *worker) {
	for {
		head, err := w.ln.Accept()
		if err != nil {
			return
		}
		go r.bridge(nodeID, w, head)
	}
}

// bridge asks the worker to dial in and splices its stream to head.
func (r *Relay) bridge(nodeID string, w *worker, head net.Conn) {
	token := randToken()
	ch := make(chan net.Conn, 1)
	r.mu.Lock()
	r.pending[token] = ch
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.pending, token)
		r.mu.Unlock()
	}()
	select {
	case w.dials <- token:
	default:
		head.Close()
		return
	}
	var remote net.Conn
	select {
	case remote = <-ch:
	case <-time.After(30 * time.Second):
		head.Close()
		return
	}
	r.mu.Lock()
	if _, alive := r.workers[nodeID]; !alive {
		r.mu.Unlock()
		head.Close()
		remote.Close()
		return
	}
	w.conns[head], w.conns[remote] = true, true
	r.mu.Unlock()
	splice(head, remote)
	r.mu.Lock()
	delete(w.conns, head)
	delete(w.conns, remote)
	r.mu.Unlock()
}

// WaitDial blocks until the coordinator needs a new stream to the worker
// and returns its token, or "" after wait.
func (r *Relay) WaitDial(ctx context.Context, nodeID string, wait time.Duration) (string, error) {
	r.mu.Lock()
	w, ok := r.workers[nodeID]
	if ok {
		w.lastPoll = time.Now()
	}
	r.mu.Unlock()
	if !ok {
		return "", errors.New("worker is not part of a chain")
	}
	select {
	case t := <-w.dials:
		return t, nil
	case <-time.After(wait):
		return "", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Deliver hands a worker's dialed-in stream to the waiting bridge.
func (r *Relay) Deliver(token string, conn net.Conn) bool {
	r.mu.Lock()
	ch, ok := r.pending[token]
	delete(r.pending, token)
	r.mu.Unlock()
	if !ok {
		return false
	}
	ch <- conn
	return true
}

// HandleUpgrade turns an HTTP request into a raw stream and delivers it.
// The caller has already authenticated the node.
func (r *Relay) HandleUpgrade(w http.ResponseWriter, req *http.Request) {
	if req.Header.Get("Upgrade") != UpgradeProto {
		http.Error(w, "upgrade required", http.StatusUpgradeRequired)
		return
	}
	token := req.URL.Query().Get("t")
	r.mu.Lock()
	_, ok := r.pending[token]
	r.mu.Unlock()
	if !ok {
		http.Error(w, "unknown stream", http.StatusNotFound)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "cannot upgrade over this connection", http.StatusInternalServerError)
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return
	}
	conn.SetDeadline(time.Time{})
	brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: " + UpgradeProto + "\r\nConnection: Upgrade\r\n\r\n")
	if err := brw.Flush(); err != nil {
		conn.Close()
		return
	}
	if !r.Deliver(token, &bufConn{Conn: conn, r: brw.Reader}) {
		conn.Close()
	}
}

func splice(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		io.Copy(dst, src)
		if c, ok := dst.(interface{ CloseWrite() error }); ok {
			c.CloseWrite()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	a.Close()
	b.Close()
	<-done
}

// bufConn reads bytes already buffered during the HTTP handshake first.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// Dial opens an upgraded stream from a worker to the coordinator.
// baseURL is the coordinator, path the dial endpoint, token the node token.
func Dial(ctx context.Context, baseURL, path, nodeToken string, tlsConf *tls.Config) (net.Conn, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	host := u.Host
	if u.Port() == "" {
		if u.Scheme == "https" {
			host += ":443"
		} else {
			host += ":80"
		}
	}
	d := net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	var conn net.Conn
	if u.Scheme == "https" {
		c := &tls.Config{}
		if tlsConf != nil {
			c = tlsConf.Clone()
		}
		c.ServerName = u.Hostname()
		c.NextProtos = []string{"http/1.1"} // HTTP/2 cannot upgrade
		td := tls.Dialer{NetDialer: &d, Config: c}
		conn, err = td.DialContext(ctx, "tcp", host)
	} else {
		conn, err = d.DialContext(ctx, "tcp", host)
	}
	if err != nil {
		return nil, err
	}
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: %s\r\nAuthorization: Bearer %s\r\n\r\n",
		path, u.Host, UpgradeProto, nodeToken)
	conn.SetDeadline(time.Now().Add(20 * time.Second))
	if _, err := io.WriteString(conn, req); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		return nil, fmt.Errorf("tunnel: %s", resp.Status)
	}
	conn.SetDeadline(time.Time{})
	return &bufConn{Conn: conn, r: br}, nil
}

// Serve runs on the worker: it waits for dial requests and connects each
// one to the local rpc-server at localAddr.
func Serve(ctx context.Context, localAddr string, poll func(context.Context) (string, error), dial func(context.Context, string) (net.Conn, error)) error {
	for ctx.Err() == nil {
		token, err := poll(ctx)
		if err != nil {
			return err
		}
		if token == "" {
			continue
		}
		go func() {
			local, err := net.DialTimeout("tcp", localAddr, 10*time.Second)
			if err != nil {
				return
			}
			remote, err := dial(ctx, token)
			if err != nil {
				local.Close()
				return
			}
			splice(remote, local)
		}()
	}
	return ctx.Err()
}
