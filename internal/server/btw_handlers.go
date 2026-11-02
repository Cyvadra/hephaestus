package server

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// sideQuestionRequest asks for an ephemeral answer from the session's
// committed context. It is not a chat turn: nothing is persisted.
type sideQuestionRequest struct {
	Question            string `json:"question" binding:"required"`
	ActiveLeafMessageID *uint  `json:"active_leaf_message_id"`
	SelectRoot          bool   `json:"select_root"`
}

// sideQuestion godoc
//
//	@Summary		Ask an ephemeral side question
//	@Description	Streams an answer based on the session's committed context up to the last completed assistant message. The question and answer are never persisted, and no tools are available.
//	@Tags		sessions
//	@Accept		json
//	@Produce		text/event-stream
//	@Param		id		path	uint				true	"Session ID"
//	@Param		request	body	sideQuestionRequest	true	"Side question payload"
//	@Success		200	{string}	string
//	@Failure		400	{object}	errorResponse
//	@Failure		404	{object}	errorResponse
//	@Router		/sessions/{id}/btw [post]
func (s *Server) sideQuestion(c *gin.Context) {
	sessionID, err := parseSessionID(c)
	if err != nil {
		badRequest(c, err)
		return
	}

	var req sideQuestionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, err)
		return
	}
	question := strings.TrimSpace(req.Question)
	if question == "" {
		badRequestf(c, "question is required")
		return
	}
	if err := validateBranchSelection(sendMessageRequest{
		SelectRoot:          req.SelectRoot,
		ActiveLeafMessageID: req.ActiveLeafMessageID,
	}); err != nil {
		badRequest(c, err)
		return
	}
	if _, err := s.sessions.Get(sessionID); err != nil {
		notFound(c, "session not found")
		return
	}

	emit := beginSSE(c)
	_, err = s.pipeline.SideQuestion(
		c.Request.Context(),
		sessionID,
		question,
		req.ActiveLeafMessageID,
		req.SelectRoot,
		func(delta string) {
			emit("delta", gin.H{"text": delta})
		},
	)
	if err != nil {
		// A disconnected client cannot receive the event; do not try to write.
		if c.Request.Context().Err() == nil {
			emit("error", err.Error())
		}
		return
	}
	emit("done", gin.H{"status": "succeeded"})
}
