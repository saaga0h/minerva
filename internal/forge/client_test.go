package forge

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func newTestLogger() *logrus.Logger {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel) // suppress noise in test output
	return log
}

// --- handleResponse dispatch ---

func TestHandleResponse_RoutesToPendingChannel(t *testing.T) {
	c := &Client{logger: newTestLogger()}

	ch := make(chan forgeResponse, 1)
	c.pending.Store("corr-1", ch)

	resp := forgeResponse{
		CorrelationID: "corr-1",
		Success:       true,
		Result:        json.RawMessage(`{"embedding":[0.1,0.2]}`),
	}
	payload, _ := json.Marshal(resp)
	c.handleResponse(nil, &fakeMsg{payload: payload})

	select {
	case got := <-ch:
		if got.CorrelationID != "corr-1" {
			t.Fatalf("expected corr-1, got %s", got.CorrelationID)
		}
		if !got.Success {
			t.Fatal("expected success=true")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("response not delivered to channel")
	}
}

func TestHandleResponse_UnknownCorrelationIDIsDropped(t *testing.T) {
	c := &Client{logger: newTestLogger()}

	resp := forgeResponse{CorrelationID: "unknown-id", Success: true}
	payload, _ := json.Marshal(resp)

	// Should not panic or block.
	c.handleResponse(nil, &fakeMsg{payload: payload})
}

func TestHandleResponse_MalformedPayloadIsDropped(t *testing.T) {
	c := &Client{logger: newTestLogger()}
	// Should not panic.
	c.handleResponse(nil, &fakeMsg{payload: []byte(`not json`)})
}

func TestHandleResponse_FullChannelDropsGracefully(t *testing.T) {
	c := &Client{logger: newTestLogger()}

	// Buffer size 1, already full.
	ch := make(chan forgeResponse, 1)
	ch <- forgeResponse{CorrelationID: "corr-full"}
	c.pending.Store("corr-full", ch)

	resp := forgeResponse{CorrelationID: "corr-full", Success: true}
	payload, _ := json.Marshal(resp)

	// Should not block even though channel is full.
	done := make(chan struct{})
	go func() {
		c.handleResponse(nil, &fakeMsg{payload: payload})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("handleResponse blocked on full channel")
	}
}

// --- concurrent pending map safety ---

func TestHandleResponse_ConcurrentDispatch(t *testing.T) {
	c := &Client{logger: newTestLogger()}

	const n = 50
	channels := make([]chan forgeResponse, n)
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		ids[i] = corrID(i)
		ch := make(chan forgeResponse, 1)
		channels[i] = ch
		c.pending.Store(ids[i], ch)
	}

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			resp := forgeResponse{CorrelationID: ids[idx], Success: true}
			payload, _ := json.Marshal(resp)
			c.handleResponse(nil, &fakeMsg{payload: payload})
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		select {
		case got := <-channels[i]:
			if got.CorrelationID != ids[i] {
				t.Errorf("channel %d got wrong correlation ID: %s", i, got.CorrelationID)
			}
		case <-time.After(200 * time.Millisecond):
			t.Errorf("channel %d never received a response", i)
		}
	}
}

// --- error response propagated to caller ---

func TestHandleResponse_ErrorResponseDelivered(t *testing.T) {
	c := &Client{logger: newTestLogger()}

	ch := make(chan forgeResponse, 1)
	c.pending.Store("corr-err", ch)

	resp := forgeResponse{
		CorrelationID: "corr-err",
		Success:       false,
		Error:         "GPU OOM",
	}
	payload, _ := json.Marshal(resp)
	c.handleResponse(nil, &fakeMsg{payload: payload})

	got := <-ch
	if got.Success {
		t.Fatal("expected success=false")
	}
	if got.Error != "GPU OOM" {
		t.Fatalf("expected 'GPU OOM', got %q", got.Error)
	}
}

// --- result type unmarshalling ---

func TestEmbedResult_Unmarshal(t *testing.T) {
	raw := json.RawMessage(`{"embedding":[0.1,0.2,0.3]}`)
	var er embedResult
	if err := json.Unmarshal(raw, &er); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(er.Embedding) != 3 {
		t.Fatalf("expected 3 floats, got %d", len(er.Embedding))
	}
	if er.Embedding[0] != float32(0.1) {
		t.Fatalf("unexpected first value: %f", er.Embedding[0])
	}
}

func TestChatResult_Unmarshal(t *testing.T) {
	raw := json.RawMessage(`{"response":"hello","prompt_tokens":10,"completion_tokens":5}`)
	var cr chatResult
	if err := json.Unmarshal(raw, &cr); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if cr.Response != "hello" {
		t.Fatalf("expected 'hello', got %q", cr.Response)
	}
	if cr.PromptTokens != 10 || cr.CompletionTokens != 5 {
		t.Fatalf("unexpected token counts: %d/%d", cr.PromptTokens, cr.CompletionTokens)
	}
}

// TestEmbedResult_EmptyEmbedding verifies zero-length slice doesn't panic downstream.
func TestEmbedResult_EmptyEmbedding(t *testing.T) {
	raw := json.RawMessage(`{"embedding":[]}`)
	var er embedResult
	if err := json.Unmarshal(raw, &er); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(er.Embedding) != 0 {
		t.Fatalf("expected empty, got len=%d", len(er.Embedding))
	}
}

// --- helpers ---

func corrID(i int) string {
	return string(rune('a'+i%26)) + string(rune('0'+i/26))
}

// fakeMsg implements paho.Message minimally.
type fakeMsg struct{ payload []byte }

func (m *fakeMsg) Duplicate() bool   { return false }
func (m *fakeMsg) Qos() byte         { return 1 }
func (m *fakeMsg) Retained() bool    { return false }
func (m *fakeMsg) Topic() string     { return "compute/response/test/corr" }
func (m *fakeMsg) MessageID() uint16 { return 0 }
func (m *fakeMsg) Payload() []byte   { return m.payload }
func (m *fakeMsg) Ack()              {}
