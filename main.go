package main

import (
	"fmt"
	"log"
	"math/big"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	x402gin "x402-server/middleware"
	"x402-server/types"
)

// Session represents a payment session
type Session struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	Type      string    `json:"type"` // "24hour" or "onetime"
	Used      *bool     `json:"used,omitempty"`
}

// SessionStore manages sessions in memory (use Redis/DB in production)
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

func NewSessionStore() *SessionStore {
	return &SessionStore{
		sessions: make(map[string]*Session),
	}
}

func (s *SessionStore) Set(id string, session *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[id] = session
}

func (s *SessionStore) Get(id string) (*Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, exists := s.sessions[id]
	return session, exists
}

func (s *SessionStore) GetAllActive() []*Session {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now()
	var active []*Session

	for _, session := range s.sessions {
		isExpired := now.After(session.ExpiresAt)
		isUsed := session.Type == "onetime" && session.Used != nil && *session.Used
		if !isExpired && !isUsed {
			active = append(active, session)
		}
	}

	return active
}

// Config holds application configuration
type Config struct {
	FacilitatorURL string
	PayTo          string
	Network        string
	Port           int
	NodeEnv        string
	CORSOrigins    []string
}

func loadConfig() *Config {
	godotenv.Load()

	config := &Config{
		FacilitatorURL: getEnv("FACILITATOR_URL", "https://x402.org/facilitator"),
		PayTo:          getEnv("ADDRESS", "0x31422e245ecf4c87fb425b3e4ab3530203ae375c"),
		Network:        getEnv("NETWORK", "base-sepolia"),
		Port:           getEnvAsInt("PORT", 3001),
		NodeEnv:        getEnv("NODE_ENV", "development"),
	}

	if config.PayTo == "" {
		log.Fatal("❌ Please set your wallet ADDRESS in the .env file")
	}
	return config
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvAsInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

// setupCORS configures CORS middleware for all responses including 402 Payment Required
func setupCORS(r *gin.Engine, config *Config) {
	r.Use(func(c *gin.Context) {
		origin := c.GetHeader("Origin")

		if origin != "" {
			c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
		}
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Expose-Headers", "X-402-Payment-Required, X-402-Amount, X-402-Pay-To, X-402-Resource, X-402-Network, X-Payment, x-payment")

		// Handle OPTIONS preflight request
		if c.Request.Method == "OPTIONS" {
			if origin != "" {
				c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
			}
			c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
			c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-402-Payment, X-Payment, x-payment, X-402-Payment-Required, access-control-expose-headers")
			c.Writer.Header().Set("Access-Control-Expose-Headers", "X-402-Payment-Required, X-402-Amount, X-402-Pay-To, X-402-Resource, X-402-Network, X-Payment, x-payment")
			c.Writer.Header().Set("Access-Control-Max-Age", "86400")

			c.AbortWithStatus(204)
			return
		}

		c.Next()
	})
}

func main() {
	config := loadConfig()
	sessionStore := NewSessionStore()

	r := gin.Default()

	// Setup CORS middleware (must be first to handle all responses)
	setupCORS(r, config)

	// Facilitator configuration
	facilitatorConfig := &types.FacilitatorConfig{
		URL: config.FacilitatorURL,
	}

	// Helper function to get resource URL for x402 payment middleware
	getResourceURL := func(path string) string {
		baseURL := getEnv("BASE_URL", fmt.Sprintf("http://localhost:%d", config.Port))
		return fmt.Sprintf("%s%s", baseURL, path)
	}

	// Free endpoint - health check
	r.GET("/api/health", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"status":  "ok",
			"message": "Server is running",
			"config": gin.H{
				"network":     config.Network,
				"payTo":       config.PayTo,
				"facilitator": config.FacilitatorURL,
			},
		})
	})

	// Free endpoint - get payment options
	r.GET("/api/payment-options", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"options": []gin.H{
				{
					"name":        "24-Hour Access",
					"endpoint":    "/api/pay/session",
					"price":       "$1.00",
					"description": "Get a session ID for 24 hours of unlimited access",
				},
				{
					"name":        "One-Time Access",
					"endpoint":    "/api/pay/onetime",
					"price":       "$0.10",
					"description": "Single use payment for immediate access",
				},
			},
		})
	})

	// Paid endpoint - 24-hour session access ($1.00)
	r.POST(
		"/api/pay/session",
		x402gin.PaymentMiddleware(
			big.NewFloat(1.00),
			config.PayTo,
			x402gin.WithFacilitatorConfig(facilitatorConfig),
			x402gin.WithResource(getResourceURL("/api/pay/session")),
		),
		func(c *gin.Context) {
			sessionID := uuid.New().String()
			now := time.Now()
			expiresAt := now.Add(24 * time.Hour)

			session := &Session{
				ID:        sessionID,
				CreatedAt: now,
				ExpiresAt: expiresAt,
				Type:      "24hour",
			}

			sessionStore.Set(sessionID, session)

			c.JSON(200, gin.H{
				"success":   true,
				"sessionId": sessionID,
				"message":   "24-hour access granted!",
				"session": gin.H{
					"id":        session.ID,
					"type":      session.Type,
					"createdAt": session.CreatedAt.Format(time.RFC3339),
					"expiresAt": session.ExpiresAt.Format(time.RFC3339),
					"validFor":  "24 hours",
				},
			})
		},
	)

	// Paid endpoint - one-time access/payment ($0.10)
	r.POST(
		"/api/pay/onetime",
		x402gin.PaymentMiddleware(
			big.NewFloat(0.10),
			config.PayTo,
			x402gin.WithFacilitatorConfig(facilitatorConfig),
			x402gin.WithResource(getResourceURL("/api/pay/onetime")),
		),
		func(c *gin.Context) {
			sessionID := uuid.New().String()
			now := time.Now()
			expiresAt := now.Add(5 * time.Minute)
			used := false

			session := &Session{
				ID:        sessionID,
				CreatedAt: now,
				ExpiresAt: expiresAt,
				Type:      "onetime",
				Used:      &used,
			}

			sessionStore.Set(sessionID, session)

			c.JSON(200, gin.H{
				"success":   true,
				"sessionId": sessionID,
				"message":   "One-time access granted!",
				"access": gin.H{
					"id":        session.ID,
					"type":      session.Type,
					"createdAt": session.CreatedAt.Format(time.RFC3339),
					"validFor":  "5 minutes (single use)",
				},
			})
		},
	)

	// Free endpoint - validate session
	r.GET("/api/session/:sessionId", func(c *gin.Context) {
		sessionID := c.Param("sessionId")
		session, exists := sessionStore.Get(sessionID)

		if !exists {
			c.JSON(404, gin.H{
				"valid": false,
				"error": "Session not found",
			})
			return
		}

		now := time.Now()
		isExpired := now.After(session.ExpiresAt)
		isUsed := session.Type == "onetime" && session.Used != nil && *session.Used

		if isExpired || isUsed {
			errorMsg := "Session expired"
			if isUsed {
				errorMsg = "One-time access already used"
			}

			usedValue := false
			if session.Used != nil {
				usedValue = *session.Used
			}

			c.JSON(200, gin.H{
				"valid": false,
				"error": errorMsg,
				"session": gin.H{
					"id":        session.ID,
					"type":      session.Type,
					"createdAt": session.CreatedAt.Format(time.RFC3339),
					"expiresAt": session.ExpiresAt.Format(time.RFC3339),
					"used":      usedValue,
				},
			})
			return
		}

		// Mark one-time sessions as used
		if session.Type == "onetime" && session.Used != nil {
			*session.Used = true
			sessionStore.Set(sessionID, session)
		}

		remainingTime := session.ExpiresAt.Sub(now).Milliseconds()

		c.JSON(200, gin.H{
			"valid": true,
			"session": gin.H{
				"id":            session.ID,
				"type":          session.Type,
				"createdAt":     session.CreatedAt.Format(time.RFC3339),
				"expiresAt":     session.ExpiresAt.Format(time.RFC3339),
				"remainingTime": remainingTime,
			},
		})
	})

	// Free endpoint - list active sessions (for demo purposes)
	r.GET("/api/sessions", func(c *gin.Context) {
		activeSessions := sessionStore.GetAllActive()

		sessions := make([]gin.H, 0, len(activeSessions))
		for _, session := range activeSessions {
			sessions = append(sessions, gin.H{
				"id":        session.ID,
				"type":      session.Type,
				"createdAt": session.CreatedAt.Format(time.RFC3339),
				"expiresAt": session.ExpiresAt.Format(time.RFC3339),
			})
		}

		c.JSON(200, gin.H{
			"sessions": sessions,
		})
	})

	// Print startup message
	fmt.Println(`
🚀 x402 Payment Template Server
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
💰 Accepting payments to:`, config.PayTo, `
🔗 Network:`, config.Network, `
🌐 Port:`, config.Port, `
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
📋 Payment Options:
   - 24-Hour Session: $1.00
   - One-Time Access: $0.10
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
🛠️  This is a template! Customize it for your app.
📚 Learn more: https://x402.org
💬 Get help: https://discord.gg/invite/cdp
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
`)

	// Start server
	addr := fmt.Sprintf(":%d", config.Port)
	if err := r.Run(addr); err != nil {
		log.Fatal("Failed to start server:", err)
	}
}
