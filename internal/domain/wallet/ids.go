package wallet

import (
	"fmt"

	"github.com/google/uuid"
)

// ID identifies a wallet. The zero value is invalid.
type ID struct{ value uuid.UUID }

// PlayerID identifies the wallet owner. The zero value is invalid.
type PlayerID struct{ value uuid.UUID }

// EntryID identifies a ledger entry. The zero value is invalid.
type EntryID struct{ value uuid.UUID }

func NewID(u uuid.UUID) (ID, error) {
	if u == uuid.Nil {
		return ID{}, ErrInvalidID
	}
	return ID{u}, nil
}

func ParseID(s string) (ID, error) {
	u, err := parseUUID(s)
	if err != nil {
		return ID{}, err
	}
	return NewID(u)
}

func NewPlayerID(u uuid.UUID) (PlayerID, error) {
	if u == uuid.Nil {
		return PlayerID{}, ErrInvalidID
	}
	return PlayerID{u}, nil
}

func ParsePlayerID(s string) (PlayerID, error) {
	u, err := parseUUID(s)
	if err != nil {
		return PlayerID{}, err
	}
	return NewPlayerID(u)
}

func NewEntryID(u uuid.UUID) (EntryID, error) {
	if u == uuid.Nil {
		return EntryID{}, ErrInvalidID
	}
	return EntryID{u}, nil
}

func (id ID) UUID() uuid.UUID       { return id.value }
func (id ID) String() string        { return id.value.String() }
func (id ID) IsZero() bool          { return id.value == uuid.Nil }
func (id PlayerID) UUID() uuid.UUID { return id.value }
func (id PlayerID) String() string  { return id.value.String() }
func (id PlayerID) IsZero() bool    { return id.value == uuid.Nil }
func (id EntryID) UUID() uuid.UUID  { return id.value }
func (id EntryID) String() string   { return id.value.String() }
func (id EntryID) IsZero() bool     { return id.value == uuid.Nil }

// parseUUID accepts only the canonical 36-character form, in any letter case.
func parseUUID(s string) (uuid.UUID, error) {
	if len(s) != 36 {
		return uuid.Nil, fmt.Errorf("%w: %q", ErrInvalidID, s)
	}
	u, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: %q", ErrInvalidID, s)
	}
	return u, nil
}
