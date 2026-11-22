package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SECWG/UIR-Agent/internal/protocol"

	bolt "go.etcd.io/bbolt"
)

// Store holds the exclusive task database and its private cache directory.
type Store struct {
	db  *bolt.DB
	Dir string
}

// Record is the durable task state. A final Result is not rewritten by ACKs,
// transfer progress, or reconciliation; those update the surrounding record.
type Record struct {
	Run        protocol.Run
	Hash       string
	Phase      string
	Result     *protocol.Result
	Ack        bool
	Abandoned  bool
	Reconciled bool
	Files      map[string]string
	Uploaded   map[string]bool
	Cleaned    bool
	Transfers  map[string]TransferCheckpoint
}

// TransferCheckpoint persists retry/failure state, not every byte notification.
type TransferCheckpoint struct {
	protocol.Transfer
	Attempts uint
}

func PrepareDirectory(dir string) error {
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		if _, err = os.Stat(filepath.Join(dir, "state.db")); err != nil {
			for _, entry := range entries {
				if entry.Name() != "identity.json" &&
					entry.Name() != "identity.lock" &&
					entry.Name() != "recovery.pending.json" &&
					!strings.HasPrefix(entry.Name(), ".identity-") {
					return errors.New("choose an empty, dedicated agent data directory")
				}
			}
		}
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("data directory must be a real directory")
	}
	return ProtectDirectory(dir)
}

// OpenStore locks a dedicated private directory until Close is called.
func OpenStore(dir string) (*Store, error) {
	if err := PrepareDirectory(dir); err != nil {
		return nil, err
	}
	db, err := bolt.Open(filepath.Join(dir, "state.db"), 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, Dir: dir}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range []string{"identity", "tasks", "history"} {
			if _, e := tx.CreateBucketIfNotExists([]byte(name)); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Joined(id Identity) error {
	id.Joined = true
	return s.SaveIdentity(id)
}

func (s *Store) SaveIdentity(id Identity) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b, e := json.Marshal(id)
		if e != nil {
			return e
		}
		return tx.Bucket([]byte("identity")).Put([]byte("self"), b)
	})
}

func (s *Store) Save(r *Record) error {
	return s.db.Update(func(tx *bolt.Tx) error { return saveRecord(tx, r) })
}

func saveRecord(tx *bolt.Tx, r *Record) error {
	value := *r
	if r.Cleaned {
		if !r.Ack || r.Result == nil || r.Unresolved() {
			return errors.New("cannot archive an unconfirmed task")
		}
		// Retain the request digest and exact result for durable deduplication.
		// Commands and transfer bookkeeping are no longer needed after ACK.
		value.Run = protocol.Run{ID: r.Run.ID}
		value.Phase = ""
		value.Files, value.Uploaded, value.Transfers = nil, nil, nil
	}
	b, e := json.Marshal(&value)
	if e != nil {
		return e
	}
	if r.Cleaned {
		if e = tx.Bucket([]byte("history")).Put([]byte(r.Run.ID), b); e != nil {
			return e
		}
		return tx.Bucket([]byte("tasks")).Delete([]byte(r.Run.ID))
	}
	return tx.Bucket([]byte("tasks")).Put([]byte(r.Run.ID), b)
}

func (s *Store) Record(id string) (*Record, error) {
	var record *Record
	err := s.db.View(func(tx *bolt.Tx) error {
		for _, name := range []string{"tasks", "history"} {
			if raw := tx.Bucket([]byte(name)).Get([]byte(id)); raw != nil {
				return json.Unmarshal(raw, &record)
			}
		}
		return nil
	})
	return record, err
}

func (s *Store) PendingRecords() ([]*Record, error) {
	var list []*Record
	err := s.db.Update(func(tx *bolt.Tx) error {
		var archived []*Record
		err := tx.Bucket([]byte("tasks")).ForEach(func(_, v []byte) error {
			var r Record
			if e := json.Unmarshal(v, &r); e != nil {
				return e
			}
			if r.Cleaned {
				archived = append(archived, &r)
			} else {
				list = append(list, &r)
			}
			return nil
		})
		if err != nil {
			return err
		}
		// Separate already-confirmed records in one transaction on upgrade.
		// Do not rewrite the bucket while its cursor is being traversed.
		for _, r := range archived {
			if err := saveRecord(tx, r); err != nil {
				return err
			}
		}
		return nil
	})
	return list, err
}

func (s *Store) CacheBytes() (int64, error) {
	var size int64
	// Database allocation includes confirmed history and reusable pages. Only
	// outstanding records and task files consume the result-cache budget.
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("tasks")).ForEach(func(k, v []byte) error {
			size += int64(len(k) + len(v))
			return nil
		})
	})
	if err != nil {
		return 0, err
	}
	root := filepath.Join(s.Dir, "tasks")
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		// Confirmed tasks can be removed while admission or collection scans the cache.
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("unexpected link in data directory")
		}
		if !d.IsDir() {
			i, e := d.Info()
			if errors.Is(e, os.ErrNotExist) {
				return nil
			}
			if e != nil {
				return e
			}
			size += i.Size()
		}
		return nil
	})
	return size, err
}

func (r *Record) Unresolved() bool {
	return r.Result != nil && r.Result.State == "unknown" && !r.Reconciled
}
