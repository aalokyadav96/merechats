package middleware

import (
	"context"
	"fmt"
	"net/http"

	"naevis/globals" // adjust this import to your actual path

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/julienschmidt/httprouter"
)

// Claims defines the JWT claims structure
type Claims struct {
	Username string   `json:"username"`
	UserID   string   `json:"userId"`
	Role     []string `json:"role"`
	jwt.RegisteredClaims
}

// Authenticate middleware verifies the JWT and stores UserID and Role in context
func Authenticate(next httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		if websocket.IsWebSocketUpgrade(r) {
			next(w, r, ps)
			return
		}

		tokenString := r.Header.Get("Authorization")
		if tokenString == "" || len(tokenString) < 8 || tokenString[:7] != "Bearer " {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		claims := &Claims{}
		token, err := jwt.ParseWithClaims(tokenString[7:], claims, func(token *jwt.Token) (any, error) {
			return globals.JwtSecret, nil
		})
		if err != nil || !token.Valid {
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return
		}

		ctx := r.Context()
		ctx = context.WithValue(ctx, globals.UserIDKey, claims.UserID)
		ctx = context.WithValue(ctx, globals.RoleKey, claims.Role)

		next(w, r.WithContext(ctx), ps)
	}
}

// OptionalAuth lets the request through even if JWT is invalid or missing
func OptionalAuth(next httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		tokenString := r.Header.Get("Authorization")
		if len(tokenString) >= 8 && tokenString[:7] == "Bearer " {
			claims := &Claims{}
			token, err := jwt.ParseWithClaims(tokenString[7:], claims, func(token *jwt.Token) (any, error) {
				return globals.JwtSecret, nil
			})
			if err == nil && token.Valid {
				ctx := r.Context()
				ctx = context.WithValue(ctx, globals.UserIDKey, claims.UserID)
				ctx = context.WithValue(ctx, globals.RoleKey, claims.Role)
				r = r.WithContext(ctx)
			}
		}
		next(w, r, ps)
	}
}

// RequireRoles restricts access to users with matching roles
func RequireRoles(allowedRoles ...string) func(httprouter.Handle) httprouter.Handle {
	return func(next httprouter.Handle) httprouter.Handle {
		return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
			rawRoles := r.Context().Value(globals.RoleKey)
			roles, ok := rawRoles.([]string)
			if !ok || roles == nil {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}

			for _, role := range roles {
				for _, allowed := range allowedRoles {
					if role == allowed {
						next(w, r, ps)
						return
					}
				}
			}

			http.Error(w, "Forbidden", http.StatusForbidden)
		}
	}
}

// ValidateJWT parses and returns claims from a token string
func ValidateJWT(tokenString string) (*Claims, error) {
	if tokenString == "" || len(tokenString) < 8 {
		return nil, fmt.Errorf("invalid token")
	}

	claims := &Claims{}
	_, err := jwt.ParseWithClaims(tokenString[7:], claims, func(token *jwt.Token) (any, error) {
		return globals.JwtSecret, nil
	})
	if err != nil {
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	return claims, nil
}
