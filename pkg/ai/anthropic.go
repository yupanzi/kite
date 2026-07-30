package ai

import (
	"context"
	"fmt"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/gin-gonic/gin"
	"github.com/zxh326/kite/pkg/model"
	"k8s.io/klog/v2"
)

func toolUseInput(args map[string]interface{}) any {
	if args == nil {
		return map[string]interface{}{}
	}
	return args
}

// legacyAnthropicModelMarkers identifies models that predate the Opus-4.6-era
// request surface and 400 on output effort / adaptive thinking / context
// management. Markers are matched as substrings against a separator-normalized
// ID (see normalizeAnthropicModelID), so dotted gateway aliases
// ("claude-3.5-sonnet") and Vertex dated snapshots ("claude-sonnet-4@20250514")
// match the same markers as the first-party hyphen form. "haiku" is deliberately
// broad: a future Haiku is more likely to follow the small-model tier, and being
// wrong there costs a missed optimization rather than a failed request.
var legacyAnthropicModelMarkers = []string{
	"opus-4-5", "opus-4-1", "opus-4-0", "opus-4-20",
	"sonnet-4-5", "sonnet-4-0", "sonnet-4-20",
	"claude-3-", "claude-2-", "claude-v2", "claude-v1", "claude-instant", "haiku",
}

// normalizeAnthropicModelID folds the separator variants the same model ID
// appears under across first-party, Bedrock, Vertex, and OpenAI-compatible
// gateways ('.', '@', ':', '/', '_') to '-', so one marker matches all of them.
// A trailing "-latest"/"-v1"-style suffix is left alone; markers are prefixes of
// the version segment, not anchored at the end.
func normalizeAnthropicModelID(modelName string) string {
	m := strings.ToLower(strings.TrimSpace(modelName))
	for _, sep := range []string{".", "@", ":", "/", "_"} {
		m = strings.ReplaceAll(m, sep, "-")
	}
	return m
}

// anthropicModelSupportsModernFeatures reports whether the configured model
// accepts the Opus-4.6-era request surface: output effort, adaptive thinking,
// and context management. This is a deny list on purpose — an allow list of
// known-good version strings silently misses every future model, and falling
// back to the plain shape keeps a small max_tokens while the server still spends
// it on thinking. Unknown/newer Claude models therefore get the modern shape.
func anthropicModelSupportsModernFeatures(modelName string) bool {
	m := normalizeAnthropicModelID(modelName)
	// Non-Claude identifiers (empty, OpenAI-compatible gateway names) get the
	// plain shape: nothing guarantees they accept the Anthropic beta surface.
	if !strings.Contains(m, "claude") && !strings.Contains(m, "fable") && !strings.Contains(m, "mythos") {
		return false
	}
	for _, marker := range legacyAnthropicModelMarkers {
		if strings.Contains(m, marker) {
			return false
		}
	}
	return true
}

// anthropicOutputEffort maps the persisted effort setting onto the SDK constant.
// Effort is the depth knob on the modern request surface: budget_tokens was
// removed and 400s, so this is the only way to ask for more thinking. All five
// levels are accepted on current models.
func anthropicOutputEffort(effort string) anthropic.BetaOutputConfigEffort {
	switch model.NormalizeGeneralAIEffort(effort) {
	case model.GeneralAIEffortLow:
		return anthropic.BetaOutputConfigEffortLow
	case model.GeneralAIEffortMedium:
		return anthropic.BetaOutputConfigEffortMedium
	case model.GeneralAIEffortHigh:
		return anthropic.BetaOutputConfigEffortHigh
	case model.GeneralAIEffortMax:
		return anthropic.BetaOutputConfigEffortMax
	default:
		return anthropic.BetaOutputConfigEffortXhigh
	}
}

