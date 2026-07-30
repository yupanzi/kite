package ai

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/gin-gonic/gin"
	"github.com/openai/openai-go"
	"github.com/zxh326/kite/pkg/cluster"
	"github.com/zxh326/kite/pkg/model"
	"github.com/zxh326/kite/pkg/rbac"
	"k8s.io/klog/v2"
)

const systemPrompt = `You are Kite AI, an intelligent assistant for Kubernetes cluster management. You help users understand, monitor, and manage their Kubernetes clusters safely and accurately.

You have access to tools that let you interact with the user's Kubernetes cluster. Use them to:
- Get information about specific resources (pods, deployments, services, etc.)
- List resources across namespaces
- Read pod logs for debugging
- Get cluster-wide status overviews
- Query Prometheus metrics for monitoring data (requires cluster-wide read access)
- Create, update, patch or delete resources
- Manage Helm releases: list them, inspect details/values/history, update values, roll back, or uninstall

Operating principles:
- Tool-calling discipline: ALWAYS invoke tools through the native tool-calling mechanism. NEVER write a tool call as text or XML in your message — do not output strings like "<invoke ...>", "<parameter ...>", or "[Tool: ...]". Any tool call written as plain text is NOT executed and is a bug. If you intend to use a tool, emit a real tool call.
- Evidence first: collect relevant cluster state before conclusions. Do not guess cluster state.
- Read before write: before any mutation operation (create/update/patch/delete), inspect current related resources unless the request is an explicit create with complete details.
- Verify after write: after a mutation, re-check the affected resource(s) and report whether the change actually took effect.
- Scope safety: prefer the smallest safe scope; avoid broad or destructive actions unless the user explicitly asks for them.

Kite RBAC semantics:
- The verbs in Kite only include get, update, delete, create, log, and exec.
- patch is covered by update in Kite RBAC. If update is allowed, patch operations are allowed.
- watch is covered by get in Kite RBAC. If get is allowed, watch-style read operations are allowed.
- Do not treat missing patch or watch entries in RBAC context as denial before verb normalization.
- First check the RBAC context, clarify the permission boundaries. If the resource to be checked exceeds the permission scope, first explain the permission restrictions and suggest the next step.

Context priority:
- Follow explicit user instructions first.
- If user intent does not specify scope, use current page context (resource/namespace) as default scope.
- If scope is still unclear, ask a concise clarification question before mutating resources.

Creation and mutation guardrails:
- For mutation operations (create/update/patch/delete), always include a brief text explanation of what you are about to do alongside the tool call so the user can confirm.
- For create operations, do not assume critical defaults. If missing, ask for required details such as namespace, image/tag, ports/exposure, storage, resource requests/limits, and required config/secrets.
- When you need the user to choose from a short list, use request_choice instead of asking for a typed reply.
- When you need a few structured values, especially for resource creation, use request_form instead of asking the user to type the answers free-form.
- Do not use request_choice or request_form for the final yes/no confirmation of a create/update/patch/delete. After collecting the required inputs, call the mutation tool directly. The system already provides the final confirmation step for mutation tools.
- Do not output secret values. If sensitive fields are involved, summarize safely.

Failure handling:
- On Forbidden errors, explain the permission boundary and provide a least-privilege next step.
- If a tool returns Forbidden, do not retry the same verb/resource/scope. Choose a permitted scope or ask for RBAC changes.
- After a Forbidden result, stop further tool attempts that would require the same or broader permission in the current turn. Ask for a narrower allowed scope or permission update.
- On NotFound errors, confirm namespace/kind/name and suggest nearby resources when possible.
- On validation or apply errors, explain the failing field and provide a minimal fix.

Response style:
- Be concise but thorough.
- When analyzing logs or resource status, provide actionable insights.
- When showing resource details, highlight important fields like status, events, and conditions.
- If you detect issues (CrashLoopBackOff, OOMKilled, pending pods, etc.), proactively suggest solutions.
- Feel free to respond with emojis where appropriate.`

