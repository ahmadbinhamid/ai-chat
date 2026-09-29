package ai

import "github.com/anthropics/anthropic-sdk-go"

// Conversation is an opaque snapshot of a Generate call's messages, ending on the assistant turn whose
// propose_changes was accepted; set ThemeContext.Continue to resume it instead of rebuilding from []Turn.
type Conversation struct {
	messages []anthropic.MessageParam
	// Every tool_use in the final assistant turn, in order; each needs a tool_result or the resume is a protocol error.
	finalToolUses []finalToolUse
	acceptedID    string
	// The resume must use the model that produced the history (a vision history can't go to a text-only model).
	model         anthropic.Model
	proposalRecap string
}

type finalToolUse struct {
	id, name string
}

const (
	supersededProposalResult = "Superseded: a later propose_changes call in this same turn was the one evaluated."
	notRunToolResult         = "Not run: your propose_changes in this same turn was accepted first."
)

// newConversation snapshots messages plus final, the turn holding the accepted propose_changes (acceptedID).
func newConversation(messages []anthropic.MessageParam, final anthropic.Message, acceptedID string, model anthropic.Model) *Conversation {
	snapshot := make([]anthropic.MessageParam, 0, len(messages)+1)
	snapshot = append(snapshot, messages...)
	snapshot = append(snapshot, final.ToParam())
	var uses []finalToolUse
	for _, block := range final.Content {
		if block.Type == "tool_use" {
			uses = append(uses, finalToolUse{id: block.ID, name: block.Name})
		}
	}
	return &Conversation{messages: snapshot, finalToolUses: uses, acceptedID: acceptedID, model: model}
}

// WithProposalRecap returns a copy whose accepted propose_changes tool_result carries recap: the proposal's
// current content, which auto-fixers may have changed since the model's own tool_use input. Nil-safe.
func (c *Conversation) WithProposalRecap(recap string) *Conversation {
	if c == nil {
		return nil
	}
	cp := *c
	cp.proposalRecap = recap
	return &cp
}

// resumeMessages pairs every final tool_use with a tool_result, then appends prompt in the same user turn.
func (c *Conversation) resumeMessages(prompt string) []anthropic.MessageParam {
	blocks := make([]anthropic.ContentBlockParamUnion, 0, len(c.finalToolUses)+1)
	for _, tu := range c.finalToolUses {
		switch {
		case tu.id == c.acceptedID:
			recap := c.proposalRecap
			if recap == "" {
				recap = "Proposal received."
			}
			blocks = append(blocks, toolResultBlock(tu.id, recap, false))
		case tu.name == toolNameProposeChanges:
			blocks = append(blocks, toolResultBlock(tu.id, supersededProposalResult, false))
		default:
			blocks = append(blocks, toolResultBlock(tu.id, notRunToolResult, false))
		}
	}
	blocks = append(blocks, anthropic.NewTextBlock(prompt))

	messages := make([]anthropic.MessageParam, 0, len(c.messages)+1)
	messages = append(messages, c.messages...)
	return append(messages, anthropic.NewUserMessage(blocks...))
}