// emptyAnthropicResponseMessage explains an empty response using the provider's
// own stop_reason. A bare "AI returned no content" hides the two causes an
// operator can actually act on — an exhausted output budget and an exceeded
// context window — behind a message that reads like a Kite bug.
func emptyAnthropicResponseMessage(stopReason anthropic.BetaStopReason, maxTokens int) string {
	switch stopReason {
	case anthropic.BetaStopReasonMaxTokens:
		return fmt.Sprintf("The model hit the Max Tokens limit (%d) before producing an answer. "+
			"On current Claude models thinking and answer share this budget — raise Max Tokens, "+
			"or lower Reasoning Effort, in Settings.", maxTokens)
	case anthropic.BetaStopReasonModelContextWindowExceeded:
		return "The conversation exceeded the model's context window. Start a new chat, " +
			"or narrow the tool queries so less output is carried forward."
	case anthropic.BetaStopReasonRefusal:
		return "The model declined this request."
	case anthropic.BetaStopReasonPauseTurn:
		return "The model paused mid-turn without producing content. Send the message again to resume."
	default:
		if stopReason != "" {
			return fmt.Sprintf("AI returned no content (stop_reason: %s)", stopReason)
		}
		return "AI returned no content"
	}
}

func toAnthropicMessages(chatMessages []ChatMessage) []anthropic.BetaMessageParam {
	normalized := normalizeChatMessages(chatMessages, anthropicLimits)

	// Append blocks under a role, coalescing into the previous message when it
	// shares that role. Anthropic requires strictly alternating user/assistant
	// roles, so an assistant text turn immediately followed by an assistant
	// tool_use turn must merge into one message.
	messages := make([]anthropic.BetaMessageParam, 0, len(normalized))
	push := func(role string, blocks ...anthropic.BetaContentBlockParamUnion) {
		if n := len(messages); n > 0 && string(messages[n-1].Role) == role {
			messages[n-1].Content = append(messages[n-1].Content, blocks...)
			return
		}
		if role == "assistant" {
			messages = append(messages, anthropic.BetaMessageParam{Role: "assistant", Content: blocks})
		} else {
			messages = append(messages, anthropic.NewBetaUserMessage(blocks...))
		}
	}

	for _, msg := range normalized {
		switch msg.Role {
		case "assistant":
			push("assistant", anthropic.NewBetaTextBlock(msg.Content))
		case "tool":
			// A tool round-trip expands to an assistant tool_use block followed
			// by a user tool_result block — the structured pair the model needs,
			// never flattened text.
			push("assistant", anthropic.BetaContentBlockParamUnion{OfToolUse: &anthropic.BetaToolUseBlockParam{
				ID:    msg.ToolCallID,
				Name:  msg.ToolName,
				Input: toolUseInput(msg.ToolArgs),
			}})
			push("user", anthropic.NewBetaToolResultBlock(msg.ToolCallID, msg.ToolResult, msg.IsError))
		default:
			push("user", anthropic.NewBetaTextBlock(msg.Content))
		}
	}

	return messages
}

func (a *Agent) processChatAnthropic(c *gin.Context, req *ChatRequest, sendEvent func(SSEEvent)) {
	ctx := c.Request.Context()
	runtimeCtx := buildRuntimePromptContext(c, a.cs)
	language := normalizeLanguage(req.Language)
	if language == "" {
		language = "en"
	}
	// The stable systemPrompt is cached inside runAnthropicConversation; only the
	// volatile per-request context travels here, as a separate uncached block.
	sysPromptSuffix := contextualPromptSuffix(req.PageContext, runtimeCtx, language)
	messages := toAnthropicMessages(req.Messages)
	a.runAnthropicConversation(ctx, c, sysPromptSuffix, messages, sendEvent)
}

func (a *Agent) continueChatAnthropic(c *gin.Context, session pendingSession, sendEvent func(SSEEvent)) error {
	ctx := c.Request.Context()
	result, isError := ExecuteTool(ctx, c, a.cs, session.ToolCall.Name, session.ToolCall.Args)
	return a.continueChatAnthropicWithToolResult(c, session, result, isError, sendEvent)
}

func (a *Agent) continueChatAnthropicWithToolResult(c *gin.Context, session pendingSession, result string, isError bool, sendEvent func(SSEEvent)) error {
	ctx := c.Request.Context()
	result = truncateWithNotice(result, maxAnthropicToolResultChars, "tool result "+session.ToolCall.Name)
	sendEvent(SSEEvent{
		Event: "tool_result",
		Data:  buildToolResultEventData(session.ToolCall.ID, session.ToolCall.Name, result, isError),
	})

	toolResult := result
	if isError {
		toolResult = "Tool error: " + result
	}

	session.AnthropicMessages = append(
		session.AnthropicMessages,
		anthropic.NewBetaUserMessage(
			anthropic.NewBetaToolResultBlock(session.ToolCall.ID, toolResult, isError),
		),
	)
	a.runAnthropicConversation(ctx, c, session.SystemPrompt, session.AnthropicMessages, sendEvent)
	return nil
}

