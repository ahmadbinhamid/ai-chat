package handlers

import (
	"github.com/gin-gonic/gin"

	"ai-chat/internal/auth"
	"ai-chat/internal/modules/themebuild"
)

// ResumeAwaitingGenerations hands a returning user's fresh token to the turns a restart left waiting for them. The
// dashboard polls constantly, so their next request resumes those turns; a no-op once nothing is waiting.
func ResumeAwaitingGenerations(builder *themebuild.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		builder.ResumeForUser(c.Request.Context(), auth.TenantID(c), auth.UserID(c), auth.Token(c))
		c.Next()
	}
}
