package state

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/SECWG/UIR-Agent/internal/protocol"

	bolt "go.etcd.io/bbolt"
)

// Identity is persisted independently of the running process's task database.
// Credential is a device secret and must not be logged or sent to other origins.
type Identity struct {
	Server           string
	DeviceID         string
	Credential       string
	Joined           bool
	RecoveryCodeHash string `json:"recoveryCodeHash,omitempty"`
	Reenrolling      bool   `json:"reenrolling,omitempty"`
}

func LoadIdentity(dir, server string, recover bool) (Identity, error) {
	path := filepath.Join(dir, "identity.json")
	id, err := ReadIdentity(path, server)
	if !errors.Is(err, os.ErrNotExist) {
		return id, err
	}
	if _, err = os.Stat(filepath.Join(dir, "state.db")); err == nil {
		// 旧版本数据只读取既有身份；占用、损坏或缺失都不能生成另一个设备。
		s, openErr := OpenStore(dir)
		if openErr != nil {
			return id, fmt.Errorf("cannot migrate identity; stop the existing Agent and retry: %w", openErr)
		}
		missing := false
		err = s.db.View(func(tx *bolt.Tx) error {
			raw := tx.Bucket([]byte("identity")).Get([]byte("self"))
			if raw == nil {
				missing = true
				return errors.New("existing task database has no identity; restore the identity file")
			}
			return json.Unmarshal(raw, &id)
		})
		s.Close()
		if missing && recover {
			id = Identity{Server: server, DeviceID: protocol.NewID(), Credential: NewSecret()}
			err = nil
		}
		if err != nil {
			return id, err
		}
		if err = validateIdentity(id, server); err != nil {
			return id, err
		}
	} else if errors.Is(err, os.ErrNotExist) {
		id = Identity{Server: server, DeviceID: protocol.NewID(), Credential: NewSecret()}
	} else {
		return id, err
	}
	return id, WriteIdentity(path, id)
}

func validateIdentity(id Identity, server string) error {
	secret, err := base64.RawURLEncoding.DecodeString(id.Credential)
	if err != nil || len(secret) != 32 || !protocol.ValidID(id.DeviceID) {
		return errors.New("invalid saved Agent identity; restore it instead of registering another device")
	}
	if !SameServer(id.Server, server) {
		return errors.New("data directory belongs to a different server; choose a separate --data-dir")
	}
	return nil
}

func ReadIdentity(path, server string) (Identity, error) {
	var id Identity
	info, err := os.Lstat(path)
	if err != nil {
		return id, err
	}
	if !info.Mode().IsRegular() || info.Size() > 8192 {
		return id, errors.New("identity must be a small regular private file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return id, err
	}
	if err = json.Unmarshal(raw, &id); err != nil {
		return id, errors.New("invalid saved Agent identity")
	}
	return id, validateIdentity(id, server)
}

func WriteIdentity(path string, id Identity) error {
	raw, err := json.Marshal(id)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".identity-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return replaceIdentityFile(file.Name(), path)
}

func IdentityLock(ctx context.Context, dir string) (func(), error) {
	path := filepath.Join(dir, "identity.lock")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("identity lock must be a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	deadline := time.NewTimer(45 * time.Second)
	defer deadline.Stop()
	for {
		locked, err := tryIdentityLock(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		if locked {
			return func() {
				releaseIdentityLock(f)
				f.Close()
			}, nil
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-deadline.C:
			f.Close()
			return nil, errors.New("another Agent is updating its identity; retry shortly")
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func NewSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