func (a *Agent) runAnthropicConversation(
	ctx context.Context,
	c *gin.Context,
	sysPromptSuffix string,
	messages []anthropic.BetaMessageParam,
	sendEvent func(SSEEvent),
) {
	if len(messages) == 0 {
		sendEvent(SSEEvent{Event: "error", Data: map[string]string{"message": "No conversation messages to send"}})
		return
	}

	tools := BetaAnthropicToolDefs(a.cs)
	modern := anthropicModelSupportsModernFeatures(a.model)

	// Cache the large, fixed system prompt together with the tool definitions
	// that render before it — a stable prefix multi-turn conversations read at
	// ~0.1x price. The volatile per-request context (time, cluster, page) rides
	// in a second, uncached block so it can't invalidate the cached prefix.
	system := []anthropic.BetaTextBlockParam{{
		Text:         systemPrompt,
		CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
	}}
	if strings.TrimSpace(sysPromptSuffix) != "" {
		system = append(system, anthropic.BetaTextBlockParam{Text: sysPromptSuffix})
	}

	// max_tokens is sent as configured. It is a ceiling on thinking + answer
	// combined, but an unused ceiling costs nothing, so silently raising an
	// operator's explicit budget would only inflate their bill. Depth is asked
	// for with output effort below, not by inflating this number.
	maxTokens := a.maxTokens

	maxIterations := 100
	for i := 0; i < maxIterations; i++ {
		params := anthropic.BetaMessageNewParams{
			Model:     a.model,
			Messages:  messages,
			System:    system,
			Tools:     tools,
			MaxTokens: int64(maxTokens),
			ToolChoice: anthropic.BetaToolChoiceUnionParam{
				// Serialize tool calls: the confirmation/pause-resume flow carries
				// one pending tool per turn, and parallel tool_use in a single
				// assistant turn would split its tool_results across two user
				// messages on resume — an invalid, alternation-breaking request.
				OfAuto: &anthropic.BetaToolChoiceAutoParam{
					DisableParallelToolUse: anthropic.Bool(true),
				},
			},
		}

		if modern {
			// Opus 4.x request surface — older models (e.g. claude-sonnet-4-5)
			// 400 on these, so apply them only when the model supports them.
			// display:"summarized" keeps the streamed think content populated;
			// the default "omitted" would blank out the UI's thinking bubble.
			params.Thinking = anthropic.BetaThinkingConfigParamUnion{OfAdaptive: &anthropic.BetaThinkingConfigAdaptiveParam{
				Display: anthropic.BetaThinkingConfigAdaptiveDisplaySummarized,
			}}
			params.OutputConfig = anthropic.BetaOutputConfigParam{Effort: anthropicOutputEffort(a.effort)}
			// Context editing: server-side clears the oldest tool results once the
			// transcript grows large, keeping long agent loops within budget
			// without summarizing. No-op on gateways that don't honor the beta.
			params.ContextManagement = anthropic.BetaContextManagementConfigParam{
				Edits: []anthropic.BetaContextManagementConfigEditUnionParam{
					{OfClearToolUses20250919: &anthropic.BetaClearToolUses20250919EditParam{}},
				},
			}
			params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaContextManagement2025_06_27}
		}

		stream := a.anthropicClient.Beta.Messages.NewStreaming(ctx, params)

		message, messageContent, thinkingContent, streamedToolCalls, err := consumeAnthropicStreamingResponse(stream, sendEvent)
		if err != nil {
			klog.Errorf("AI generation error: %v", err)
			sendEvent(SSEEvent{Event: "error", Data: map[string]string{"message": fmt.Sprintf("AI error: %v", err)}})
			return
		}

		klog.V(2).Infof("Anthropic usage: input=%d cache_read=%d cache_write=%d output=%d",
			message.Usage.InputTokens, message.Usage.CacheReadInputTokens, message.Usage.CacheCreationInputTokens, message.Usage.OutputTokens)

		if len(streamedToolCalls) == 0 {
			content := strings.TrimSpace(messageContent)
			if content == "" && strings.TrimSpace(thinkingContent) == "" {
				sendEvent(SSEEvent{Event: "error", Data: map[string]string{
					"message": emptyAnthropicResponseMessage(message.StopReason, maxTokens),
				}})
				return
			}
			return
		}

		messages = append(messages, message.ToParam())
		toolResults := make([]anthropic.BetaContentBlockParamUnion, 0, len(streamedToolCalls))

		for _, tc := range streamedToolCalls {
			toolName := tc.Name
			args, err := parseToolCallArguments(tc.Arguments)
			if err != nil {
				klog.Errorf("Failed to parse tool arguments: %v", err)
				toolError := fmt.Sprintf("Failed to parse arguments: %v", err)
				toolResults = append(toolResults, anthropic.NewBetaToolResultBlock(tc.ID, "Tool error: "+toolError, true))
				continue
			}

			sendEvent(SSEEvent{
				Event: "tool_call",
				Data:  buildToolCallEventData(tc, args),
			})

			if InteractionTools[toolName] {
				request, err := parseInteractionRequest(toolName, args)
				if err != nil {
					result := "Error: " + err.Error()
					sendEvent(SSEEvent{
						Event: "tool_result",
						Data:  buildToolResultEventData(tc.ID, toolName, result, true),
					})
					toolResults = append(toolResults, anthropic.NewBetaToolResultBlock(tc.ID, "Tool error: "+result, true))
					continue
				}
				if len(toolResults) > 0 {
					messages = append(messages, anthropic.NewBetaUserMessage(toolResults...))
					toolResults = nil
				}
				sessionID := agentPendingSessions.save(pendingSession{
					Provider:          a.provider,
					SystemPrompt:      sysPromptSuffix,
					AnthropicMessages: append([]anthropic.BetaMessageParam(nil), messages...),
					ToolCall: pendingToolCall{
						ID:   tc.ID,
						Name: toolName,
						Args: args,
					},
				})
				if sessionID == "" {
					errorMsg := "Failed to save pending session"
					toolResults = append(toolResults, anthropic.NewBetaToolResultBlock(tc.ID, "Tool error: "+errorMsg, true))
					continue
				}
				sendEvent(SSEEvent{
					Event: "input_required",
					Data:  buildInteractionEventData(toolName, tc.ID, sessionID, request),
				})
				return
			}

			if MutationTools[toolName] {
				result, isError := AuthorizeTool(c, a.cs, toolName, args)
				if isError {
					sendEvent(SSEEvent{
						Event: "tool_result",
						Data:  buildToolResultEventData(tc.ID, toolName, result, true),
					})
					toolResults = append(toolResults, anthropic.NewBetaToolResultBlock(tc.ID, "Tool error: "+result, true))
					continue
				}
				if len(toolResults) > 0 {
					messages = append(messages, anthropic.NewBetaUserMessage(toolResults...))
				}
				sessionID := agentPendingSessions.save(pendingSession{
					Provider:          a.provider,
					SystemPrompt:      sysPromptSuffix,
					AnthropicMessages: append([]anthropic.BetaMessageParam(nil), messages...),
					ToolCall: pendingToolCall{
						ID:   tc.ID,
						Name: toolName,
						Args: args,
					},
				})
				if sessionID == "" {
					errorMsg := "Failed to save pending session"
					toolResults = append(toolResults, anthropic.NewBetaToolResultBlock(tc.ID, "Tool error: "+errorMsg, true))
					continue
				}
				sendEvent(SSEEvent{
					Event: "action_required",
					Data:  buildActionRequiredEventData(tc, sessionID, args),
				})
				return
			}

			result, isError := ExecuteTool(ctx, c, a.cs, toolName, args)
			// Cap the live result the same way replayed history is capped. The
			// per-tool bounds (log bytes, item counts) do not cover every tool —
			// get_resource yaml-marshals whole objects — so without this an
			// oversized result is only trimmed on the *next* turn, after the
			// oversized request has already been sent.
			result = truncateWithNotice(result, maxAnthropicToolResultChars, "tool result "+toolName)

			sendEvent(SSEEvent{
				Event: "tool_result",
				Data:  buildToolResultEventData(tc.ID, toolName, result, isError),
			})

			if isError {
				result = "Tool error: " + result
			}
			toolResults = append(toolResults, anthropic.NewBetaToolResultBlock(tc.ID, result, isError))
		}

		if len(toolResults) > 0 {
			messages = append(messages, anthropic.NewBetaUserMessage(toolResults...))
		}
	}

	sendEvent(SSEEvent{Event: "error", Data: map[string]string{"message": "Too many tool calling iterations"}})
}

