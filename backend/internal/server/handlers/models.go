package handlers

import (
	"github.com/gin-gonic/gin"

	"ai-chat/internal/httpresponse"
	"ai-chat/internal/modules/themebuild"
)

type ModelsHandler struct {
	builder *themebuild.Service
}

func NewModelsHandler(builder *themebuild.Service) *ModelsHandler {
	return &ModelsHandler{builder: builder}
}

// List returns the models a merchant may choose: ids, labels, image support and efforts, never provider details.
func (h *ModelsHandler) List(c *gin.Context) {
	httpresponse.OK(c, h.builder.ModelChoices())
}
