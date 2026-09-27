package postgres

import (
	"context"
	"errors"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

type errorClass int

const (
	classPermanent errorClass = iota
	classTransient
	classUniqueRace
)

// Retry reasons, also used as metric label values.
const (
	reasonSerialization = "serialization"
	reasonDeadlock      = "deadlock"
	reasonLockTimeout   = "lock_timeout"
	reasonUniqueRace    = "unique_race"
	reasonConnection    = "connection"
)

// Unique indexes whose violation means a concurrent writer won the race; the next attempt
// observes the committed row and answers as a replay or a conflict.
var raceConstraints = map[string]struct{}{
	"wager_tx_provider_external_key":    {},
	"wager_tx_provider_idempotency_key": {},
	"wager_tx_single_reversal":          {},
	"inbox_messages_pkey":               {},
}

func classify(err error) (errorClass, string) {
	if err == nil {
		return classPermanent, ""
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return classPermanent, ""
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "40001":
			return classTransient, reasonSerialization
		case pgErr.Code == "40P01":
			return classTransient, reasonDeadlock
		case pgErr.Code == "55P03", pgErr.Code == "57014":
			return classTransient, reasonLockTimeout
		case strings.HasPrefix(pgErr.Code, "08"),
			strings.HasPrefix(pgErr.Code, "53"),
			strings.HasPrefix(pgErr.Code, "57P0"):
			return classTransient, reasonConnection
		case pgErr.Code == "23505":
			if _, ok := raceConstraints[pgErr.ConstraintName]; ok {
				return classUniqueRace, reasonUniqueRace
			}
		}
		return classPermanent, ""
	}

	if pgconn.Timeout(err) || pgconn.SafeToRetry(err) {
		return classTransient, reasonConnection
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return classTransient, reasonConnection
	}
	var connErr *pgconn.ConnectError
	if errors.As(err, &connErr) {
		return classTransient, reasonConnection
	}
	return classPermanent, ""
}

func isRetryable(err error) (bool, string) {
	class, reason := classify(err)
	return class == classTransient || class == classUniqueRace, reason
}

// uniqueViolation reports whether err is a unique violation of the named constraint.
func uniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}
