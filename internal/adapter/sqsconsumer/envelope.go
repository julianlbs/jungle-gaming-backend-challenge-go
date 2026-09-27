// Package sqsconsumer turns wager messages from SQS into use case calls.
package sqsconsumer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/app"
	"github.com/julianlbs/jungle-gaming-backend-challenge-go/internal/domain/wagering"
)

const MessageTypeWagerRequested = "WagerTransactionRequested"

var errInvalidMessage = errors.New("invalid message")

type envelope struct {
	MessageID     string     `json:"messageId"`
	Type          string     `json:"type"`
	OccurredAt    string     `json:"occurredAt"`
	CorrelationID string     `json:"correlationId"`
	CausationID   string     `json:"causationId"`
	Data          *wagerData `json:"data"`
}

type moneyData struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type wagerData struct {
	ProviderID                     string     `json:"providerId"`
	ExternalTransactionID          string     `json:"externalTransactionId"`
	IdempotencyKey                 string     `json:"idempotencyKey"`
	PlayerID                       string     `json:"playerId"`
	WalletID                       string     `json:"walletId"`
	RoundID                        string     `json:"roundId"`
	GameID                         string     `json:"gameId"`
	Kind                           string     `json:"kind"`
	Money                          *moneyData `json:"money"`
	ReferenceExternalTransactionID string     `json:"referenceExternalTransactionId"`
}

// decode validates the envelope; business fields are validated by the use case.
func decode(body string) (envelope, app.WagerCommand, error) {
	var env envelope
	dec := json.NewDecoder(strings.NewReader(body))
	if err := dec.Decode(&env); err != nil {
		return env, app.WagerCommand{}, fmt.Errorf("%w: %v", errInvalidMessage, err)
	}
	if dec.More() {
		return env, app.WagerCommand{}, fmt.Errorf("%w: trailing data", errInvalidMessage)
	}
	switch {
	case strings.TrimSpace(env.MessageID) == "":
		return env, app.WagerCommand{}, fmt.Errorf("%w: messageId is required", errInvalidMessage)
	case len(env.MessageID) > 200:
		return env, app.WagerCommand{}, fmt.Errorf("%w: messageId is too long", errInvalidMessage)
	case env.Type != MessageTypeWagerRequested:
		return env, app.WagerCommand{}, fmt.Errorf("%w: unsupported type %q", errInvalidMessage, env.Type)
	case env.Data == nil:
		return env, app.WagerCommand{}, fmt.Errorf("%w: data is required", errInvalidMessage)
	case env.Data.Money == nil:
		return env, app.WagerCommand{}, fmt.Errorf("%w: data.money is required", errInvalidMessage)
	case strings.TrimSpace(env.Data.IdempotencyKey) == "":
		return env, app.WagerCommand{}, fmt.Errorf("%w: data.idempotencyKey is required", errInvalidMessage)
	}
	d := env.Data
	correlation := env.CorrelationID
	if correlation == "" {
		correlation = env.MessageID
	}
	return env, app.WagerCommand{
		Channel:                        wagering.ChannelSQS,
		ProviderID:                     d.ProviderID,
		ExternalTransactionID:          d.ExternalTransactionID,
		IdempotencyKey:                 d.IdempotencyKey,
		WalletID:                       d.WalletID,
		PlayerID:                       d.PlayerID,
		Kind:                           d.Kind,
		Amount:                         d.Money.Amount,
		Currency:                       d.Money.Currency,
		RoundID:                        d.RoundID,
		GameID:                         d.GameID,
		ReferenceExternalTransactionID: d.ReferenceExternalTransactionID,
		CorrelationID:                  correlation,
		CausationID:                    env.MessageID,
	}, nil
}

// Encode builds a message body; producers and tests share it with the decoder.
func Encode(messageID, occurredAt, correlationID string, cmd app.WagerCommand) ([]byte, error) {
	env := envelope{
		MessageID: messageID, Type: MessageTypeWagerRequested, OccurredAt: occurredAt, CorrelationID: correlationID,
		Data: &wagerData{
			ProviderID: cmd.ProviderID, ExternalTransactionID: cmd.ExternalTransactionID,
			IdempotencyKey: cmd.IdempotencyKey, PlayerID: cmd.PlayerID, WalletID: cmd.WalletID,
			RoundID: cmd.RoundID, GameID: cmd.GameID, Kind: cmd.Kind,
			Money:                          &moneyData{Amount: cmd.Amount, Currency: cmd.Currency},
			ReferenceExternalTransactionID: cmd.ReferenceExternalTransactionID,
		},
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(env); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
