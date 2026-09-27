package wagering

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const payloadHashPrefix = "sha256:"

// CanonicalPayload holds the business fields that identify an operation,
// already in normalized form: lowercase canonical UUIDs and two-decimal amounts.
// Idempotency keys, message ids and transport metadata are deliberately absent.
type CanonicalPayload struct {
	ProviderID                     string
	ExternalTransactionID          string
	PlayerID                       string
	WalletID                       string
	RoundID                        string
	GameID                         string
	Kind                           string
	Amount                         string
	Currency                       string
	ReferenceExternalTransactionID string
}

// PayloadHash is SHA-256 over canonical JSON: keys sorted, no insignificant
// whitespace, HTML characters unescaped, and absent optional fields omitted.
func PayloadHash(p CanonicalPayload) string {
	doc := map[string]any{
		"providerId":            p.ProviderID,
		"externalTransactionId": p.ExternalTransactionID,
		"playerId":              p.PlayerID,
		"walletId":              p.WalletID,
		"roundId":               p.RoundID,
		"gameId":                p.GameID,
		"kind":                  p.Kind,
		"money": map[string]string{
			"amount":   p.Amount,
			"currency": p.Currency,
		},
	}
	if p.ReferenceExternalTransactionID != "" {
		doc["referenceExternalTransactionId"] = p.ReferenceExternalTransactionID
	}
	sum := sha256.Sum256(canonicalJSON(doc))
	return payloadHashPrefix + hex.EncodeToString(sum[:])
}

// canonicalJSON relies on encoding/json sorting map keys. Every value is a
// string or a map of strings, so encoding cannot fail.
func canonicalJSON(doc map[string]any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(doc)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}