func consumeAnthropicStreamingResponse(
	stream interface {
		Next() bool
		Current() anthropic.BetaRawMessageStreamEventUnion
		Err() error
		Close() error
	},
	sendEvent func(SSEEvent),
) (anthropic.BetaMessage, string, string, []streamedToolCall, error) {
	defer func() {
		if err := stream.Close(); err != nil {
			klog.Warningf("Failed to close AI stream: %v", err)
		}
	}()

	var message anthropic.BetaMessage
	var contentBuilder strings.Builder
	var thinkingBuilder strings.Builder

	for stream.Next() {
		event := stream.Current()
		if err := message.Accumulate(event); err != nil {
			return anthropic.BetaMessage{}, "", "", nil, err
		}

		if startEvent, ok := event.AsAny().(anthropic.BetaRawContentBlockStartEvent); ok {
			if thinkingBlock, ok := startEvent.ContentBlock.AsAny().(anthropic.BetaThinkingBlock); ok && thinkingBlock.Thinking != "" {
				thinkingBuilder.WriteString(thinkingBlock.Thinking)
				sendEvent(SSEEvent{Event: "think", Data: map[string]string{"content": thinkingBlock.Thinking}})
			}
		}

		if deltaEvent, ok := event.AsAny().(anthropic.BetaRawContentBlockDeltaEvent); ok {
			if textDelta, ok := deltaEvent.Delta.AsAny().(anthropic.BetaTextDelta); ok && textDelta.Text != "" {
				contentBuilder.WriteString(textDelta.Text)
				sendEvent(SSEEvent{Event: "message", Data: map[string]string{"content": textDelta.Text}})
			}
			if thinkingDelta, ok := deltaEvent.Delta.AsAny().(anthropic.BetaThinkingDelta); ok && thinkingDelta.Thinking != "" {
				thinkingBuilder.WriteString(thinkingDelta.Thinking)
				sendEvent(SSEEvent{Event: "think", Data: map[string]string{"content": thinkingDelta.Thinking}})
			}
		}
	}

	if err := stream.Err(); err != nil {
		return anthropic.BetaMessage{}, "", "", nil, err
	}

	toolCalls := anthropicToolCallsToStreamedToolCalls(message)
	content := contentBuilder.String()
	if content == "" {
		content = anthropicMessageText(message)
	}
	thinking := thinkingBuilder.String()
	if thinking == "" {
		thinking = anthropicMessageThinking(message)
	}

	return message, content, thinking, toolCalls, nil
}

