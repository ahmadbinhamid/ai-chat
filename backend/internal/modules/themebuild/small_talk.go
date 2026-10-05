package themebuild

import (
	"strings"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/pageintent"
)

// greetingReply says what the builder can do, so a merchant who opened with "hi" gets a useful next step, not just a hello.
const greetingReply = "Hi! I can redesign pages, change colours and fonts, add sections, or fix something that isn't working. What would you like to do?"

// acknowledgementReply closes the loop on "thanks"/"ok" without pretending anything was changed.
const acknowledgementReply = "You're welcome! Let me know if there's anything else you'd like to change."

// smallTalkReply answers a bare greeting/acknowledgement without FlowPOS or the model. Anything sent with it, a guided
// mode, or an assistant question it may be answering ("ok" to "Want me to update the footer?") means a real request.
func smallTalkReply(in GenerateInput, priorMessages []chat.Message) (*ai.Result, bool) {
	attachedHTML := in.HTMLAttachmentContent != nil && !in.HTMLAttachmentCarriedForward
	guidedMode := in.Mode != "" && in.Mode != ai.GenerationModeEdit
	if len(in.Images) > 0 || attachedHTML || in.ReferenceURL != "" || len(in.PreviewErrors) > 0 || guidedMode ||
		lastAssistantAskedQuestion(priorMessages) {
		return nil, false
	}
	switch pageintent.DetectSmallTalk(in.Prompt) {
	case pageintent.SmallTalkGreeting:
		return &ai.Result{Summary: greetingReply, AnsweredQuestion: true}, true
	case pageintent.SmallTalkAcknowledgement:
		return &ai.Result{Summary: acknowledgementReply, AnsweredQuestion: true}, true
	default:
		return nil, false
	}
}

// lastAssistantAskedQuestion reports whether the latest replayed assistant reply ends with a question.
func lastAssistantAskedQuestion(messages []chat.Message) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role == chat.RoleAssistant && isReplayedMessage(m) {
			return strings.HasSuffix(strings.TrimSpace(m.Content), "?")
		}
	}
	return false
}