// ChatMessage represents a message in the conversation.
//
// A "tool" role message carries a full tool round-trip (the model's tool call
// plus its result) structurally, so the backend can rebuild real
// tool_use/tool_result blocks. Feeding tool calls back as flattened text
// poisons the model into emitting textual/XML tool calls on later turns.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// Tool round-trip fields (only set when Role == "tool").
	ToolCallID string                 `json:"tool_call_id,omitempty"`
	ToolName   string                 `json:"tool_name,omitempty"`
	ToolArgs   map[string]interface{} `json:"tool_args,omitempty"`
	ToolResult string                 `json:"tool_result,omitempty"`
	IsError    bool                   `json:"is_error,omitempty"`
}

// PageContext provides context about which page the user is viewing.
type PageContext struct {
	Page         string `json:"page"`
	Namespace    string `json:"namespace"`
	ResourceName string `json:"resource_name"`
	ResourceKind string `json:"resource_kind"`
}

// ChatRequest is the incoming chat request.
type ChatRequest struct {
	Messages    []ChatMessage `json:"messages"`
	Language    string        `json:"language,omitempty"`
	PageContext *PageContext  `json:"page_context"`
}

// SSEEvent represents a Server-Sent Event to the client.
type SSEEvent struct {
	Event string      `json:"event"`
	Data  interface{} `json:"data"`
}

// Agent handles the AI conversation loop with tool calling.
type Agent struct {
	provider        string
	openaiClient    openai.Client
	anthropicClient anthropic.Client
	cs              *cluster.ClientSet
	model           string
	maxTokens       int
	effort          string
}

type runtimePromptContext struct {
	ClusterName  string
	AccountName  string
	RBACOverview string
}

// Conversation/message truncation limits. User content and tool results get
// separate budgets: a user message is human-typed and self-limiting, while a
// tool result is sized by the model and one bad call can produce megabytes.
// The Anthropic path targets the current Claude models (1M-token context
// window); the OpenAI path must stay safe for smaller context windows.
//
// maxTotalChars is the load-bearing bound. Per-message caps multiplied by the
// message count do not bound a request — 300 x 200000 is ~15M tokens, far past
// any context window — and the server-side clear_tool_uses backstop only runs on
// the modern Anthropic shape, so legacy models have none. The aggregate budget is
// sized for the smallest context window each provider is realistically pointed
// at (~200K tokens), which holds for a 1M-window model too.
const (
	maxOpenAIConversationMessages = 30
	maxOpenAIMessageChars         = 8000
	maxOpenAIToolResultChars      = 8000
	maxOpenAITotalChars           = 120000

	maxAnthropicConversationMessages = 300
	maxAnthropicMessageChars         = 200000
	maxAnthropicToolResultChars      = 30000
	maxAnthropicTotalChars           = 600000
)

// truncationNotice is appended to content that had to be cut, so neither the
// model nor the user silently reads a sentence that stops mid-word.
const truncationNotice = "\n\n[... truncated by Kite: content exceeded the per-message limit ...]"

// NewAgent creates a new AI agent for a conversation.
func NewAgent(cs *cluster.ClientSet, cfg *RuntimeConfig) (*Agent, error) {
	provider := model.DefaultGeneralAIProvider
	if cfg != nil {
		provider = normalizeProvider(cfg.Provider)
	}

	modelName := model.DefaultGeneralAIModelByProvider(provider)
	if cfg != nil && cfg.Model != "" {
		modelName = cfg.Model
	}

	maxTokens := model.DefaultGeneralAIMaxTokensByProvider(provider)
	if cfg != nil && cfg.MaxTokens > 0 {
		maxTokens = cfg.MaxTokens
	}

	effort := model.DefaultGeneralAIEffort
	if cfg != nil && cfg.Effort != "" {
		effort = model.NormalizeGeneralAIEffort(cfg.Effort)
	}

	agent := &Agent{
		provider:  provider,
		cs:        cs,
		model:     modelName,
		maxTokens: maxTokens,
		effort:    effort,
	}

	switch provider {
	case model.GeneralAIProviderAnthropic:
		client, err := NewAnthropicClient(cfg)
		if err != nil {
			return nil, err
		}
		agent.anthropicClient = client
	default:
		client, err := NewOpenAIClient(cfg)
		if err != nil {
			return nil, err
		}
		agent.openaiClient = client
	}

	return agent, nil
}

