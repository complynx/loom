package config

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"strings"
	"unicode"
)

const applicationKeyLimit = 64 << 10

type applicationKey struct {
	Type     string `json:"type"`
	KeyID    string `json:"keyId"`
	Key      string `json:"key"`
	ClientID string `json:"clientId"`
}

// ResolveCredentials accepts a Zitadel application JSON key from one operator source.
// Base64 input stays in memory and is never written to the data directory.
func (i Identity) ResolveCredentials() (Identity, error) {
	if i.ClientPrivateKey != nil {
		if i.ClientSecret != "" || i.ClientKeyBase64 != "" {
			return Identity{}, errors.New("select one introspection credential source")
		}
		return i, nil
	}
	if i.ClientKeyBase64 == "" && i.ClientKeyFile == "" {
		return i, nil
	}
	if i.ClientSecret != "" || (i.ClientKeyBase64 != "" && i.ClientKeyFile != "") {
		return Identity{}, errors.New("select one introspection credential source")
	}
	data, err := i.applicationKeyBytes()
	if err != nil {
		return Identity{}, errors.New("zitadel application key unavailable or invalid base64")
	}
	defer clear(data)
	var key applicationKey
	if len(data) > applicationKeyLimit || json.Unmarshal(data, &key) != nil ||
		key.Type != "application" || key.KeyID == "" || key.ClientID == "" ||
		(i.ClientID != "" && i.ClientID != key.ClientID) {
		return Identity{}, errors.New("invalid or mismatched Zitadel application key")
	}
	block, rest := pem.Decode([]byte(key.Key))
	if block == nil || len(rest) != 0 {
		return Identity{}, errors.New("invalid Zitadel application private key")
	}
	defer clear(block.Bytes)
	private, err := parseApplicationRSA(block)
	if err != nil || private.Validate() != nil || private.N.BitLen() < 2048 {
		return Identity{}, errors.New("zitadel application key requires a valid RSA key of at least 2048 bits")
	}
	i.ClientID, i.ClientKeyID, i.ClientPrivateKey = key.ClientID, key.KeyID, private
	i.ClientKeyBase64 = ""
	return i, nil
}

func (i Identity) applicationKeyBytes() ([]byte, error) {
	if i.ClientKeyBase64 == "" {
		return readApplicationKey(i.ClientKeyFile)
	}
	if len(i.ClientKeyBase64) > base64.StdEncoding.EncodedLen(applicationKeyLimit) {
		return nil, errors.New("zitadel application key exceeds size limit")
	}
	encoded := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, i.ClientKeyBase64)
	return base64.StdEncoding.DecodeString(encoded)
}

func readApplicationKey(path string) ([]byte, error) {
	// #nosec G304 -- Credential path is an operator setting, never a request parameter.
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > applicationKeyLimit {
		return nil, errors.New("invalid credential file")
	}
	data := make([]byte, info.Size())
	_, err = file.ReadAt(data, 0)
	return data, err
}

func parseApplicationRSA(block *pem.Block) (*rsa.PrivateKey, error) {
	if block.Type == "RSA PRIVATE KEY" {
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	private, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("RSA key required")
	}
	return private, nil
}
