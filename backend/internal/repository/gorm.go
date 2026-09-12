// Package repository implements the GORM repository layer for campus-market.
package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/lp/campus-market/internal/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// maxTxRetries bounds retry attempts for serialization failures (SQLite
// SQLITE_LOCKED / MySQL deadlock 1213 / lock-wait timeout 1205).
const maxTxRetries = 8

// lockForUpdate applies SELECT ... FOR UPDATE on transactional dialects
// (MySQL) and is a no-op elsewhere (SQLite tests).
func lockForUpdate(q *gorm.DB) *gorm.DB {
	if q.Dialector.Name() == "sqlite" {
		return q
	}
	return q.Clauses(clause.Locking{Strength: "UPDATE"})
}

// txKey is the context key under which an in-flight transaction lives.
type txKey struct{}

// WithTx attaches a GORM transaction to the context so repository methods
// execute within it when present.
func WithTx(ctx context.Context, tx *gorm.DB) context.Context {
	return context.WithValue(ctx, txKey{}, tx)
}

// db returns the transaction-bound database handle if one exists in the
// context, otherwise it returns the repository's base handle.
func db(ctx context.Context, fallback *gorm.DB) *gorm.DB {
	if tx, ok := ctx.Value(txKey{}).(*gorm.DB); ok && tx != nil {
		return tx.WithContext(ctx)
	}
	return fallback.WithContext(ctx)
}

// Transaction runs fn inside a GORM transaction. The transaction handle is
// injected into txCtx so all repository writes participate atomically. When
// the database reports a serialization failure (SQLite immediate
// lock/deadlock, or MySQL deadlock/lock-wait timeout), the whole unit of work
// is retried with bounded backoff; compare-and-swap guards inside fn
// (CreateIfAbsent / DecideIfPending) make retries safe: only one concurrent
// unit of work can claim the row, so business effects still apply once.
func Transaction(ctx context.Context, database *gorm.DB, fn func(txCtx context.Context) error) error {
	var err error
	for attempt := 0; attempt < maxTxRetries; attempt++ {
		err = database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return fn(WithTx(ctx, tx))
		})
		if err == nil || !isLockError(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 5 * time.Millisecond):
		}
	}
	return err
}

// isLockError reports whether err is a dialect-level lock/deadlock failure
// that justifies retrying the whole transaction.
func isLockError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "database is locked"), strings.Contains(msg, "database table is locked"):
		// SQLite SQLITE_BUSY/SQLITE_LOCKED under concurrent WAL writers.
		return true
	case strings.Contains(msg, "deadlock found"), strings.Contains(msg, "lock wait timeout"):
		// MySQL Error 1213 / 1205.
		return true
	default:
		return false
	}
}

// normalizeError converts GORM errors into sentinel repository errors.
func normalizeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return util.ErrNotFound
	}
	return err
}
