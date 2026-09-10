package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"nofx/auth"

	"github.com/gin-gonic/gin"
)

func TestOptionalAuthenticatedUserID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	newCtx := func(header string) *gin.Context {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/equity-history?trader_id=t1", nil)
		if header != "" {
			c.Request.Header.Set("Authorization", header)
		}
		return c
	}

	if got := optionalAuthenticatedUserID(newCtx("")); got != "" {
		t.Fatalf("anonymous request must not resolve a user, got %q", got)
	}
	if got := optionalAuthenticatedUserID(newCtx("Token abc")); got != "" {
		t.Fatalf("non-Bearer authorization must not resolve a user, got %q", got)
	}
	if got := optionalAuthenticatedUserID(newCtx("Bearer not-a-jwt")); got != "" {
		t.Fatalf("invalid token must not resolve a user, got %q", got)
	}

	token, err := auth.GenerateJWT("user-123", "owner@example.com")
	if err != nil {
		t.Fatalf("failed to generate test token: %v", err)
	}
	if got := optionalAuthenticatedUserID(newCtx("Bearer "+token)); got != "user-123" {
		t.Fatalf("valid token must resolve its user, got %q", got)
	}
}