// conversationLimits groups the truncation budgets for one provider so call
// sites read by name instead of by the position of four bare ints.
type conversationLimits struct {
	maxMessages        int
	maxChars           int
	maxToolResultChars int
	maxTotalChars      int
}

var (
	openAILimits = conversationLimits{
		maxMessages:        maxOpenAIConversationMessages,
		maxChars:           maxOpenAIMessageChars,
		maxToolResultChars: maxOpenAIToolResultChars,
		maxTotalChars:      maxOpenAITotalChars,
	}
	anthropicLimits = conversationLimits{
		maxMessages:        maxAnthropicConversationMessages,
		maxChars:           maxAnthropicMessageChars,
		maxToolResultChars: maxAnthropicToolResultChars,
		maxTotalChars:      maxAnthropicTotalChars,
	}
)

func normalizeChatMessages(chatMessages []ChatMessage, limits conversationLimits) []ChatMessage {
	if len(chatMessages) > limits.maxMessages {
		chatMessages = chatMessages[len(chatMessages)-limits.maxMessages:]
	}

	normalized := make([]ChatMessage, 0, len(chatMessages))
	for _, msg := range chatMessages {
		if msg.Role == "tool" {
			// Structured tool round-trip. Keep only when it carries a usable
			// id+name+result triple; a tool_use missing any of them (or its
			// matching tool_result) would make the provider request invalid.
			if strings.TrimSpace(msg.ToolCallID) == "" || strings.TrimSpace(msg.ToolName) == "" || strings.TrimSpace(msg.ToolResult) == "" {
				continue
			}
			normalized = append(normalized, ChatMessage{
				Role:       "tool",
				ToolCallID: msg.ToolCallID,
				ToolName:   msg.ToolName,
				ToolArgs:   msg.ToolArgs,
				ToolResult: truncateWithNotice(msg.ToolResult, limits.maxToolResultChars, "tool result "+msg.ToolName),
				IsError:    msg.IsError,
			})
			continue
		}

		content := strings.TrimSpace(msg.Content)

		role := "user"
		if msg.Role == "assistant" {
			role = "assistant"
			// Defensive rescue: strip any tool calls a previous (broken) turn
			// leaked as text/XML, so a poisoned history doesn't re-poison the
			// model on this turn.
			content = strings.TrimSpace(stripLeakedToolCalls(content))
		}

		if content == "" {
			continue
		}

		normalized = append(normalized, ChatMessage{
			Role:    role,
			Content: truncateWithNotice(content, limits.maxChars, role+" message"),
		})
	}

	normalized = trimToTotalBudget(normalized, limits.maxTotalChars)

	// The (possibly truncated) history must start with a user turn so the
	// reconstructed provider messages satisfy the "first message must be user"
	// rule and never begin with an orphaned tool_result.
	for len(normalized) > 0 && normalized[0].Role != "user" {
		normalized = normalized[1:]
	}

	return normalized
}

