package coordinator

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sqo4/uley/internal/protocol"
)

const weatherTools = `[{"type":"function","function":{"name":"get_weather","description":"Weather in a city","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}]`

// answerWithTool plays a volunteer that answers the next task with a tool call.
func answerWithTool(t *testing.T, s *Server) protocol.Job {
	reg, _ := s.store.RegisterNode(protocol.RegisterRequest{NodeName: "gpu", OS: "linux"})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, err := s.store.Assign(reg.NodeID, []string{"qwen3-0.6b"}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if j != nil {
			s.store.Complete(reg.NodeID, j.ID, protocol.JobResult{
				ToolCalls:    json.RawMessage(`[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Санкт-Петербург\"}"}}]`),
				FinishReason: "tool_calls", TokensOut: 12, DurationMS: 50,
			})
			s.bell.ring()
			return *j
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no task arrived")
	return protocol.Job{}
}

func postJSON(t *testing.T, url, key, body string) *http.Response {
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestHermesStyleToolCalling(t *testing.T) {
	s, hs := newSocial(t, Config{RequireAPIKey: true})
	vlad := signup(t, hs.URL, "vlad")
	var key APIKey
	vlad.do("POST", "/keys", map[string]string{"name": "Hermes на ноутбуке"}, &key)
	if !strings.HasPrefix(key.Key, "uley-") {
		t.Fatalf("bad key %+v", key)
	}

	// Without a key, or with a wrong one, the API is closed.
	if r := postJSON(t, hs.URL+"/v1/chat/completions", "", `{}`); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", r.StatusCode)
	}
	if r := postJSON(t, hs.URL+"/v1/chat/completions", "uley-wrong", `{}`); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong key: %d", r.StatusCode)
	}

	req, _ := http.NewRequest("GET", hs.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+key.Key)
	resp, _ := http.DefaultClient.Do(req)
	var models struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	json.NewDecoder(resp.Body).Decode(&models)
	if len(models.Data) != 1 || models.Data[0].ID != "qwen3-0.6b" {
		t.Fatalf("models: %+v", models)
	}

	// A second turn of an agent conversation: history with a tool result.
	body := `{"model":"anything","tools":` + weatherTools + `,"tool_choice":"auto","max_tokens":300,
	 "messages":[{"role":"system","content":"You are an agent"},
	  {"role":"user","content":[{"type":"text","text":"Какая погода в Питере?"}]},
	  {"role":"assistant","content":null,"tool_calls":[{"id":"call_0","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Москва\"}"}}]},
	  {"role":"tool","tool_call_id":"call_0","content":"Москва: +5"}]}`
	done := make(chan *http.Response)
	go func() { done <- postJSON(t, hs.URL+"/v1/chat/completions", key.Key, body) }()
	job := answerWithTool(t, s)
	if len(job.Tools) == 0 || len(job.Messages) != 4 || job.Messages[3].Role != "tool" || job.Messages[3].ToolCallID != "call_0" ||
		job.Messages[1].Content != "Какая погода в Питере?" || len(job.Messages[2].ToolCalls) == 0 {
		t.Fatalf("job lost agent fields: %+v", job)
	}
	resp = <-done
	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content   *string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct{ Name, Arguments string }
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if len(out.Choices) != 1 || out.Choices[0].FinishReason != "tool_calls" || out.Model != "anything" ||
		len(out.Choices[0].Message.ToolCalls) != 1 || out.Choices[0].Message.ToolCalls[0].Function.Name != "get_weather" ||
		out.Choices[0].Message.Content != nil {
		t.Fatalf("bad completion %+v", out)
	}

	// The same through streaming, as most agents ask for it.
	go func() {
		done <- postJSON(t, hs.URL+"/v1/chat/completions", key.Key, strings.Replace(body, `"model"`, `"stream":true,"model"`, 1))
	}()
	answerWithTool(t, s)
	resp = <-done
	sc := bufio.NewScanner(resp.Body)
	var sawCall, sawDone bool
	finish := ""
	for sc.Scan() {
		line := sc.Text()
		if line == "data: [DONE]" {
			sawDone = true
			break
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ch struct {
			Choices []struct {
				Delta struct {
					ToolCalls []struct {
						Index *int `json:"index"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		json.Unmarshal([]byte(line[6:]), &ch)
		if len(ch.Choices) > 0 && len(ch.Choices[0].Delta.ToolCalls) == 1 && ch.Choices[0].Delta.ToolCalls[0].Index != nil {
			sawCall = true
		}
		if len(ch.Choices) > 0 && ch.Choices[0].FinishReason != nil {
			finish = *ch.Choices[0].FinishReason
		}
	}
	if !sawCall || !sawDone || finish != "tool_calls" {
		t.Fatalf("stream: call %v done %v finish %q", sawCall, sawDone, finish)
	}

	var keys struct {
		Keys    []APIKey
		Context int
	}
	vlad.do("GET", "/keys", nil, &keys)
	if len(keys.Keys) != 1 || keys.Keys[0].Key != "" || keys.Keys[0].LastUsed == 0 {
		t.Fatalf("key list must hide the secret and show usage: %+v", keys)
	}
	vlad.do("DELETE", "/keys/"+key.ID, nil, nil)
	if r := postJSON(t, hs.URL+"/v1/chat/completions", key.Key, body); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("deleted key still works: %d", r.StatusCode)
	}
}
