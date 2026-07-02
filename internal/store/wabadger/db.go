// Package wabadger implements the whatsmeow store interfaces on top of a local
// BadgerDB key-value store, replacing the SQL-backed sqlstore. Keys are
// namespaced as code\x00jid\x00sub so a device's whole state is one prefix.
package wabadger

import (
	"bytes"
	"errors"

	badger "github.com/dgraph-io/badger/v4"
)

var sep = []byte{0x00}

// kb builds an exact key by joining parts with the NUL separator (NUL never
// appears in whatsmeow JIDs or signal addresses, so joins are unambiguous).
func kb(parts ...string) []byte {
	bb := make([][]byte, len(parts))
	for i, p := range parts {
		bb[i] = []byte(p)
	}
	return bytes.Join(bb, sep)
}

// kp builds a scan prefix: kb(parts...) plus a trailing separator so it matches
// only full sub-key segments (kp("idt","j1") never matches jid "j1x").
func kp(parts ...string) []byte {
	return append(kb(parts...), sep...)
}

type DB struct{ db *badger.DB }

func Open(dir string) (*DB, error) {
	opts := badger.DefaultOptions(dir).
		WithLogger(nil).      // silence badger's own logging; wire zap later if needed
		WithSyncWrites(false) // per-write sync decided per-call in put()
	bdb, err := badger.Open(opts)
	if err != nil {
		return nil, err
	}
	return &DB{db: bdb}, nil
}

func (d *DB) Close() error { return d.db.Close() }

// get returns (nil, nil) when the key is absent.
func (d *DB) get(k []byte) ([]byte, error) {
	var out []byte
	err := d.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(k)
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		out, err = item.ValueCopy(nil)
		return err
	})
	return out, err
}

// put writes k=v in a single-key transaction. When sync is true, db.Sync() is
// called after the update to force the write to durable storage before
// returning. WriteBatch is intentionally not used here: it commits
// asynchronously across its own set of transactions, which doesn't compose
// with a per-call fsync guarantee.
func (d *DB) put(k, v []byte, sync bool) error {
	if err := d.db.Update(func(txn *badger.Txn) error {
		return txn.Set(k, v)
	}); err != nil {
		return err
	}
	if sync {
		return d.db.Sync()
	}
	return nil
}

// putBatch writes every pair[0]=pair[1] in a SINGLE transaction, so callers
// that must keep several keys consistent (e.g. a bidirectional LID mapping, or
// PutManySessions) get all-or-nothing atomicity. When sync is true, db.Sync()
// forces the batch to durable storage before returning.
func (d *DB) putBatch(pairs [][2][]byte, sync bool) error {
	if err := d.db.Update(func(txn *badger.Txn) error {
		for _, p := range pairs {
			if err := txn.Set(p[0], p[1]); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if sync {
		return d.db.Sync()
	}
	return nil
}

func (d *DB) del(k []byte) error {
	return d.db.Update(func(txn *badger.Txn) error { return txn.Delete(k) })
}

// scanPrefix calls fn(key, value) for every key under prefix p. fn receives
// copies safe to retain.
func (d *DB) scanPrefix(p []byte, fn func(k, v []byte) error) error {
	return d.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		for it.Seek(p); it.ValidForPrefix(p); it.Next() {
			item := it.Item()
			v, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			if err := fn(item.KeyCopy(nil), v); err != nil {
				return err
			}
		}
		return nil
	})
}

// dropPrefix deletes all keys under p (used to delete a device's whole state).
func (d *DB) dropPrefix(p []byte) error { return d.db.DropPrefix(p) }