// truncateRunes caps s at max runes (not bytes), so multi-byte UTF-8 content
// (e.g. Chinese tool output) is never split mid-rune into an invalid sequence.
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	// Byte length <= max guarantees rune count <= max — skip the rune scan.
	if len(s) <= max {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// truncationNoticeRunes is the notice's own cost against a message budget.
var truncationNoticeRunes = utf8.RuneCountInString(truncationNotice)

// truncateWithNotice caps s at max runes and, when content was actually cut,
// appends truncationNotice so the model does not read a sentence that stops
// mid-word with no indication anything is missing. The notice is counted against
// max, so the result never exceeds the cap.
func truncateWithNotice(s string, max int, label string) string {
	if max <= 0 {
		return ""
	}
	// Byte length <= max guarantees rune count <= max, so the common (uncut)
	// case never materializes a []rune. Only content past the byte bound pays
	// the rune count, and that count is exact — unlike len(s) it does not
	// over-report multi-byte content that actually fits.
	if len(s) <= max {
		return s
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	keep := max - truncationNoticeRunes
	if keep <= 0 {
		// The budget cannot fit the notice; fall back to a plain hard cut.
		return truncateRunes(s, max)
	}
	klog.V(2).Infof("AI conversation: truncated %s to %d runes (limit %d)", label, keep, max)
	return truncateRunes(s, keep) + truncationNotice
}

// trimToTotalBudget drops whole messages, oldest first, until the transcript
// fits maxTotalChars. Per-message caps bound one message; only this bounds the
// request, which is what the provider's context window actually limits. Tool
// round-trips are dropped with their result so no orphaned tool_result survives
// (the id/name/result triple is kept intact or removed entirely).
func trimToTotalBudget(messages []ChatMessage, maxTotalChars int) []ChatMessage {
	if maxTotalChars <= 0 {
		return messages
	}

	total := 0
	keepFrom := 0
	for i := len(messages) - 1; i >= 0; i-- {
		size := utf8.RuneCountInString(messages[i].Content) +
			utf8.RuneCountInString(messages[i].ToolResult)
		if total+size > maxTotalChars && i != len(messages)-1 {
			keepFrom = i + 1
			break
		}
		total += size
	}

	if keepFrom == 0 {
		return messages
	}
	klog.V(2).Infof("AI conversation: dropped %d oldest message(s) to fit the %d-rune transcript budget",
		keepFrom, maxTotalChars)
	return messages[keepFrom:]
}

// leakedToolCallPattern matches tool calls that a model previously emitted as
// text/XML instead of via the native tool-calling mechanism: full or partial
// <invoke>/<parameter> blocks (including the antml: namespace) and "[Tool: x]"
// summary markers.
var leakedToolCallPattern = regexp.MustCompile(
	`(?is)<(?:antml:)?invoke\b.*?</(?:antml:)?invoke>` +
		`|</?(?:antml:)?(?:invoke|parameter)\b[^>]*>` +
		`|\[Tool:[^\]]*\]`,
)

// stripLeakedToolCalls removes textual/XML tool-call leakage from assistant
// content: whole <invoke>...</invoke> blocks (with their parameter junk),
// stray invoke/parameter tags, and "[Tool: x]" markers. Surrounding prose is
// left intact.
func stripLeakedToolCalls(content string) string {
	// Two-tier guard, behaviour-identical to running the regex unconditionally.
	// Every alternative in the pattern needs a "<" or a "[", and beyond that the
	// literal "invoke", "parameter", or "[tool:". Assistant turns carry YAML,
	// code, and shell output where "<" alone is ubiquitous, so checking only for
	// it sent nearly every message through a backtracking scan of the whole
	// (up to maxAnthropicMessageChars) string. The fold is case-insensitive
	// because the pattern is, and it only runs for candidates.
	if !strings.ContainsAny(content, "<[") {
		return content
	}
	lower := strings.ToLower(content)
	if !strings.Contains(lower, "invoke") &&
		!strings.Contains(lower, "parameter") &&
		!strings.Contains(lower, "[tool:") {
		return content
	}
	return leakedToolCallPattern.ReplaceAllString(content, "")
}

func summarizeScope(items []string) string {
	if len(items) == 0 {
		return "-"
	}
	scope := strings.Join(items, ",")
	if strings.Contains(scope, "get") {
		scope += ",list,watch"
	}
	return scope
}

func buildRBACOverview(user model.User) string {
	roles := rbac.GetUserRoles(user)
	if len(roles) == 0 {
		return "no roles"
	}

	sort.Slice(roles, func(i, j int) bool {
		return roles[i].Name < roles[j].Name
	})

	summaries := make([]string, 0, len(roles))
	for _, role := range roles {
		summaries = append(summaries, fmt.Sprintf(
			"%s[clusters=%s;namespaces=%s;resources=%s;verbs=%s]",
			role.Name,
			summarizeScope(role.Clusters),
			summarizeScope(role.Namespaces),
			summarizeScope(role.Resources),
			summarizeScope(role.Verbs),
		))
	}
	return strings.Join(summaries, " | ")
}

func buildRuntimePromptContext(c *gin.Context, cs *cluster.ClientSet) runtimePromptContext {
	ctx := runtimePromptContext{}
	if cs != nil {
		ctx.ClusterName = cs.Name
	}
	if c == nil {
		return ctx
	}
	rawUser, ok := c.Get("user")
	if !ok {
		return ctx
	}
	user, ok := rawUser.(model.User)
	if !ok {
		return ctx
	}
	ctx.AccountName = user.Key()
	ctx.RBACOverview = buildRBACOverview(user)
	return ctx
}

// contextualPromptSuffix returns the per-request/per-session context appended
// after the stable system prompt: current time, runtime context, page context,
// and response-language guidance. It is kept separate from the systemPrompt
// constant so the Anthropic path can cache the stable prefix while sending this
// volatile remainder uncached — a timestamp inside the cached block would
// invalidate the prompt cache on every request.
func contextualPromptSuffix(pageCtx *PageContext, runtimeCtx runtimePromptContext, language string) string {
	var b strings.Builder

	// Current system time
	fmt.Fprintf(&b, "\n\nCurrent system time: %s", time.Now().Format("2006-01-02 15:04:05 MST"))

	if runtimeCtx.ClusterName != "" || runtimeCtx.AccountName != "" || runtimeCtx.RBACOverview != "" {
		b.WriteString("\n\nCurrent runtime context:")
		if runtimeCtx.ClusterName != "" {
			fmt.Fprintf(&b, "\n- Current cluster: %s", runtimeCtx.ClusterName)
		}
		if runtimeCtx.AccountName != "" {
			fmt.Fprintf(&b, "\n- Current account name: %s", runtimeCtx.AccountName)
		}
		if runtimeCtx.RBACOverview != "" {
			fmt.Fprintf(&b, "\n- RBAC overview: %s", runtimeCtx.RBACOverview)
		}
	}

	if pageCtx != nil {
		b.WriteString("\n\nCurrent page context:")
		if pageCtx.Page != "" {
			fmt.Fprintf(&b, "\n- User is viewing: %s", pageCtx.Page)
		}
		if pageCtx.ResourceKind != "" && pageCtx.ResourceName != "" {
			fmt.Fprintf(&b, "\n- Current resource: %s/%s", pageCtx.ResourceKind, pageCtx.ResourceName)
		}
		if pageCtx.Namespace != "" {
			fmt.Fprintf(&b, "\n- Current namespace: %s", pageCtx.Namespace)
		}

		// Add contextual suggestions
		switch pageCtx.Page {
		case "overview":
			b.WriteString("\n- Suggest analyzing overall cluster health, resource utilization, and potential issues.")
		case "pod-detail":
			b.WriteString("\n- Focus on this pod's status, logs, events, and health. Proactively check for issues.")
		case "deployment-detail":
			b.WriteString("\n- Focus on this deployment's rollout status, replica health, and recent changes.")
		case "node-detail":
			b.WriteString("\n- Focus on this node's status, resource pressure, and pods running on it.")
		}
	}

	if language == "zh" {
		b.WriteString("\n\nResponse language:\n- Prefer replying in the same language as the user's latest message.\n- If the user's latest message language is unclear, respond in Simplified Chinese unless the user explicitly asks for another language.")
	} else {
		b.WriteString("\n\nResponse language:\n- Prefer replying in the same language as the user's latest message.\n- If the user's latest message language is unclear, respond in English unless the user explicitly asks for another language.")
	}

	return b.String()
}

// buildContextualSystemPrompt augments the system prompt with runtime/page
// context. Used by the OpenAI path (single system message); the Anthropic path
// caches systemPrompt and sends contextualPromptSuffix as a separate block.
func buildContextualSystemPrompt(pageCtx *PageContext, runtimeCtx runtimePromptContext, language string) string {
	prompt := systemPrompt + contextualPromptSuffix(pageCtx, runtimeCtx, language)
	klog.V(4).Infof("system prompt %s", prompt)
	return prompt
}

// ProcessChat runs the AI conversation loop and sends SSE events via the callback.
func (a *Agent) ProcessChat(c *gin.Context, req *ChatRequest, sendEvent func(SSEEvent)) {
	switch a.provider {
	case model.GeneralAIProviderAnthropic:
		a.processChatAnthropic(c, req, sendEvent)
	default:
		a.processChatOpenAI(c, req, sendEvent)
	}
}

func (a *Agent) ContinuePendingAction(c *gin.Context, sessionID string, sendEvent func(SSEEvent)) error {
	session, err := agentPendingSessions.take(sessionID)
	if err != nil {
		return err
	}

	switch session.Provider {
	case model.GeneralAIProviderAnthropic:
		return a.continueChatAnthropic(c, session, sendEvent)
	default:
		return a.continueChatOpenAI(c, session, sendEvent)
	}
}

func (a *Agent) ContinuePendingInput(c *gin.Context, sessionID string, values map[string]interface{}, sendEvent func(SSEEvent)) error {
	session, err := agentPendingSessions.load(sessionID)
	if err != nil {
		return err
	}
	if !InteractionTools[session.ToolCall.Name] {
		return fmt.Errorf("pending input not found or expired")
	}

	request, err := parseInteractionRequest(session.ToolCall.Name, session.ToolCall.Args)
	if err != nil {
		return err
	}
	result, err := buildInteractionToolResult(request, values)
	if err != nil {
		return err
	}

	agentPendingSessions.delete(sessionID)

	switch session.Provider {
	case model.GeneralAIProviderAnthropic:
		return a.continueChatAnthropicWithToolResult(c, session, result, false, sendEvent)
	default:
		return a.continueChatOpenAIWithToolResult(c, session, result, false, sendEvent)
	}
}

func parseToolCallArguments(raw string) (map[string]interface{}, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]interface{}{}, nil
	}

	args := map[string]interface{}{}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, err
	}
	return args, nil
}

// toolArgsJSON marshals tool-call arguments to a JSON object string, defaulting
// to "{}" when the args are nil, empty, or marshal to null. It is the inverse of
// parseToolCallArguments and the single source of the empty-args convention
// shared by the OpenAI and Anthropic message builders.
func toolArgsJSON(args any) string {
	raw, err := json.Marshal(args)
	if err != nil {
		return "{}"
	}
	if trimmed := strings.TrimSpace(string(raw)); trimmed != "" && trimmed != "null" {
		return trimmed
	}
	return "{}"
}

type streamedToolCall struct {
	Index     int64
	ID        string
	Name      string
	Arguments string
}

// MarshalSSEEvent marshals an SSE event to JSON for sending.
func MarshalSSEEvent(event SSEEvent) string {
	data, err := json.Marshal(event.Data)
	if err != nil {
		return "event: error\ndata: {\"message\":\"marshal error\"}\n\n"
	}
	return fmt.Sprintf("event: %s\ndata: %s\n\n", event.Event, string(data))
}
