package agents

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

func NewGenerationID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("generate generation ID: %w", err)
	}
	return hex.EncodeToString(id[:]), nil
}
