package identity

import (
	"crypto/rand"
	"encoding/json"
)

type operationArgumentEnvelope struct {
	KeyID      string `json:"key_id"`
	Ciphertext []byte `json:"ciphertext"`
}

func operationArgumentAAD(operationID, keyID string) []byte {
	return []byte("loom-external-operation-arguments-v1\x00" + operationID + "\x00" + keyID)
}

// SealOperationArguments uses the active storage key and binds ciphertext to the write operation.
func (c *SessionCipher) SealOperationArguments(operationID string, arguments json.RawMessage) (json.RawMessage, error) {
	if c == nil || operationID == "" || len(arguments) == 0 {
		return nil, ErrUnauthorized
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	sealed := c.aead.Seal(nonce, nonce, arguments, operationArgumentAAD(operationID, c.keyID))
	return json.Marshal(operationArgumentEnvelope{KeyID: c.keyID, Ciphertext: sealed})
}

// OpenOperationArguments authenticates the operation binding before returning input to the caller.
func (c *SessionCipher) OpenOperationArguments(operationID string, raw json.RawMessage) (json.RawMessage, error) {
	var env operationArgumentEnvelope
	if c == nil || operationID == "" || json.Unmarshal(raw, &env) != nil {
		return nil, ErrUnauthorized
	}
	aead, ok := c.keys[env.KeyID]
	if !ok || len(env.Ciphertext) < aead.NonceSize()+aead.Overhead() {
		return nil, ErrUnauthorized
	}
	nonce := env.Ciphertext[:aead.NonceSize()]
	plain, err := aead.Open(nil, nonce, env.Ciphertext[aead.NonceSize():], operationArgumentAAD(operationID, env.KeyID))
	if err != nil {
		return nil, ErrUnauthorized
	}
	return plain, nil
}
