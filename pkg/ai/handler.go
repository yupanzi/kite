package ai

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/zxh326/kite/pkg/cluster"
	"k8s.io/klog/v2"
)

// sseKeepaliveInterval is how often an idle stream emits a comment line. An
// agent turn is legitimately silent while a tool runs or the model thinks, and
// ingress-nginx closes a connection after 60s with no bytes from the backend
// (proxy-read-timeout), so silence has to be broken well inside that window.
const sseKeepaliveInterval = 20 * time.Second

// newStreamSender wires the SSE headers and returns a send function plus a stop
// function. Writes are mutex-guarded because the keepalive ticker runs on its
// own goroutine and gin's ResponseWriter is not safe for concurrent use. Always
// `defer stop()` — it ends the ticker goroutine.
func newStreamSender(c *gin.Context) (send func(SSEEvent), stop func()) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	var mu sync.Mutex
	send = func(event SSEEvent) {
		data := MarshalSSEEvent(event)
		mu.Lock()
		defer mu.Unlock()
		_, _ = fmt.Fprint(c.Writer, data)
		c.Writer.Flush()
	}

	done := make(chan struct{})
	var once sync.Once
	ticker := time.NewTicker(sseKeepaliveInterval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-c.Request.Context().Done():
				return
			case <-ticker.C:
				// An SSE comment line: clients ignore it per spec, proxies see
				// traffic. Written through the same lock as real events.
				mu.Lock()
				_, err := fmt.Fprint(c.Writer, ": keepalive\n\n")
				c.Writer.Flush()
				mu.Unlock()
				if err != nil {
					klog.V(4).Infof("SSE keepalive write failed, stopping: %v", err)
					return
				}
			}
		}
	}()

	return send, func() { once.Do(func() { close(done) }) }
}

// HandleChat handles the SSE streaming chat endpoint.
func HandleChat(c *gin.Context) {
	cfg, err := LoadRuntimeConfig()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to load AI config: %v", err)})
		return
	}
	if !cfg.Enabled {
		c.JSON(http.StatusBadRequest, gin.H{"error": "AI is not enabled"})
		return
	}

	var req ChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Invalid request: %v", err)})
		return
	}
	req.Language = detectRequestLanguage(req.Language, c.GetHeader("Accept-Language"))

	if len(req.Messages) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No messages provided"})
		return
	}

	clientSet, ok := getClusterClientSet(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No cluster selected"})
		return
	}

	agent, err := NewAgent(clientSet, cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to create AI agent: %v", err)})
		return
	}

	sendEvent, stopKeepalive := newStreamSender(c)
	defer stopKeepalive()

	agent.ProcessChat(c, &req, sendEvent)

	stopKeepalive()
	sendEvent(SSEEvent{Event: "done", Data: map[string]string{}})
}

type ContinueRequest struct {
	SessionID string `json:"sessionId"`
}

type ContinueInputRequest struct {
	SessionID string                 `json:"sessionId"`
	Values    map[string]interface{} `json:"values"`
}

// HandleExecuteContinue resumes a pending AI action after user confirmation.
func HandleExecuteContinue(c *gin.Context) {
	cfg, err := LoadRuntimeConfig()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to load AI config: %v", err)})
		return
	}
	if !cfg.Enabled {
		c.JSON(http.StatusBadRequest, gin.H{"error": "AI is not enabled"})
		return
	}

	var req ContinueRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Invalid request: %v", err)})
		return
	}
	if strings.TrimSpace(req.SessionID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sessionId is required"})
		return
	}

	clientSet, ok := getClusterClientSet(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No cluster selected"})
		return
	}

	agent, err := NewAgent(clientSet, cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to create AI agent: %v", err)})
		return
	}

	sendEvent, stopKeepalive := newStreamSender(c)
	defer stopKeepalive()

	if err := agent.ContinuePendingAction(c, req.SessionID, sendEvent); err != nil {
		sendEvent(SSEEvent{Event: "error", Data: map[string]string{"message": err.Error()}})
	}

	stopKeepalive()
	sendEvent(SSEEvent{Event: "done", Data: map[string]string{}})
}

func HandleInputContinue(c *gin.Context) {
	cfg, err := LoadRuntimeConfig()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to load AI config: %v", err)})
		return
	}
	if !cfg.Enabled {
		c.JSON(http.StatusBadRequest, gin.H{"error": "AI is not enabled"})
		return
	}

	var req ContinueInputRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Invalid request: %v", err)})
		return
	}
	if strings.TrimSpace(req.SessionID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sessionId is required"})
		return
	}

	clientSet, ok := getClusterClientSet(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No cluster selected"})
		return
	}

	agent, err := NewAgent(clientSet, cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to create AI agent: %v", err)})
		return
	}

	sendEvent, stopKeepalive := newStreamSender(c)
	defer stopKeepalive()

	if err := agent.ContinuePendingInput(c, req.SessionID, req.Values, sendEvent); err != nil {
		sendEvent(SSEEvent{Event: "error", Data: map[string]string{"message": err.Error()}})
	}

	stopKeepalive()
	sendEvent(SSEEvent{Event: "done", Data: map[string]string{}})
}

func getClusterClientSet(c *gin.Context) (*cluster.ClientSet, bool) {
	cs, exists := c.Get("cluster")
	if !exists {
		return nil, false
	}
	clientSet, ok := cs.(*cluster.ClientSet)
	return clientSet, ok
}
