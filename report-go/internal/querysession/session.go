package querysession

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

const Header = "X-NL2SQL-Session"

type contextKey struct{}

func NewID() string {
	var bytes [16]byte
	rand.Read(bytes[:])
	return hex.EncodeToString(bytes[:])
}

func ValidID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func WithID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

func ID(ctx context.Context) string {
	id, _ := ctx.Value(contextKey{}).(string)
	return id
}
