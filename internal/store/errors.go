package store

import (
	"errors"
	"fmt"
)

// Sentinel errors. Callers test them with errors.Is.
var (
	// ErrSchemaTooNew means the database needs a newer reader than this binary.
	ErrSchemaTooNew = errors.New("store: database schema is too new for this binary")
	// ErrKeyConflict means an idempotency key is stored with a different
	// fingerprint. The caller maps it to the USAGE refusal (KTD13).
	ErrKeyConflict = errors.New("store: idempotency key reused with different arguments")
	// ErrNotFound means the named row does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrIntentInFlight means the item already holds an unapplied intent (KTD6).
	ErrIntentInFlight = errors.New("store: item already holds an unapplied intent")
	// ErrIntentSettled means the intent is already applied or refused.
	ErrIntentSettled = errors.New("store: intent is already settled")
	// ErrEffectExists means a live outbox row already holds the natural key.
	ErrEffectExists = errors.New("store: outbox effect with this natural key exists")
)

// SchemaTooNewError names both schema versions when Open refuses a database.
type SchemaTooNewError struct {
	Path            string
	DBSchema        int // the database's user_version
	MinReaderSchema int // the oldest schema that may read the database
	BinarySchema    int // the highest migration this binary embeds
}

func (e *SchemaTooNewError) Error() string {
	return fmt.Sprintf("store: database %s needs reader schema %d or later (database schema %d); this binary has schema %d",
		e.Path, e.MinReaderSchema, e.DBSchema, e.BinarySchema)
}

// Is makes errors.Is(err, ErrSchemaTooNew) hold.
func (e *SchemaTooNewError) Is(target error) bool { return target == ErrSchemaTooNew }
