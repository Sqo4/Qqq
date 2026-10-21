package rpctunnel

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A fake rpc-server that upper-cases every line.
func fakeGPU(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					c.Write([]byte(strings.ToUpper(line)))
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func TestRelayRoundTrip(t *testing.T) {
	relay := NewRelay()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /dial", relay.HandleUpgrade)
	mux.HandleFunc("GET /wait", func(w http.ResponseWriter, r *http.Request) {
		tok, err := relay.WaitDial(r.Context(), "n1", 2*time.Second)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		w.Write([]byte(tok))
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	addr, err := relay.Attach("n1")
	if err != nil {
		t.Fatal(err)
	}
	gpu := fakeGPU(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := srv.Client()
	tlsConf := client.Transport.(*http.Transport).TLSClientConfig
	go Serve(ctx, gpu,
		func(ctx context.Context) (string, error) {
			req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/wait", nil)
			resp, err := client.Do(req)
			if err != nil {
				return "", err
			}
			defer resp.Body.Close()
			b := make([]byte, 64)
			n, _ := resp.Body.Read(b)
			return string(b[:n]), nil
		},
		func(ctx context.Context, tok string) (net.Conn, error) {
			return Dial(ctx, srv.URL, "/dial?t="+tok, "node-token", tlsConf)
		})

	// Two independent streams, like a llama-server head with reconnects.
	for i := 0; i < 2; i++ {
		head, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		head.SetDeadline(time.Now().Add(10 * time.Second))
		head.Write([]byte("hello hive\n"))
		got, err := bufio.NewReader(head).ReadString('\n')
		if err != nil || got != "HELLO HIVE\n" {
			t.Fatalf("stream %d: got %q %v", i, got, err)
		}
		head.Close()
	}

	relay.Detach("n1")
	if _, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		t.Fatal("detached worker port must be closed")
	}
}

// A slow fake GPU must show a proportionally longer waiting time.
func TestMeterSeesSlowWorker(t *testing.T) {
	relay := NewRelay()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /dial", relay.HandleUpgrade)
	mux.HandleFunc("GET /wait/{node}", func(w http.ResponseWriter, r *http.Request) {
		tok, _ := relay.WaitDial(r.Context(), r.PathValue("node"), 2*time.Second)
		w.Write([]byte(tok))
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	tlsConf := srv.Client().Transport.(*http.Transport).TLSClientConfig
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	delays := map[string]time.Duration{"fast": 2 * time.Millisecond, "slow": 20 * time.Millisecond}
	addrs := map[string]string{}
	for node, d := range delays {
		gpu := slowGPU(t, d)
		addrs[node], _ = relay.Attach(node)
		node := node
		go Serve(ctx, gpu,
			func(ctx context.Context) (string, error) {
				req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/wait/"+node, nil)
				resp, err := srv.Client().Do(req)
				if err != nil {
					return "", err
				}
				defer resp.Body.Close()
				b := make([]byte, 64)
				n, _ := resp.Body.Read(b)
				return string(b[:n]), nil
			},
			func(ctx context.Context, tok string) (net.Conn, error) {
				return Dial(ctx, srv.URL, "/dial?t="+tok, "x", tlsConf)
			})
	}
	for node := range delays {
		c, err := net.Dial("tcp", addrs[node])
		if err != nil {
			t.Fatal(err)
		}
		br := bufio.NewReader(c)
		for i := 0; i < 10; i++ {
			c.Write([]byte("compute\n"))
			if _, err := br.ReadString('\n'); err != nil {
				t.Fatal(err)
			}
		}
		c.Close()
	}
	fast, _ := relay.Meter("fast")
	slow, _ := relay.Meter("slow")
	if fast.Rounds != 10 || slow.Rounds != 10 {
		t.Fatalf("rounds: fast %d slow %d", fast.Rounds, slow.Rounds)
	}
	if slow.Wait < 5*fast.Wait || slow.Wait < 200*time.Millisecond {
		t.Fatalf("slow worker not visible: fast %v slow %v", fast.Wait, slow.Wait)
	}
	relay.ResetMeter("slow")
	if m, _ := relay.Meter("slow"); m.Rounds != 0 {
		t.Fatal("reset failed")
	}
}

// slowGPU answers every line after a fixed compute delay.
func slowGPU(t *testing.T, d time.Duration) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					time.Sleep(d)
					c.Write([]byte(line))
				}
			}()
		}
	}()
	return ln.Addr().String()
}
