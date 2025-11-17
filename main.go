package main

import (
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"

	x402gin "x402-server/middleware"
	"x402-server/types"
)

// 支付中间件
func paymentMiddleware(payTo, priceStr, network string) gin.HandlerFunc {
	return func(c *gin.Context) {
		walletAddress := c.GetHeader("X-Wallet-Address")
		if walletAddress == "" {
			c.Header("X-402-Payment-Required", "true")
			c.Header("X-402-Amount", priceStr)
			c.Header("X-402-Pay-To", payTo)
			c.Header("X-402-Network", network)
			c.AbortWithStatusJSON(http.StatusPaymentRequired, gin.H{
				"error":           "Payment Required",
				"price":           priceStr,
				"paymentEndpoint": "/api/pay/image",
			})
			return
		}
		c.Set("walletAddress", strings.ToLower(walletAddress))
		c.Next()
	}
}

// 解析价格
func parsePrice(priceEnv string) (*big.Float, string) {
	cleanPrice := strings.TrimPrefix(priceEnv, "$")
	price, ok := new(big.Float).SetString(cleanPrice)
	if !ok {
		return big.NewFloat(0.1), "0.1"
	}
	return price, cleanPrice
}

// 生成合法 resource URL
func getResourceURL(baseURL, path string) string {
	baseURL = strings.TrimSuffix(baseURL, "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return baseURL + path
}

// 开发环境专用，关闭所有跨域限制
func devCorsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Headers", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, OPTIONS, PUT, DELETE")
		c.Header("Access-Control-Expose-Headers", "*")
		c.Header("Access-Control-Allow-Credentials", "false")
		c.Header("Access-Control-Max-Age", "86400")

		// 直接处理 OPTIONS 预检
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		// 强制后置补全
		defer func() {
			c.Header("Access-Control-Allow-Origin", "*")
			c.Header("Access-Control-Allow-Headers", "*")
			c.Header("Access-Control-Allow-Methods", "GET, POST, OPTIONS, PUT, DELETE")
			c.Header("Access-Control-Expose-Headers", "*")
			c.Header("Access-Control-Allow-Credentials", "false")
		}()

		c.Next()
	}
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

func main() {
	godotenv.Load()

	// 配置初始化
	payTo := getEnv("ADDRESS", "")
	if payTo == "" {
		panic("❌ Please set your wallet ADDRESS in the .env file")
	}

	network := getEnv("NETWORK", "base-sepolia")
	port := getEnvAsInt("PORT", 3001)
	imageUrl := getEnv("IMAGE_URL", "https://x402.taolimarket.com/images/pretty-girl.jpeg")
	baseURL := getEnv("BASE_URL", "https://x402.taolimarket.com")
	facilitatorURL := getEnv("FACILITATOR_URL", "https://x402.org/facilitator")
	imagePriceEnv := getEnv("IMAGE_PRICE", "$0.1")
	imagePrice, cleanPrice := parsePrice(imagePriceEnv)
	nodeEnv := getEnv("NODE_ENV", "production")

	// Gin 初始化
	app := gin.Default()
	if nodeEnv == "development" {
		app.Use(devCorsMiddleware()) // 开发环境跨域全放行
	}

	// 支付状态存储
	type UserAccess struct {
		StartTime time.Time
	}
	var (
		paidUsers = make(map[string]UserAccess)
		mu        sync.RWMutex
	)
	const ViewDuration = 30 * time.Second

	// 1. 免费接口：支付信息
	app.GET("/api/payment-info", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"price":       "$" + cleanPrice,
			"description": "支付后解锁图片，获得30秒的访问权限",
			"endpoint":    "/api/pay/image",
			"network":     network,
			"resource":    getResourceURL(baseURL, "/api/pay/image"),
		})
	})

	// 2. 付费接口: 实际支付
	app.POST("/api/pay/image",
		x402gin.PaymentMiddleware(
			imagePrice,
			payTo,
			x402gin.WithFacilitatorConfig(&types.FacilitatorConfig{URL: facilitatorURL}),
			x402gin.WithResource(getResourceURL(baseURL, "/api/pay/image")),
		),
		func(c *gin.Context) {
			walletAddress := c.GetHeader("X-Wallet-Address")
			if walletAddress == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "X-Wallet-Address header is required"})
				return
			}

			normalizedAddress := strings.ToLower(walletAddress)
			mu.Lock()
			paidUsers[normalizedAddress] = UserAccess{StartTime: time.Now()}
			mu.Unlock()

			c.JSON(http.StatusOK, gin.H{
				"success":   true,
				"message":   "支付成功！30秒内可访问图片",
				"imageUrl":  imageUrl,
				"startTime": time.Now().Format(time.RFC3339),
				"duration":  30,
			})
		},
	)

	// 3. 受保护接口：图片访问
	app.GET("/api/image", paymentMiddleware(payTo, cleanPrice, network), func(c *gin.Context) {
		walletAddress := c.GetHeader("X-Wallet-Address")
		log.Println("walletAddresss:", walletAddress)
		if walletAddress == "" {
			c.JSON(http.StatusForbidden, gin.H{
				"error":           "需要支付才能访问",
				"paid":            false,
				"paymentEndpoint": "/api/pay/image",
				"price":           "$" + cleanPrice,
			})
			return
		}

		mu.RLock()
		userAccess, userFound := paidUsers[walletAddress]
		mu.RUnlock()
		log.Println("userFound:", userFound)
		if !userFound {
			c.JSON(http.StatusForbidden, gin.H{
				"error":           "需要支付才能访问",
				"paid":            false,
				"paymentEndpoint": "/api/pay/image",
				"price":           "$" + cleanPrice,
			})
			return
		}

		now := time.Now()
		elapsed := now.Sub(userAccess.StartTime)
		log.Println("elapsed:", elapsed)
		if elapsed >= ViewDuration {
			log.Println("------:", elapsed-ViewDuration)
			mu.Lock()
			delete(paidUsers, walletAddress)
			mu.Unlock()

			c.JSON(http.StatusForbidden, gin.H{
				"error":           "访问已过期，请重新支付",
				"paid":            false,
				"expired":         true,
				"paymentEndpoint": "/api/pay/image",
				"price":           "$" + cleanPrice,
			})
			return
		}

		remaining := ViewDuration - elapsed
		log.Println("remaining:", remaining)

		c.JSON(http.StatusOK, gin.H{
			"success":          true,
			"paid":             true,
			"imageUrl":         imageUrl,
			"startTime":        userAccess.StartTime.Format(time.RFC3339),
			"remainingSeconds": int(remaining.Seconds()),
			"totalDuration":    30,
		})
	})

	// 启动服务器
	fmt.Printf(`
🖼️  x402 Image Payment Server (开发环境无限制版)
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
💰 收款地址: %s
🔗 网络: %s
🌐 端口: %d
💵 价格: $%s
⚠️  开发环境专用：已关闭所有跨域限制
✅ 支持所有源、所有头、所有方法
✅ 402/200 响应均带完整 CORS 头
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
`, payTo, network, port, cleanPrice)

	if err := app.Run(":" + strconv.Itoa(port)); err != nil {
		panic(fmt.Sprintf("❌ 服务器启动失败: %v", err))
	}
}
