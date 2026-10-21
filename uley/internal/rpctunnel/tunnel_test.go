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
