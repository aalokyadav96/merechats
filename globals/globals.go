package globals

import (
	"context"
	"time"
)

const (
	RefreshTokenTTL = 7 * 24 * time.Hour // 7 days
	AccessTokenTTL  = 15 * time.Minute   // 15 minutes
)

var (
	// tokenSigningAlgo = jwt.SigningMethodHS256
	JwtSecret = []byte("your_secret_key") // Replace with a secure secret key
)

// Context keys
type ContextKey string

const RoleKey ContextKey = "role"
const UserIDKey ContextKey = "userId"

var Ctx = context.Background()