func anthropicToolCallsToStreamedToolCalls(message anthropic.BetaMessage) []streamedToolCall {
	toolCalls := make([]streamedToolCall, 0)
	for idx, block := range message.Content {
		toolUse, ok := block.AsAny().(anthropic.BetaToolUseBlock)
		if !ok {
			continue
		}
		toolCalls = append(toolCalls, streamedToolCall{
			Index:     int64(idx),
			ID:        toolUse.ID,
			Name:      toolUse.Name,
			Arguments: toolArgsJSON(toolUse.Input),
		})
	}
	return toolCalls
}

func anthropicMessageText(message anthropic.BetaMessage) string {
	var contentBuilder strings.Builder
	for _, block := range message.Content {
		textBlock, ok := block.AsAny().(anthropic.BetaTextBlock)
		if !ok || textBlock.Text == "" {
			continue
		}
		contentBuilder.WriteString(textBlock.Text)
	}
	return contentBuilder.String()
}

func anthropicMessageThinking(message anthropic.BetaMessage) string {
	var thinkingBuilder strings.Builder
	for _, block := range message.Content {
		thinkingBlock, ok := block.AsAny().(anthropic.BetaThinkingBlock)
		if !ok || thinkingBlock.Thinking == "" {
			continue
		}
		thinkingBuilder.WriteString(thinkingBlock.Thinking)
	}
	return thinkingBuilder.String()
}
