package server

import (
	"errors"
	"net/http"

	"github.com/Cyvadra/hephaestus/internal/auth"
	"github.com/gin-gonic/gin"
)

type loginRequest struct {
	Username   string `json:"username" binding:"required"`
	Timestamp  int64  `json:"timestamp" binding:"required"`
	Salt       string `json:"salt" binding:"required"`
	Digest     string `json:"digest" binding:"required"`
	ProofNonce string `json:"proof_nonce"`
}

// login authenticates a browser login proof.
//
// @Summary Login
// @Description Exchanges SHA-256(password + timestamp + salt) for a JWT session. This endpoint is public.
// @Tags auth
// @Accept json
// @Produce json
// @Param request body loginRequest true "Login proof"
// @Success 200 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Router /auth/login [post]
func (s *Server) login(c *gin.Context) {
	var request loginRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}
	token, proof, err := s.auth.LoginWithProof(request.Username, request.Timestamp, request.Salt, request.Digest, request.ProofNonce)
	if errors.Is(err, auth.ErrProofRequired) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "proof_of_work_required", "proof_of_work": proof})
		return
	}
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}
	s.auth.SetCookie(c.Writer, token, c.Request.TLS != nil)
	c.JSON(http.StatusOK, gin.H{"username": request.Username})
}

// authSession returns the authenticated user.
//
// @Summary Current session
// @Tags auth
// @Produce json
// @Success 200 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Security BearerAuth
// @Router /auth/session [get]
func (s *Server) authSession(c *gin.Context) {
	claims := c.MustGet("auth.claims").(*auth.Claims)
	c.JSON(http.StatusOK, gin.H{"username": claims.Username})
}

// logout clears the browser session cookie.
//
// @Summary Logout
// @Tags auth
// @Success 204
// @Failure 401 {object} map[string]string
// @Security BearerAuth
// @Router /auth/logout [post]
func (s *Server) logout(c *gin.Context) {
	s.auth.ClearCookie(c.Writer, c.Request.TLS != nil)
	c.Status(http.StatusNoContent)
}

func (s *Server) requireAuthentication(c *gin.Context) {
	claims, err := s.auth.Authenticate(c.Request)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		c.Abort()
		return
	}
	if token, refreshed, err := s.auth.RefreshIfNeeded(claims); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		c.Abort()
		return
	} else if refreshed {
		s.auth.SetCookie(c.Writer, token, c.Request.TLS != nil)
	} else if token, err := s.auth.Token(c.Request); err == nil {
		c.Header(auth.TokenHeader, token)
	}
	c.Set("auth.claims", claims)
	c.Next()
}
