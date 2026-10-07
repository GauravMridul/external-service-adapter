package auth

import (
	"context"
	"errors"
	"esa/internal/app/constants"
	commoninit "esa/internal/app/init"
	"fmt"
	"net/http"
	"strings"

	"github.com/dmi-infotech/common-modules/go/infrastructure/logger"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	"github.com/lestrrat-go/jwx/jwk"
	"go.uber.org/zap"
)

// Manager handles JWT authentication
type Manager struct {
	log         *zap.SugaredLogger
	audience    string
	issuer      string
	jwksURL     string
	environment string
	keySet      jwk.Set
}

// NewManager creates a new auth manager
func NewManager(
	log *zap.SugaredLogger,
	audience string,
	issuer string,
	jwksURL string,
	environment string,
) (*Manager, error) {
	if audience == "" || issuer == "" || jwksURL == "" {
		return nil, errors.New("missing required authentication configuration")
	}

	// Fetch the JWK Set from the provided URL
	keySet, err := jwk.Fetch(context.Background(), jwksURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch JWK set: %w", err)
	}

	return &Manager{
		log:         log,
		audience:    audience,
		issuer:      issuer,
		jwksURL:     jwksURL,
		environment: environment,
		keySet:      keySet,
	}, nil
}

// AuthMiddleware returns a Gin middleware for JWT authentication
func (m *Manager) AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Skip auth in development environment if needed
		// Allow all requests in non-production environments
		//TO BE INTEGRATED WHEN AUTH MIDDLEWARE IS IMPLEMENTED
		c.Next()
		return

		// Get the Authorization header
		authHeader := c.GetHeader(constants.Authorization)
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authorization header is required"})
			return
		}

		// Check for Bearer token
		if !strings.HasPrefix(authHeader, constants.Bearer) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid authorization format"})
			return
		}

		// Extract the token
		tokenString := strings.TrimPrefix(authHeader, constants.Bearer)
		if tokenString == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Token is required"})
			return
		}

		// Parse the token without verifying the signature yet
		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			// Validate the algorithm
			if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}

			// Get the key ID from the token header
			kid, ok := token.Header["kid"].(string)
			if !ok {
				return nil, errors.New("token does not contain a key ID")
			}

			// Get the key from the JWK Set
			key, found := m.keySet.LookupKeyID(kid)
			if !found {
				// If the key is not found, try refreshing the key set
				refreshedSet, err := jwk.Fetch(context.Background(), m.jwksURL)
				if err != nil {
					return nil, fmt.Errorf("failed to refresh JWK set: %w", err)
				}
				m.keySet = refreshedSet

				// Try again with the refreshed set
				key, found = m.keySet.LookupKeyID(kid)
				if !found {
					return nil, errors.New("key ID not found in JWK Set")
				}
			}

			// Get the raw public key
			var rawKey interface{}
			if err := key.Raw(&rawKey); err != nil {
				return nil, fmt.Errorf("failed to get raw key: %w", err)
			}

			return rawKey, nil
		})

		if err != nil {
			m.log.Errorw("token validation error", "error", err.Error())
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			return
		}

		// Check if the token is valid
		if !token.Valid {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			return
		}

		// Verify claims like audience and issuer
		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token claims"})
			return
		}

		// Check audience
		if claims["aud"] != m.audience {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token audience"})
			return
		}

		// Check issuer
		if claims["iss"] != m.issuer {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token issuer"})
			return
		}

		// Store claims in the context for later use
		c.Set("claims", claims)
		c.Next()
	}
}

// AuthManager provides a global instance of the Manager
var AuthManager *Manager

// Init initializes the AuthManager instance
func Init() {
	log := commoninit.GetLogger()

	// Check if authentication is enabled via configuration
	enableAuth := commoninit.GetConfig().GetBool(constants.ENABLE_AUTH)

	if !enableAuth {
		log.Info("🔓 Authentication is DISABLED via configuration - creating bypass auth manager")

		// Convert contracts.Logger to *zap.SugaredLogger for the bypass manager
		var zapLogger *zap.SugaredLogger
		if zapLoggerImpl, ok := log.(*logger.ZapLogger); ok {
			zapLogger = zapLoggerImpl.GetZapLogger()
		} else {
			// Fallback - create a new zap logger
			zapLoggerFallback, _ := logger.NewZapLogger(logger.Config{
				Level:       logger.LogLevel("info"),
				Development: false,
				Encoding:    "json",
			})
			zapLogger = zapLoggerFallback.(*logger.ZapLogger).GetZapLogger()
		}

		env := commoninit.GetConfig().GetString(constants.Environment)

		// Create a bypass auth manager
		AuthManager = &Manager{
			log:         zapLogger,
			audience:    "bypass",
			issuer:      "bypass",
			jwksURL:     "bypass",
			environment: env,
			keySet:      nil, // Not used when auth is disabled
		}
		log.Infow("Auth manager initialized with authentication DISABLED - all requests will be allowed")
		return
	}

	log.Info("🔐 Authentication is ENABLED via configuration - initializing auth manager")

	// Convert contracts.Logger to *zap.SugaredLogger
	var zapLogger *zap.SugaredLogger
	if zapLoggerImpl, ok := log.(*logger.ZapLogger); ok {
		zapLogger = zapLoggerImpl.GetZapLogger()
	} else {
		// Fallback - create a new zap logger
		zapLoggerFallback, _ := logger.NewZapLogger(logger.Config{
			Level:       logger.LogLevel("info"),
			Development: false,
			Encoding:    "json",
		})
		zapLogger = zapLoggerFallback.(*logger.ZapLogger).GetZapLogger()
	}

	env := commoninit.GetConfig().GetString(constants.Environment)

	// Initialize auth manager with JWKS configuration
	var err error
	AuthManager, err = NewManager(
		zapLogger,
		commoninit.GetConfig().GetString(constants.JWKS_AUDIENCE),
		commoninit.GetConfig().GetString(constants.JWKS_ISSUER),
		commoninit.GetConfig().GetString(constants.JWKS_URL),
		env,
	)
	if err != nil {
		log.Errorw("error while setting up auth manager", "error", err.Error())
		log.Error("❌ Authentication is ENABLED but required JWKS configuration is missing!")
		log.Errorw("Required configuration keys",
			"JWKS_AUDIENCE", constants.JWKS_AUDIENCE,
			"JWKS_ISSUER", constants.JWKS_ISSUER,
			"JWKS_URL", constants.JWKS_URL)
		panic(err.Error())
	}

	log.Info("✅ Auth manager initialized successfully with JWKS authentication")
}

// APIKeyAuth middleware for API key authentication
func APIKeyAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		log := commoninit.GetLogger(c.Request.Context())

		apiKey := c.GetHeader(constants.ApiKey)
		expectedKey := commoninit.GetConfig().GetString(constants.ApiKey)

		if apiKey == "" {
			log.Warn("API key missing in request")
			c.JSON(http.StatusUnauthorized, gin.H{"error": "API key required"})
			c.Abort()
			return
		}

		if apiKey != expectedKey {
			log.Warnw("Invalid API key provided", "provided_key", apiKey)
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid API key"})
			c.Abort()
			return
		}

		log.Debug("API key authentication successful")
		c.Next()
	}
}

// TokenAuth middleware for token-based authentication
func TokenAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		log := commoninit.GetLogger(c.Request.Context())

		token := c.GetHeader("Authorization")
		if token == "" {
			log.Warn("Authorization token missing in request")
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization token required"})
			c.Abort()
			return
		}

		// Add token validation logic here
		log.Debug("Token authentication successful")
		c.Next()
	}
}
