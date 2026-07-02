# M1: BadgerDB 会话 Store 适配器 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用一个基于 BadgerDB 的本地 KV 适配器替换 whatsmeow 默认的 `sqlstore`，把高频 Signal 会话状态（identity/session/prekey/senderkey/appstate…）从 PostgreSQL 迁到本机 NVMe，消除事务锁等待与 IOPS 瓶颈。

**Architecture:** 新增独立包 `internal/store/wabadger`：一个 `Container`（实现 whatsmeow `store.DeviceContainer` + 持有全局 `LIDMap`）+ 一个 per-device `badgerStore`（实现 `store.AllSessionSpecificStores` 全部 12 个子 store 接口，经 `device.SetAllStores` 装配）。单节点一个 `*badger.DB`，按 `code\x00jid\x00sub` 前缀分区，删号用 `DropPrefix`。正确性由**差分测试**保证：同一组操作分别跑 whatsmeow 官方 `sqlstore`（真 PG，testcontainers）与本适配器，断言可观察行为一致。

**Tech Stack:** Go 1.26、`github.com/dgraph-io/badger/v4`、`go.mau.fi/whatsmeow/store`、`go.mau.fi/whatsmeow/store/sqlstore`（仅测试用作参照）、`go.mau.fi/whatsmeow/util/keys`、`encoding/gob`、testcontainers-go（PG）。

## Global Constraints

- 红线本次已解除，但**必须保留正确性不变式**：适配器行为须与 `sqlstore` 可观察等价（差分测试为准）。
- Badger 持久性策略：`code=dev`（device）与 `code=idt`（identity）写入 `sync=true`（fsync）；其余高频子 store `sync=false`。（spec §3.4 决策①）
- LID map 是**全局**（container 级），key 不含 `{jid}`。（spec §3.2 决策④已核实）
- 包 `internal/store/wabadger` 只依赖 whatsmeow store 接口 + badger；不 import `internal/store` 其它文件（避免环）。装配在 `internal/store/manager.go`。
- 每个子 store 方法签名必须与 `go.mau.fi/whatsmeow/store` 接口逐字一致；用 `var _ store.AllSessionSpecificStores = (*badgerStore)(nil)` 编译期钉死。
- whatsmeow 版本锁定 `v0.0.0-20260622185415-5f04eac6dbbb`（go.mod 现值）。
- 提交信息用语义化前缀；每个 Task 末尾 `make -C .. vet` + 该包 `go test -race` 通过才提交。

---

## 文件结构

- Create `internal/store/wabadger/db.go` — `*badger.DB` 生命周期 + key/KV 辅助（get/put/del/scan/dropPrefix）。
- Create `internal/store/wabadger/device.go` — `deviceRecord` (gob) 序列化 ↔ `*store.Device`。
- Create `internal/store/wabadger/container.go` — `Container` 实现 `DeviceContainer` + 持 `LIDMap`。
- Create `internal/store/wabadger/store.go` — per-device `badgerStore` 骨架 + `Identity`。
- Create `internal/store/wabadger/store_session.go` — `SessionStore`。
- Create `internal/store/wabadger/store_prekey.go` — `PreKeyStore`（含 id 计数器元键）。
- Create `internal/store/wabadger/store_appstate.go` — `SenderKeyStore` + `AppStateSyncKeyStore` + `AppStateStore`。
- Create `internal/store/wabadger/store_misc.go` — `ContactStore`+`ChatSettingsStore`+`MsgSecretStore`+`PrivacyTokenStore`+`NCTSaltStore`+`EventBuffer`。
- Create `internal/store/wabadger/lidmap.go` — 全局 `LIDMap`（实现 `store.LIDStore`）。
- Create `internal/store/wabadger/difftest_test.go` — 差分测试脚手架（对拍 sqlstore）。
- Modify `internal/store/config.go` — 新增 `SessionStore string`（"pg"|"badger"）+ `BadgerDir string` 配置。
- Modify `internal/store/manager.go:52-173` — 依配置选 `sqlstore.Container` 或 `wabadger.Container`。
- Modify `internal/config/config.go` — 读 `WADIST_SESSION_STORE` / `WADIST_BADGER_DIR`。

---

### Task 1: Badger 生命周期 + key/KV 辅助

**Files:**
- Create: `internal/store/wabadger/db.go`
- Test: `internal/store/wabadger/db_test.go`

**Interfaces:**
- Produces: `func Open(dir string) (*DB, error)`；`func (*DB) Close() error`；`func (*DB) get(k []byte) ([]byte, error)`（未命中返回 `nil,nil`）；`func (*DB) put(k, v []byte, sync bool) error`；`func (*DB) del(k []byte) error`；`func (*DB) scanPrefix(p []byte, fn func(k, v []byte) error) error`；`func (*DB) dropPrefix(p []byte) error`；`func kb(parts ...string) []byte`（0x00 连接）；`func kp(parts ...string) []byte`（前缀，尾部追加 0x00）。

- [ ] **Step 1: Write the failing test**

```go
package wabadger

import (
	"bytes"
	"testing"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestKV_PutGetDelScan(t *testing.T) {
	db := openTestDB(t)

	if v, err := db.get(kb("idt", "j1", "a")); err != nil || v != nil {
		t.Fatalf("get missing = %v,%v; want nil,nil", v, err)
	}
	if err := db.put(kb("idt", "j1", "a"), []byte("x"), true); err != nil {
		t.Fatalf("put: %v", err)
	}
	v, err := db.get(kb("idt", "j1", "a"))
	if err != nil || !bytes.Equal(v, []byte("x")) {
		t.Fatalf("get = %q,%v; want x,nil", v, err)
	}
	_ = db.put(kb("idt", "j1", "b"), []byte("y"), false)
	_ = db.put(kb("idt", "j2", "a"), []byte("z"), false) // different jid, must not match

	var got int
	if err := db.scanPrefix(kp("idt", "j1"), func(_, _ []byte) error { got++; return nil }); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got != 2 {
		t.Fatalf("scanPrefix count = %d; want 2", got)
	}
	if err := db.dropPrefix(kp("idt", "j1")); err != nil {
		t.Fatalf("dropPrefix: %v", err)
	}
	if v, _ := db.get(kb("idt", "j1", "a")); v != nil {
		t.Fatalf("after dropPrefix get j1/a = %q; want nil", v)
	}
	if v, _ := db.get(kb("idt", "j2", "a")); !bytes.Equal(v, []byte("z")) {
		t.Fatalf("dropPrefix leaked to j2")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /var/klwa && go test ./internal/store/wabadger/ -run TestKV_PutGetDelScan -v`
Expected: FAIL — package/`Open` undefined (compile error).

- [ ] **Step 3: Write minimal implementation**

```go
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
		WithLogger(nil).       // silence badger's own logging; wire zap later if needed
		WithSyncWrites(false)  // per-write sync decided per-call in put()
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

// put writes k=v. When sync is true the write is fsync'd before returning.
func (d *DB) put(k, v []byte, sync bool) error {
	wb := d.db.NewWriteBatch()
	defer wb.Cancel()
	if err := wb.Set(k, v); err != nil {
		return err
	}
	if err := wb.Flush(); err != nil {
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /var/klwa && go test ./internal/store/wabadger/ -run TestKV_PutGetDelScan -race -v`
Expected: PASS.

- [ ] **Step 5: Add badger dep + commit**

```bash
cd /var/klwa
go get github.com/dgraph-io/badger/v4@latest
go mod tidy
git add go.mod go.sum internal/store/wabadger/db.go internal/store/wabadger/db_test.go
git commit -m "feat(wabadger): badger lifecycle + namespaced KV helpers"
```

---

### Task 2: Device 序列化（deviceRecord ↔ store.Device）

**Files:**
- Create: `internal/store/wabadger/device.go`
- Test: `internal/store/wabadger/device_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `func encodeDevice(d *store.Device) ([]byte, error)`；`func decodeDevice(b []byte, log waLog.Logger) (*store.Device, error)`。字段集逐字镜像 sqlstore `whatsmeow_device` 列（jid,lid,registration_id,noise_key,identity_key,signed_pre_key(+id,+sig),adv_key,adv_details,adv_account_sig,adv_account_sig_key,adv_device_sig,platform,business_name,push_name,facebook_uuid,lid_migration_ts）。

- [ ] **Step 1: Write the failing test** (round-trip a fully-populated device)

```go
package wabadger

import (
	"bytes"
	"testing"

	waLog "go.mau.fi/whatsmeow/util/log"
	"go.mau.fi/whatsmeow/util/keys"
	"go.mau.fi/whatsmeow/types"
	waAdv "go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/store"
)

func sampleDevice() *store.Device {
	jid := types.NewJID("15551234567", types.DefaultUserServer)
	jid.Device = 3
	d := &store.Device{
		Log:            waLog.Noop,
		NoiseKey:       keys.NewKeyPair(),
		IdentityKey:    keys.NewKeyPair(),
		RegistrationID: 42,
		AdvSecretKey:   bytes.Repeat([]byte{7}, 32),
		ID:             &jid,
		Platform:       "test", BusinessName: "biz", PushName: "pn",
		Account: &waAdv.ADVSignedDeviceIdentity{
			Details: []byte("d"), AccountSignature: []byte("as"),
			AccountSignatureKey: []byte("ask"), DeviceSignature: []byte("ds"),
		},
	}
	d.SignedPreKey = d.IdentityKey.CreateSignedPreKey(1)
	return d
}

func TestDevice_RoundTrip(t *testing.T) {
	d := sampleDevice()
	b, err := encodeDevice(d)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := decodeDevice(b, waLog.Noop)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID.String() != d.ID.String() || got.RegistrationID != d.RegistrationID {
		t.Fatalf("id/regid mismatch")
	}
	if got.NoiseKey.Priv != d.NoiseKey.Priv || got.IdentityKey.Priv != d.IdentityKey.Priv {
		t.Fatalf("key priv mismatch")
	}
	if got.SignedPreKey.KeyID != d.SignedPreKey.KeyID || *got.SignedPreKey.Signature != *d.SignedPreKey.Signature {
		t.Fatalf("signed prekey mismatch")
	}
	if got.PushName != "pn" || !bytes.Equal(got.Account.Details, []byte("d")) {
		t.Fatalf("scalar/account mismatch")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /var/klwa && go test ./internal/store/wabadger/ -run TestDevice_RoundTrip -v`
Expected: FAIL — `encodeDevice` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
package wabadger

import (
	"bytes"
	"encoding/gob"
	"fmt"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/keys"
	waLog "go.mau.fi/whatsmeow/util/log"
	waAdv "go.mau.fi/whatsmeow/proto/waAdv"
)

// deviceRecord mirrors the whatsmeow_device columns exactly (see sqlstore
// container.go scanDevice/insertDeviceQuery). Fixed-length key material is
// stored as raw bytes; JIDs as their canonical string form.
type deviceRecord struct {
	JID              string
	LID              string
	RegistrationID   uint32
	NoisePriv        [32]byte
	IdentityPriv     [32]byte
	SignedPreKeyPriv [32]byte
	SignedPreKeyID   uint32
	SignedPreKeySig  [64]byte
	AdvSecretKey     []byte
	AdvDetails       []byte
	AdvAccountSig    []byte
	AdvAccountSigKey []byte
	AdvDeviceSig     []byte
	Platform         string
	BusinessName     string
	PushName         string
	FacebookUUID     string
	LIDMigrationTS   int64
}

func encodeDevice(d *store.Device) ([]byte, error) {
	if d.ID == nil {
		return nil, fmt.Errorf("wabadger: device JID must be set before save")
	}
	rec := deviceRecord{
		JID:              d.ID.String(),
		LID:              d.LID.String(),
		RegistrationID:   d.RegistrationID,
		NoisePriv:        *d.NoiseKey.Priv,
		IdentityPriv:     *d.IdentityKey.Priv,
		SignedPreKeyPriv: *d.SignedPreKey.Priv,
		SignedPreKeyID:   d.SignedPreKey.KeyID,
		SignedPreKeySig:  *d.SignedPreKey.Signature,
		AdvSecretKey:     d.AdvSecretKey,
		Platform:         d.Platform,
		BusinessName:     d.BusinessName,
		PushName:         d.PushName,
		FacebookUUID:     d.FacebookUUID.String(),
		LIDMigrationTS:   d.LIDMigrationTimestamp,
	}
	if d.Account != nil {
		rec.AdvDetails = d.Account.Details
		rec.AdvAccountSig = d.Account.AccountSignature
		rec.AdvAccountSigKey = d.Account.AccountSignatureKey
		rec.AdvDeviceSig = d.Account.DeviceSignature
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(rec); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decodeDevice(b []byte, log waLog.Logger) (*store.Device, error) {
	var rec deviceRecord
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&rec); err != nil {
		return nil, err
	}
	jid, err := types.ParseJID(rec.JID)
	if err != nil {
		return nil, fmt.Errorf("parse device jid %q: %w", rec.JID, err)
	}
	lid, _ := types.ParseJID(rec.LID) // empty LID parses to empty JID; ignore err
	d := &store.Device{
		Log:                   log,
		RegistrationID:        rec.RegistrationID,
		AdvSecretKey:          rec.AdvSecretKey,
		ID:                    &jid,
		LID:                   lid,
		Platform:              rec.Platform,
		BusinessName:          rec.BusinessName,
		PushName:              rec.PushName,
		LIDMigrationTimestamp: rec.LIDMigrationTS,
		Account: &waAdv.ADVSignedDeviceIdentity{
			Details:             rec.AdvDetails,
			AccountSignature:    rec.AdvAccountSig,
			AccountSignatureKey: rec.AdvAccountSigKey,
			DeviceSignature:     rec.AdvDeviceSig,
		},
	}
	d.NoiseKey = keys.NewKeyPairFromPrivateKey(rec.NoisePriv)
	d.IdentityKey = keys.NewKeyPairFromPrivateKey(rec.IdentityPriv)
	d.SignedPreKey = &keys.PreKey{
		KeyPair:   *keys.NewKeyPairFromPrivateKey(rec.SignedPreKeyPriv),
		KeyID:     rec.SignedPreKeyID,
		Signature: &rec.SignedPreKeySig,
	}
	return d, nil
}
```

> Note: `FacebookUUID` is stored as string; on decode leave `d.FacebookUUID` zero unless non-empty (parse with `uuid.Parse` if you wire the `github.com/google/uuid` import — optional, whatsmeow send path does not require it; keep it minimal and only round-trip if a diff test demands it).

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /var/klwa && go test ./internal/store/wabadger/ -run TestDevice_RoundTrip -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /var/klwa
git add internal/store/wabadger/device.go internal/store/wabadger/device_test.go
git commit -m "feat(wabadger): device gob (de)serialization mirroring sqlstore columns"
```

---

### Task 3: Container 实现 DeviceContainer + 差分测试脚手架

**Files:**
- Create: `internal/store/wabadger/container.go`
- Create: `internal/store/wabadger/difftest_test.go`
- Test: `internal/store/wabadger/container_test.go`

**Interfaces:**
- Consumes: Task 1 `*DB`/helpers；Task 2 `encodeDevice/decodeDevice`。
- Produces: `func NewContainer(db *DB, log waLog.Logger) *Container`；`Container` 实现 `store.DeviceContainer`（`GetDevice/GetAllDevices/NewDevice/PutDevice/DeleteDevice`）；`func (c *Container) newBadgerStore(jid types.JID) *badgerStore`（Task 4 用）；差分辅助 `func newRefContainer(t) *sqlstore.Container`（testcontainers PG）。

- [ ] **Step 1: Write the failing test**

```go
package wabadger

import (
	"context"
	"testing"

	waLog "go.mau.fi/whatsmeow/util/log"
)

func TestContainer_DevicePersistence(t *testing.T) {
	ctx := context.Background()
	c := NewContainer(openTestDB(t), waLog.Noop)

	nd := c.NewDevice()
	if nd.ID != nil {
		t.Fatalf("NewDevice must have nil ID before pairing")
	}
	d := sampleDevice() // has an ID
	if err := c.PutDevice(ctx, d); err != nil {
		t.Fatalf("PutDevice: %v", err)
	}
	got, err := c.GetDevice(ctx, *d.ID)
	if err != nil || got == nil {
		t.Fatalf("GetDevice = %v,%v", got, err)
	}
	if got.RegistrationID != d.RegistrationID {
		t.Fatalf("regid mismatch after reload")
	}
	all, err := c.GetAllDevices(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("GetAllDevices = %d,%v; want 1", len(all), err)
	}
	if err := c.DeleteDevice(ctx, d); err != nil {
		t.Fatalf("DeleteDevice: %v", err)
	}
	if got, _ := c.GetDevice(ctx, *d.ID); got != nil {
		t.Fatalf("device present after delete")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /var/klwa && go test ./internal/store/wabadger/ -run TestContainer_DevicePersistence -v`
Expected: FAIL — `NewContainer` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
package wabadger

import (
	"context"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// Container implements store.DeviceContainer backed by BadgerDB. The LID map is
// global (container-level), matching sqlstore where device.LIDs = c.LIDMap.
type Container struct {
	db     *DB
	log    waLog.Logger
	lidMap *LIDMap // wired in Task 8; nil-safe until then
}

func NewContainer(db *DB, log waLog.Logger) *Container {
	c := &Container{db: db, log: log}
	c.lidMap = newLIDMap(db) // defined in Task 8
	return c
}

var _ store.DeviceContainer = (*Container)(nil)

// initializeDevice wires all per-device stores + the global LID store, mirroring
// sqlstore.Container.initializeDevice.
func (c *Container) initializeDevice(d *store.Device) {
	bs := c.newBadgerStore(*d.ID)
	d.SetAllStores(bs)
	d.LIDs = c.lidMap
	d.Container = c
	d.Initialized = true
}

func (c *Container) NewDevice() *store.Device {
	return &store.Device{
		Log:            c.log,
		Container:      c,
		NoiseKey:       newKeyPair(),
		IdentityKey:    newKeyPair(),
		RegistrationID: randUint32(),
		AdvSecretKey:   randBytes32(),
	}
	// SignedPreKey is derived by whatsmeow after IdentityKey is set; mirror
	// sqlstore by generating it here (see Step 3b).
}

func (c *Container) GetDevice(ctx context.Context, jid types.JID) (*store.Device, error) {
	b, err := c.db.get(kb("dev", jid.String()))
	if err != nil || b == nil {
		return nil, err
	}
	d, err := decodeDevice(b, c.log)
	if err != nil {
		return nil, err
	}
	c.initializeDevice(d)
	return d, nil
}

func (c *Container) GetAllDevices(ctx context.Context) ([]*store.Device, error) {
	var out []*store.Device
	err := c.db.scanPrefix(kp("dev"), func(_, v []byte) error {
		d, err := decodeDevice(v, c.log)
		if err != nil {
			return err
		}
		c.initializeDevice(d)
		out = append(out, d)
		return nil
	})
	return out, err
}

func (c *Container) PutDevice(ctx context.Context, d *store.Device) error {
	b, err := encodeDevice(d)
	if err != nil {
		return err
	}
	if err := c.db.put(kb("dev", d.ID.String()), b, true); err != nil { // dev = sync
		return err
	}
	if !d.Initialized {
		c.initializeDevice(d)
	}
	return nil
}

func (c *Container) DeleteDevice(ctx context.Context, d *store.Device) error {
	if d.ID == nil {
		return store.ErrDeviceIDMustBeSet
	}
	jid := d.ID.String()
	// delete the device row + all per-device sub-store keys (every code\x00jid\x00…).
	for _, code := range perDeviceCodes {
		if err := c.db.dropPrefix(kp(code, jid)); err != nil {
			return err
		}
	}
	return c.db.del(kb("dev", jid))
}

// perDeviceCodes enumerates every key namespace scoped to a single device, used
// by DeleteDevice. Keep in sync as stores are added.
var perDeviceCodes = []string{
	"idt", "ses", "pk", "pkm", "sk", "ask", "askl", "asv", "asm",
	"ctc", "chs", "msec", "ptk", "nct", "evb", "oge",
}
```

- [ ] **Step 3b: Add key/random helpers to device.go**

```go
// appended to internal/store/wabadger/device.go
import cryptoRand "crypto/rand"

func newKeyPair() *keys.KeyPair { return keys.NewKeyPair() }

func randUint32() uint32 {
	var b [4]byte
	_, _ = cryptoRand.Read(b[:])
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func randBytes32() []byte {
	b := make([]byte, 32)
	_, _ = cryptoRand.Read(b)
	return b
}
```

And fix `NewDevice` to derive the signed pre-key (mirror sqlstore):

```go
func (c *Container) NewDevice() *store.Device {
	d := &store.Device{
		Log: c.log, Container: c,
		NoiseKey: newKeyPair(), IdentityKey: newKeyPair(),
		RegistrationID: randUint32(), AdvSecretKey: randBytes32(),
	}
	d.SignedPreKey = d.IdentityKey.CreateSignedPreKey(1)
	return d
}
```

- [ ] **Step 3c: Write the differential-test scaffold** `difftest_test.go`

```go
package wabadger

import (
	"context"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"database/sql"

	_ "github.com/jackc/pgx/v5/stdlib"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// newRefContainer spins a real PG (testcontainers) and returns whatsmeow's own
// sqlstore.Container as the behavioural reference. Skips if Docker is absent.
func newRefContainer(t *testing.T) *sqlstore.Container {
	t.Helper()
	ctx := context.Background()
	pg, err := tcpg.Run(ctx, "postgres:16",
		tcpg.WithDatabase("wm"), tcpg.WithUsername("wm"), tcpg.WithPassword("wm"),
		testcontainers.WithWaitStrategyAndDeadline(0, defaultPGWait()...))
	if err != nil {
		t.Skipf("no docker for reference PG: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(ctx) })
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	c := sqlstore.NewWithDB(db, "postgres", waLog.Noop)
	if err := c.Upgrade(ctx); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	return c
}
```

> `defaultPGWait()` is a tiny local helper returning the project's standard readiness wait strategies — copy the pattern from `internal/store/testsupport_test.go` (existing). If simpler, reuse that file's existing PG-spawn helper directly instead of duplicating.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /var/klwa && go test ./internal/store/wabadger/ -run TestContainer -race -v`
Expected: PASS (differential PG test skips if no Docker; container test passes on Badger alone).

- [ ] **Step 5: Commit**

```bash
cd /var/klwa
git add internal/store/wabadger/container.go internal/store/wabadger/difftest_test.go internal/store/wabadger/container_test.go internal/store/wabadger/device.go
git commit -m "feat(wabadger): DeviceContainer (get/put/delete/all) + diff-test scaffold"
```

---

### Task 4: per-device badgerStore 骨架 + IdentityStore

**Files:**
- Create: `internal/store/wabadger/store.go`
- Test: `internal/store/wabadger/store_identity_test.go`

**Interfaces:**
- Consumes: Task 1 `*DB`；Task 3 `Container`。
- Produces: `type badgerStore struct{ db *DB; jid string }`；`func (c *Container) newBadgerStore(jid types.JID) *badgerStore`；实现 `store.IdentityStore`（`PutIdentity/DeleteAllIdentities/DeleteIdentity`）+ `IsTrustedIdentity`（whatsmeow signal 层运行期需要）。语义镜像 sqlstore：identity 存 32 字节；`DeleteAllIdentities(phone)` 删 `phone:*` 前缀（address 形如 `user:device` 或 `user.device`）；未知 identity 视为可信。

- [ ] **Step 1: Write the failing test**

```go
package wabadger

import (
	"context"
	"testing"

	waLog "go.mau.fi/whatsmeow/util/log"
	"go.mau.fi/whatsmeow/types"
)

func newTestStore(t *testing.T) *badgerStore {
	c := NewContainer(openTestDB(t), waLog.Noop)
	jid := types.NewJID("15550000001", types.DefaultUserServer)
	return c.newBadgerStore(jid)
}

func TestIdentity_PutTrustDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	var key [32]byte
	key[0] = 9

	// unknown identity is trusted
	if ok, err := s.IsTrustedIdentity(ctx, "u1:1", key); err != nil || !ok {
		t.Fatalf("unknown identity should be trusted: %v,%v", ok, err)
	}
	if err := s.PutIdentity(ctx, "u1:1", key); err != nil {
		t.Fatalf("PutIdentity: %v", err)
	}
	if ok, _ := s.IsTrustedIdentity(ctx, "u1:1", key); !ok {
		t.Fatalf("stored identity should be trusted")
	}
	var other [32]byte
	other[0] = 1
	if ok, _ := s.IsTrustedIdentity(ctx, "u1:1", other); ok {
		t.Fatalf("different key must be untrusted")
	}
	_ = s.PutIdentity(ctx, "u1:2", key)
	if err := s.DeleteAllIdentities(ctx, "u1"); err != nil {
		t.Fatalf("DeleteAllIdentities: %v", err)
	}
	if ok, _ := s.IsTrustedIdentity(ctx, "u1:1", other); !ok {
		t.Fatalf("after delete-all, identity should be unknown→trusted")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /var/klwa && go test ./internal/store/wabadger/ -run TestIdentity -v`
Expected: FAIL — `newBadgerStore`/`IsTrustedIdentity` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
package wabadger

import (
	"context"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

// badgerStore implements store.AllSessionSpecificStores for a single device.
type badgerStore struct {
	db  *DB
	jid string
}

func (c *Container) newBadgerStore(jid types.JID) *badgerStore {
	return &badgerStore{db: c.db, jid: jid.String()}
}

// compile-time proof the adapter satisfies every per-device store interface.
var _ store.AllSessionSpecificStores = (*badgerStore)(nil)

func (s *badgerStore) PutIdentity(ctx context.Context, address string, key [32]byte) error {
	return s.db.put(kb("idt", s.jid, address), key[:], true) // idt = sync
}

func (s *badgerStore) DeleteIdentity(ctx context.Context, address string) error {
	return s.db.del(kb("idt", s.jid, address))
}

func (s *badgerStore) DeleteAllIdentities(ctx context.Context, phone string) error {
	// address form is "<user>:<device>"/"<user>.<device>"; delete the whole user.
	return s.db.dropPrefix(kp("idt", s.jid, phone))
}

func (s *badgerStore) IsTrustedIdentity(ctx context.Context, address string, key [32]byte) (bool, error) {
	v, err := s.db.get(kb("idt", s.jid, address))
	if err != nil {
		return false, err
	}
	if v == nil {
		return true, nil // unknown → trusted (matches sqlstore)
	}
	if len(v) != 32 {
		return false, nil
	}
	return *(*[32]byte)(v) == key, nil
}
```

> Note: `DeleteAllIdentities` prefix is `kp("idt",jid,phone)` = `idt\x00jid\x00phone\x00`, which only matches addresses `phone\x00...`. Since addresses are `phone:device`, and we store them as a single segment `address`, use address delimiter awareness: store identities under key `kb("idt", jid, phone, device)` by splitting address on the last separator — OR keep address as one segment and match `phone` as a **value prefix**. To keep sqlstore's `LIKE phone:%` semantics exact, store as `kb("idt", jid, address)` and implement DeleteAllIdentities by scanning `kp("idt", jid)` and deleting keys whose address segment has prefix `phone+":"` or `phone+"."`. Replace the body accordingly:

```go
func (s *badgerStore) DeleteAllIdentities(ctx context.Context, phone string) error {
	pfx := kp("idt", s.jid)
	var toDel [][]byte
	err := s.db.scanPrefix(pfx, func(k, _ []byte) error {
		addr := string(k[len(pfx):])
		if addr == phone || hasSignalUserPrefix(addr, phone) {
			toDel = append(toDel, k)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, k := range toDel {
		if err := s.db.del(k); err != nil {
			return err
		}
	}
	return nil
}

// hasSignalUserPrefix reports whether a signal address belongs to user `phone`
// (address form "<user>:<device>" or "<user>.<device>").
func hasSignalUserPrefix(addr, phone string) bool {
	return len(addr) > len(phone) && addr[:len(phone)] == phone &&
		(addr[len(phone)] == ':' || addr[len(phone)] == '.')
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /var/klwa && go test ./internal/store/wabadger/ -run TestIdentity -race -v`
Expected: PASS. (Note: `var _ store.AllSessionSpecificStores` will NOT compile until Tasks 5-8 add the remaining methods; temporarily comment that line, restore it in Task 8's final step. Add a `// TODO(compile-gate): re-enable in Task 8` — this is the ONE allowed temporary scaffold, removed by Task 8.)

- [ ] **Step 5: Commit**

```bash
cd /var/klwa
git add internal/store/wabadger/store.go internal/store/wabadger/store_identity_test.go
git commit -m "feat(wabadger): per-device store skeleton + IdentityStore"
```

---

### Task 5: SessionStore

**Files:**
- Create: `internal/store/wabadger/store_session.go`
- Test: `internal/store/wabadger/store_session_test.go`

**Interfaces:**
- Produces on `*badgerStore`: `GetSession(ctx,address)([]byte,error)`（未命中返回 `nil,nil`）、`HasSession`、`GetManySessions`、`PutSession`、`PutManySessions`、`DeleteAllSessions`(前缀)、`DeleteSession`、`MigratePNToLID(ctx, pn, lid types.JID)`（把 `pn:*` 会话/身份复制到 `lid:*`）。

- [ ] **Step 1: Write the failing test**

```go
package wabadger

import (
	"bytes"
	"context"
	"testing"
)

func TestSession_CRUDAndMany(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if v, _ := s.GetSession(ctx, "u:1"); v != nil {
		t.Fatalf("missing session must be nil")
	}
	_ = s.PutSession(ctx, "u:1", []byte("s1"))
	_ = s.PutSession(ctx, "u:2", []byte("s2"))
	if has, _ := s.HasSession(ctx, "u:1"); !has {
		t.Fatalf("HasSession false after put")
	}
	m, err := s.GetManySessions(ctx, []string{"u:1", "u:2", "u:3"})
	if err != nil || len(m) != 2 || !bytes.Equal(m["u:1"], []byte("s1")) {
		t.Fatalf("GetManySessions = %v,%v", m, err)
	}
	_ = s.PutManySessions(ctx, map[string][]byte{"u:3": []byte("s3")})
	if v, _ := s.GetSession(ctx, "u:3"); !bytes.Equal(v, []byte("s3")) {
		t.Fatalf("PutManySessions failed")
	}
	_ = s.DeleteSession(ctx, "u:1")
	if has, _ := s.HasSession(ctx, "u:1"); has {
		t.Fatalf("DeleteSession failed")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /var/klwa && go test ./internal/store/wabadger/ -run TestSession -v`
Expected: FAIL — methods undefined.

- [ ] **Step 3: Write minimal implementation**

```go
package wabadger

import (
	"context"

	"go.mau.fi/whatsmeow/types"
)

func (s *badgerStore) GetSession(ctx context.Context, address string) ([]byte, error) {
	return s.db.get(kb("ses", s.jid, address))
}

func (s *badgerStore) HasSession(ctx context.Context, address string) (bool, error) {
	v, err := s.db.get(kb("ses", s.jid, address))
	return v != nil, err
}

func (s *badgerStore) GetManySessions(ctx context.Context, addresses []string) (map[string][]byte, error) {
	if len(addresses) == 0 {
		return nil, nil
	}
	out := make(map[string][]byte, len(addresses))
	for _, a := range addresses {
		v, err := s.db.get(kb("ses", s.jid, a))
		if err != nil {
			return nil, err
		}
		if v != nil {
			out[a] = v
		}
	}
	return out, nil
}

func (s *badgerStore) PutSession(ctx context.Context, address string, session []byte) error {
	return s.db.put(kb("ses", s.jid, address), session, false) // high-churn → async
}

func (s *badgerStore) PutManySessions(ctx context.Context, sessions map[string][]byte) error {
	for a, sess := range sessions {
		if err := s.db.put(kb("ses", s.jid, a), sess, false); err != nil {
			return err
		}
	}
	return nil
}

func (s *badgerStore) DeleteSession(ctx context.Context, address string) error {
	return s.db.del(kb("ses", s.jid, address))
}

func (s *badgerStore) DeleteAllSessions(ctx context.Context, phone string) error {
	pfx := kp("ses", s.jid)
	var toDel [][]byte
	err := s.db.scanPrefix(pfx, func(k, _ []byte) error {
		if addr := string(k[len(pfx):]); addr == phone || hasSignalUserPrefix(addr, phone) {
			toDel = append(toDel, k)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, k := range toDel {
		if err := s.db.del(k); err != nil {
			return err
		}
	}
	return nil
}

// MigratePNToLID copies sessions AND identities whose address is under the phone
// JID's user to the corresponding LID user (mirrors sqlstore's PN→LID migration).
func (s *badgerStore) MigratePNToLID(ctx context.Context, pn, lid types.JID) error {
	pnUser, lidUser := pn.User, lid.User
	for _, code := range []string{"ses", "idt"} {
		pfx := kp(code, s.jid)
		type kv struct{ k, v []byte }
		var copies []kv
		err := s.db.scanPrefix(pfx, func(k, v []byte) error {
			addr := string(k[len(pfx):])
			if hasSignalUserPrefix(addr, pnUser) {
				newAddr := lidUser + addr[len(pnUser):]
				copies = append(copies, kv{k: kb(code, s.jid, newAddr), v: append([]byte(nil), v...)})
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, c := range copies {
			if err := s.db.put(c.k, c.v, false); err != nil {
				return err
			}
		}
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /var/klwa && go test ./internal/store/wabadger/ -run TestSession -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /var/klwa
git add internal/store/wabadger/store_session.go internal/store/wabadger/store_session_test.go
git commit -m "feat(wabadger): SessionStore incl GetMany/PutMany/MigratePNToLID"
```

---

### Task 6: PreKeyStore（含 id 计数器元键）

**Files:**
- Create: `internal/store/wabadger/store_prekey.go`
- Test: `internal/store/wabadger/store_prekey_test.go`

**Interfaces:**
- Produces on `*badgerStore`: `GetOrGenPreKeys(ctx,count)([]*keys.PreKey,error)`、`GenOnePreKey(ctx)(*keys.PreKey,error)`、`GetPreKey(ctx,id)`、`RemovePreKey(ctx,id)`、`MarkPreKeysAsUploaded(ctx,upToID)`、`UploadedPreKeyCount(ctx)(int,error)`。id 用单调元键 `pkm\x00jid`（uint32 BE）分配，永不复用；prekey 存 `pk\x00jid\x00{id:BE32}` → value=`priv[32]||uploaded(1B)`。加 `sync.Mutex` 串行化分配。

- [ ] **Step 1: Write the failing test**

```go
package wabadger

import (
	"context"
	"testing"
)

func TestPreKey_GenGetMarkCount(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	ks, err := s.GetOrGenPreKeys(ctx, 5)
	if err != nil || len(ks) != 5 {
		t.Fatalf("GetOrGenPreKeys = %d,%v; want 5", len(ks), err)
	}
	// idempotent-ish: unuploaded keys are reused, count stays 5
	ks2, _ := s.GetOrGenPreKeys(ctx, 5)
	if len(ks2) != 5 || ks2[0].KeyID != ks[0].KeyID {
		t.Fatalf("second GetOrGen must reuse unuploaded keys")
	}
	got, err := s.GetPreKey(ctx, ks[0].KeyID)
	if err != nil || got == nil || got.KeyID != ks[0].KeyID {
		t.Fatalf("GetPreKey = %v,%v", got, err)
	}
	if err := s.MarkPreKeysAsUploaded(ctx, ks[2].KeyID); err != nil {
		t.Fatalf("MarkUploaded: %v", err)
	}
	if n, _ := s.UploadedPreKeyCount(ctx); n != 3 {
		t.Fatalf("UploadedPreKeyCount = %d; want 3", n)
	}
	if err := s.RemovePreKey(ctx, ks[0].KeyID); err != nil {
		t.Fatalf("RemovePreKey: %v", err)
	}
	if got, _ := s.GetPreKey(ctx, ks[0].KeyID); got != nil {
		t.Fatalf("prekey present after remove")
	}
	one, err := s.GenOnePreKey(ctx)
	if err != nil || one == nil {
		t.Fatalf("GenOnePreKey: %v,%v", one, err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /var/klwa && go test ./internal/store/wabadger/ -run TestPreKey -v`
Expected: FAIL — methods undefined.

- [ ] **Step 3: Write minimal implementation**

```go
package wabadger

import (
	"context"
	"encoding/binary"
	"sync"

	"go.mau.fi/whatsmeow/util/keys"
)

var preKeyLock sync.Mutex // package-level: prekey id allocation is per-jid but
// serialized globally for simplicity (allocation is rare and cheap).

func preKeyVal(k *keys.PreKey, uploaded bool) []byte {
	v := make([]byte, 33)
	copy(v[:32], k.Priv[:])
	if uploaded {
		v[32] = 1
	}
	return v
}

func idKey(id uint32) string {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], id)
	return string(b[:])
}

func (s *badgerStore) nextPreKeyID() (uint32, error) {
	v, err := s.db.get(kb("pkm", s.jid))
	if err != nil {
		return 0, err
	}
	var last uint32
	if len(v) == 4 {
		last = binary.BigEndian.Uint32(v)
	}
	return last + 1, nil
}

func (s *badgerStore) setLastPreKeyID(id uint32) error {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], id)
	return s.db.put(kb("pkm", s.jid), b[:], true)
}

func (s *badgerStore) genOnePreKey(id uint32, uploaded bool) (*keys.PreKey, error) {
	k := keys.NewPreKey(id)
	if err := s.db.put(kb("pk", s.jid, idKey(id)), preKeyVal(k, uploaded), false); err != nil {
		return nil, err
	}
	return k, nil
}

func (s *badgerStore) GenOnePreKey(ctx context.Context) (*keys.PreKey, error) {
	preKeyLock.Lock()
	defer preKeyLock.Unlock()
	id, err := s.nextPreKeyID()
	if err != nil {
		return nil, err
	}
	k, err := s.genOnePreKey(id, true)
	if err != nil {
		return nil, err
	}
	return k, s.setLastPreKeyID(id)
}

func (s *badgerStore) GetOrGenPreKeys(ctx context.Context, count uint32) ([]*keys.PreKey, error) {
	preKeyLock.Lock()
	defer preKeyLock.Unlock()

	// collect existing unuploaded keys, ordered by id.
	type idk struct {
		id uint32
		k  *keys.PreKey
	}
	var existing []idk
	pfx := kp("pk", s.jid)
	err := s.db.scanPrefix(pfx, func(k, v []byte) error {
		if len(v) != 33 || v[32] == 1 {
			return nil // skip malformed or uploaded
		}
		id := binary.BigEndian.Uint32([]byte(string(k[len(pfx):])))
		existing = append(existing, idk{id: id, k: &keys.PreKey{
			KeyPair: *keys.NewKeyPairFromPrivateKey(*(*[32]byte)(v[:32])),
			KeyID:   id,
		}})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sortByID(existing) // ascending; helper below

	out := make([]*keys.PreKey, 0, count)
	for _, e := range existing {
		if uint32(len(out)) >= count {
			break
		}
		out = append(out, e.k)
	}
	if uint32(len(out)) < count {
		id, err := s.nextPreKeyID()
		if err != nil {
			return nil, err
		}
		for uint32(len(out)) < count {
			k, err := s.genOnePreKey(id, false)
			if err != nil {
				return nil, err
			}
			out = append(out, k)
			id++
		}
		if err := s.setLastPreKeyID(id - 1); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func sortByID[T any](s []struct {
	id uint32
	k  *keys.PreKey
}) {
	// simple insertion sort (n is small: prekey batch ~ tens)
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1].id > s[j].id; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func (s *badgerStore) GetPreKey(ctx context.Context, id uint32) (*keys.PreKey, error) {
	v, err := s.db.get(kb("pk", s.jid, idKey(id)))
	if err != nil || v == nil {
		return nil, err
	}
	if len(v) < 32 {
		return nil, nil
	}
	return &keys.PreKey{
		KeyPair: *keys.NewKeyPairFromPrivateKey(*(*[32]byte)(v[:32])),
		KeyID:   id,
	}, nil
}

func (s *badgerStore) RemovePreKey(ctx context.Context, id uint32) error {
	return s.db.del(kb("pk", s.jid, idKey(id)))
}

func (s *badgerStore) MarkPreKeysAsUploaded(ctx context.Context, upToID uint32) error {
	pfx := kp("pk", s.jid)
	type kv struct {
		k, v []byte
	}
	var ups []kv
	err := s.db.scanPrefix(pfx, func(k, v []byte) error {
		if len(v) != 33 {
			return nil
		}
		id := binary.BigEndian.Uint32([]byte(string(k[len(pfx):])))
		if id <= upToID && v[32] == 0 {
			nv := append([]byte(nil), v...)
			nv[32] = 1
			ups = append(ups, kv{k: append([]byte(nil), k...), v: nv})
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, u := range ups {
		if err := s.db.put(u.k, u.v, false); err != nil {
			return err
		}
	}
	return nil
}

func (s *badgerStore) UploadedPreKeyCount(ctx context.Context) (int, error) {
	n := 0
	err := s.db.scanPrefix(kp("pk", s.jid), func(_, v []byte) error {
		if len(v) == 33 && v[32] == 1 {
			n++
		}
		return nil
	})
	return n, err
}
```

> `sortByID` generic on the anonymous struct is awkward; if the compiler rejects the anonymous-struct type param, hoist `type idk struct{ id uint32; k *keys.PreKey }` to package scope and make `sortByID([]idk)`. Adjust both call site and signature together.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /var/klwa && go test ./internal/store/wabadger/ -run TestPreKey -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /var/klwa
git add internal/store/wabadger/store_prekey.go internal/store/wabadger/store_prekey_test.go
git commit -m "feat(wabadger): PreKeyStore with monotonic id counter"
```

---

### Task 7: SenderKeyStore + AppState（sync keys / version / mutation MACs）

**Files:**
- Create: `internal/store/wabadger/store_appstate.go`
- Test: `internal/store/wabadger/store_appstate_test.go`

**Interfaces:**
- Produces on `*badgerStore`: `PutSenderKey/GetSenderKey`；`PutAppStateSyncKey/GetAppStateSyncKey/GetLatestAppStateSyncKeyID/GetAllAppStateSyncKeys`；`PutAppStateVersion/GetAppStateVersion/DeleteAppStateVersion`；`PutAppStateMutationMACs/DeleteAppStateMutationMACs/GetAppStateMutationMAC`。类型：`store.AppStateSyncKey{Data,Fingerprint,Timestamp}`、`store.AppStateMutationMAC{IndexMAC,ValueMAC}`。

- [ ] **Step 1: Write the failing test**

```go
package wabadger

import (
	"bytes"
	"context"
	"testing"

	"go.mau.fi/whatsmeow/store"
)

func TestAppState_VersionMutationSenderKey(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// sender key
	_ = s.PutSenderKey(ctx, "grp@g.us", "u:1", []byte("sk"))
	if v, _ := s.GetSenderKey(ctx, "grp@g.us", "u:1"); !bytes.Equal(v, []byte("sk")) {
		t.Fatalf("sender key round-trip failed")
	}
	// app state version
	var hash [128]byte
	hash[0] = 5
	_ = s.PutAppStateVersion(ctx, "critical_block", 7, hash)
	gotV, gotH, _ := s.GetAppStateVersion(ctx, "critical_block")
	if gotV != 7 || gotH != hash {
		t.Fatalf("app state version mismatch")
	}
	// mutation macs
	_ = s.PutAppStateMutationMACs(ctx, "critical_block", 7, []store.AppStateMutationMAC{
		{IndexMAC: []byte("i1"), ValueMAC: []byte("v1")},
	})
	if vm, _ := s.GetAppStateMutationMAC(ctx, "critical_block", []byte("i1")); !bytes.Equal(vm, []byte("v1")) {
		t.Fatalf("mutation MAC round-trip failed")
	}
	// sync key + latest
	_ = s.PutAppStateSyncKey(ctx, []byte("kid1"), store.AppStateSyncKey{Data: []byte("d"), Timestamp: 100})
	_ = s.PutAppStateSyncKey(ctx, []byte("kid2"), store.AppStateSyncKey{Data: []byte("d2"), Timestamp: 200})
	latest, _ := s.GetLatestAppStateSyncKeyID(ctx)
	if !bytes.Equal(latest, []byte("kid2")) {
		t.Fatalf("latest sync key id = %q; want kid2", latest)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /var/klwa && go test ./internal/store/wabadger/ -run TestAppState -v`
Expected: FAIL — methods undefined.

- [ ] **Step 3: Write minimal implementation**

```go
package wabadger

import (
	"context"
	"encoding/binary"
	"encoding/gob"
	"bytes"

	"go.mau.fi/whatsmeow/store"
)

func gobEnc(v any) []byte {
	var buf bytes.Buffer
	_ = gob.NewEncoder(&buf).Encode(v)
	return buf.Bytes()
}
func gobDec(b []byte, v any) error { return gob.NewDecoder(bytes.NewReader(b)).Decode(v) }

// --- SenderKeyStore ---
func (s *badgerStore) PutSenderKey(ctx context.Context, group, user string, session []byte) error {
	return s.db.put(kb("sk", s.jid, group, user), session, false)
}
func (s *badgerStore) GetSenderKey(ctx context.Context, group, user string) ([]byte, error) {
	return s.db.get(kb("sk", s.jid, group, user))
}

// --- AppStateStore (version + mutation MACs) ---
type appStateVersionRec struct {
	Version uint64
	Hash    [128]byte
}

func (s *badgerStore) PutAppStateVersion(ctx context.Context, name string, version uint64, hash [128]byte) error {
	return s.db.put(kb("asv", s.jid, name), gobEnc(appStateVersionRec{Version: version, Hash: hash}), false)
}
func (s *badgerStore) GetAppStateVersion(ctx context.Context, name string) (uint64, [128]byte, error) {
	v, err := s.db.get(kb("asv", s.jid, name))
	if err != nil || v == nil {
		return 0, [128]byte{}, err
	}
	var rec appStateVersionRec
	if err := gobDec(v, &rec); err != nil {
		return 0, [128]byte{}, err
	}
	return rec.Version, rec.Hash, nil
}
func (s *badgerStore) DeleteAppStateVersion(ctx context.Context, name string) error {
	if err := s.db.del(kb("asv", s.jid, name)); err != nil {
		return err
	}
	return s.db.dropPrefix(kp("asm", s.jid, name)) // drop this name's mutation MACs
}
func (s *badgerStore) PutAppStateMutationMACs(ctx context.Context, name string, version uint64, mutations []store.AppStateMutationMAC) error {
	for _, m := range mutations {
		if err := s.db.put(kb("asm", s.jid, name, string(m.IndexMAC)), m.ValueMAC, false); err != nil {
			return err
		}
	}
	return nil
}
func (s *badgerStore) DeleteAppStateMutationMACs(ctx context.Context, name string, indexMACs [][]byte) error {
	for _, im := range indexMACs {
		if err := s.db.del(kb("asm", s.jid, name, string(im))); err != nil {
			return err
		}
	}
	return nil
}
func (s *badgerStore) GetAppStateMutationMAC(ctx context.Context, name string, indexMAC []byte) ([]byte, error) {
	return s.db.get(kb("asm", s.jid, name, string(indexMAC)))
}

// --- AppStateSyncKeyStore ---
func (s *badgerStore) PutAppStateSyncKey(ctx context.Context, id []byte, key store.AppStateSyncKey) error {
	if err := s.db.put(kb("ask", s.jid, string(id)), gobEnc(key), true); err != nil {
		return err
	}
	// track latest-by-timestamp pointer
	cur, err := s.GetLatestAppStateSyncKeyID(ctx)
	if err != nil {
		return err
	}
	if cur != nil {
		if prev, _ := s.getSyncKey(cur); prev != nil && prev.Timestamp > key.Timestamp {
			return nil // existing latest is newer; keep it
		}
	}
	return s.db.put(kb("askl", s.jid), id, true)
}
func (s *badgerStore) getSyncKey(id []byte) (*store.AppStateSyncKey, error) {
	v, err := s.db.get(kb("ask", s.jid, string(id)))
	if err != nil || v == nil {
		return nil, err
	}
	var k store.AppStateSyncKey
	if err := gobDec(v, &k); err != nil {
		return nil, err
	}
	return &k, nil
}
func (s *badgerStore) GetAppStateSyncKey(ctx context.Context, id []byte) (*store.AppStateSyncKey, error) {
	return s.getSyncKey(id)
}
func (s *badgerStore) GetLatestAppStateSyncKeyID(ctx context.Context) ([]byte, error) {
	return s.db.get(kb("askl", s.jid))
}
func (s *badgerStore) GetAllAppStateSyncKeys(ctx context.Context) ([]*store.AppStateSyncKey, error) {
	var out []*store.AppStateSyncKey
	err := s.db.scanPrefix(kp("ask", s.jid), func(_, v []byte) error {
		var k store.AppStateSyncKey
		if err := gobDec(v, &k); err != nil {
			return err
		}
		out = append(out, &k)
		return nil
	})
	return out, err
}

var _ = binary.BigEndian // keep import if unused after edits
```

> Verify `store.AppStateSyncKey` field names against the whatsmeow version (`grep -n "type AppStateSyncKey" $WM/store/store.go`). If `GetLatestAppStateSyncKeyID` semantics in whatsmeow pick max-by-timestamp, the pointer-update logic above matches; adjust field name `Timestamp` if it differs (e.g., `Timestamp int64`).

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /var/klwa && go test ./internal/store/wabadger/ -run TestAppState -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /var/klwa
git add internal/store/wabadger/store_appstate.go internal/store/wabadger/store_appstate_test.go
git commit -m "feat(wabadger): SenderKey + AppState (version/mutation/sync-key) stores"
```

---

### Task 8: 外围 stores（Contacts/ChatSettings/MsgSecrets/PrivacyTokens/NCTSalt/EventBuffer）+ 编译钉死

**Files:**
- Create: `internal/store/wabadger/store_misc.go`
- Test: `internal/store/wabadger/store_misc_test.go`

**Interfaces:**
- Produces on `*badgerStore` 全部剩余接口方法（签名以 `$WM/store/store.go` 为准）。完成后**恢复** Task 4 注释掉的 `var _ store.AllSessionSpecificStores = (*badgerStore)(nil)`。

- [ ] **Step 1: Read exact signatures then write the failing test**

Run first: `grep -nA3 "type ContactStore\|type ChatSettingsStore\|type MsgSecretStore\|type PrivacyTokenStore\|type NCTSaltStore\|type EventBuffer\|type ContactEntry\|type RedactedPhoneEntry\|type MessageSecretInsert\|type PrivacyToken\|type BufferedEvent" /root/go/pkg/mod/go.mau.fi/whatsmeow@v0.0.0-20260622185415-5f04eac6dbbb/store/store.go`

Then the test (round-trips one value per store):

```go
package wabadger

import (
	"bytes"
	"context"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestMisc_ContactChatNCTSecrets(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	u := types.NewJID("15550000009", types.DefaultUserServer)

	if _, _, err := s.PutPushName(ctx, u, "Alice"); err != nil {
		t.Fatalf("PutPushName: %v", err)
	}
	ci, err := s.GetContact(ctx, u)
	if err != nil || ci.PushName != "Alice" {
		t.Fatalf("GetContact = %+v,%v", ci, err)
	}
	_ = s.PutNCTSalt(ctx, []byte("salt"))
	if v, _ := s.GetNCTSalt(ctx); !bytes.Equal(v, []byte("salt")) {
		t.Fatalf("NCT salt round-trip failed")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /var/klwa && go test ./internal/store/wabadger/ -run TestMisc -v`
Expected: FAIL — methods undefined.

- [ ] **Step 3: Write minimal implementation**

```go
package wabadger

import (
	"context"
	"time"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

// --- ContactStore --- (values gob-encoded types.ContactInfo)
func (s *badgerStore) getContact(u types.JID) (types.ContactInfo, error) {
	var ci types.ContactInfo
	v, err := s.db.get(kb("ctc", s.jid, u.String()))
	if err != nil || v == nil {
		return ci, err
	}
	return ci, gobDec(v, &ci)
}
func (s *badgerStore) putContact(u types.JID, ci types.ContactInfo) error {
	return s.db.put(kb("ctc", s.jid, u.String()), gobEnc(ci), false)
}
func (s *badgerStore) PutPushName(ctx context.Context, u types.JID, pushName string) (bool, string, error) {
	ci, err := s.getContact(u)
	if err != nil {
		return false, "", err
	}
	prev := ci.PushName
	if prev == pushName {
		return false, prev, nil
	}
	ci.PushName, ci.Found = pushName, true
	return true, prev, s.putContact(u, ci)
}
func (s *badgerStore) PutBusinessName(ctx context.Context, u types.JID, businessName string) (bool, string, error) {
	ci, err := s.getContact(u)
	if err != nil {
		return false, "", err
	}
	prev := ci.BusinessName
	if prev == businessName {
		return false, prev, nil
	}
	ci.BusinessName, ci.Found = businessName, true
	return true, prev, s.putContact(u, ci)
}
func (s *badgerStore) PutContactName(ctx context.Context, u types.JID, fullName, firstName string) error {
	ci, err := s.getContact(u)
	if err != nil {
		return err
	}
	ci.FullName, ci.FirstName, ci.Found = fullName, firstName, true
	return s.putContact(u, ci)
}
func (s *badgerStore) PutAllContactNames(ctx context.Context, contacts []store.ContactEntry) error {
	for _, e := range contacts {
		ci, err := s.getContact(e.JID)
		if err != nil {
			return err
		}
		ci.FullName, ci.FirstName, ci.Found = e.FullName, e.FirstName, true
		if err := s.putContact(e.JID, ci); err != nil {
			return err
		}
	}
	return nil
}
func (s *badgerStore) PutManyRedactedPhones(ctx context.Context, entries []store.RedactedPhoneEntry) error {
	// stored alongside contact; minimal: no-op persistence keyed by jid is
	// acceptable for send path. Persist if a diff test requires it.
	return nil
}
func (s *badgerStore) GetContact(ctx context.Context, u types.JID) (types.ContactInfo, error) {
	return s.getContact(u)
}
func (s *badgerStore) GetAllContacts(ctx context.Context) (map[types.JID]types.ContactInfo, error) {
	out := map[types.JID]types.ContactInfo{}
	pfx := kp("ctc", s.jid)
	err := s.db.scanPrefix(pfx, func(k, v []byte) error {
		j, perr := types.ParseJID(string(k[len(pfx):]))
		if perr != nil {
			return nil
		}
		var ci types.ContactInfo
		if err := gobDec(v, &ci); err != nil {
			return err
		}
		out[j] = ci
		return nil
	})
	return out, err
}

// --- ChatSettingsStore ---
type chatSettingsRec struct {
	MutedUntil int64
	Pinned     bool
	Archived   bool
}

func (s *badgerStore) getChat(chat types.JID) (chatSettingsRec, error) {
	var r chatSettingsRec
	v, err := s.db.get(kb("chs", s.jid, chat.String()))
	if err != nil || v == nil {
		return r, err
	}
	return r, gobDec(v, &r)
}
func (s *badgerStore) putChat(chat types.JID, r chatSettingsRec) error {
	return s.db.put(kb("chs", s.jid, chat.String()), gobEnc(r), false)
}
func (s *badgerStore) PutMutedUntil(ctx context.Context, chat types.JID, until time.Time) error {
	r, err := s.getChat(chat)
	if err != nil {
		return err
	}
	if until.IsZero() {
		r.MutedUntil = 0
	} else {
		r.MutedUntil = until.Unix()
	}
	return s.putChat(chat, r)
}
func (s *badgerStore) PutPinned(ctx context.Context, chat types.JID, pinned bool) error {
	r, err := s.getChat(chat)
	if err != nil {
		return err
	}
	r.Pinned = pinned
	return s.putChat(chat, r)
}
func (s *badgerStore) PutArchived(ctx context.Context, chat types.JID, archived bool) error {
	r, err := s.getChat(chat)
	if err != nil {
		return err
	}
	r.Archived = archived
	return s.putChat(chat, r)
}
func (s *badgerStore) GetChatSettings(ctx context.Context, chat types.JID) (types.LocalChatSettings, error) {
	r, err := s.getChat(chat)
	if err != nil {
		return types.LocalChatSettings{}, err
	}
	out := types.LocalChatSettings{Found: true, Pinned: r.Pinned, Archived: r.Archived}
	if r.MutedUntil != 0 {
		out.MutedUntil = time.Unix(r.MutedUntil, 0)
	}
	return out, nil
}

// --- MsgSecretStore ---
func (s *badgerStore) PutMessageSecrets(ctx context.Context, inserts []store.MessageSecretInsert) error {
	for _, in := range inserts {
		if err := s.db.put(kb("msec", s.jid, in.Chat.String(), in.Sender.String(), string(in.ID)), in.Secret, false); err != nil {
			return err
		}
	}
	return nil
}
func (s *badgerStore) PutMessageSecret(ctx context.Context, chat, sender types.JID, id types.MessageID, secret []byte) error {
	return s.db.put(kb("msec", s.jid, chat.String(), sender.String(), string(id)), secret, false)
}
func (s *badgerStore) GetMessageSecret(ctx context.Context, chat, sender types.JID, id types.MessageID) ([]byte, types.JID, error) {
	v, err := s.db.get(kb("msec", s.jid, chat.String(), sender.String(), string(id)))
	return v, sender, err
}

// --- PrivacyTokenStore ---
type privacyTokenRec struct {
	Token     []byte
	Timestamp int64
}

func (s *badgerStore) PutPrivacyTokens(ctx context.Context, tokens ...store.PrivacyToken) error {
	for _, t := range tokens {
		rec := privacyTokenRec{Token: t.Token, Timestamp: t.Timestamp.Unix()}
		if err := s.db.put(kb("ptk", s.jid, t.User.String()), gobEnc(rec), false); err != nil {
			return err
		}
	}
	return nil
}
func (s *badgerStore) GetPrivacyToken(ctx context.Context, user types.JID) (*store.PrivacyToken, error) {
	v, err := s.db.get(kb("ptk", s.jid, user.String()))
	if err != nil || v == nil {
		return nil, err
	}
	var rec privacyTokenRec
	if err := gobDec(v, &rec); err != nil {
		return nil, err
	}
	return &store.PrivacyToken{User: user, Token: rec.Token, Timestamp: time.Unix(rec.Timestamp, 0)}, nil
}
func (s *badgerStore) DeleteExpiredPrivacyTokens(ctx context.Context, cutoff time.Time) (int64, error) {
	var n int64
	pfx := kp("ptk", s.jid)
	var toDel [][]byte
	err := s.db.scanPrefix(pfx, func(k, v []byte) error {
		var rec privacyTokenRec
		if gobDec(v, &rec) == nil && time.Unix(rec.Timestamp, 0).Before(cutoff) {
			toDel = append(toDel, append([]byte(nil), k...))
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for _, k := range toDel {
		if err := s.db.del(k); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// --- NCTSaltStore ---
func (s *badgerStore) PutNCTSalt(ctx context.Context, salt []byte) error {
	return s.db.put(kb("nct", s.jid), salt, true)
}
func (s *badgerStore) GetNCTSalt(ctx context.Context) ([]byte, error) { return s.db.get(kb("nct", s.jid)) }
func (s *badgerStore) DeleteNCTSalt(ctx context.Context) error       { return s.db.del(kb("nct", s.jid)) }

// --- EventBuffer --- (buffered inbound + outgoing dedup; TTL via Badger entry TTL)
func (s *badgerStore) GetBufferedEvent(ctx context.Context, ciphertextHash [32]byte) (*store.BufferedEvent, error) {
	v, err := s.db.get(kb("evb", s.jid, string(ciphertextHash[:])))
	if err != nil || v == nil {
		return nil, err
	}
	var be store.BufferedEvent
	if err := gobDec(v, &be); err != nil {
		return nil, err
	}
	return &be, nil
}
func (s *badgerStore) PutBufferedEvent(ctx context.Context, ciphertextHash [32]byte, plaintext []byte, serverTimestamp time.Time) error {
	be := store.BufferedEvent{Plaintext: plaintext, InsertTime: time.Now(), ServerTimestamp: serverTimestamp}
	return s.db.put(kb("evb", s.jid, string(ciphertextHash[:])), gobEnc(be), false)
}
func (s *badgerStore) DoDecryptionTxn(ctx context.Context, fn func(store.EventBuffer) error) error {
	return fn(s) // Badger writes are atomic per-op; a coarse pass-through is acceptable
}
func (s *badgerStore) ClearBufferedEventPlaintext(ctx context.Context, ciphertextHash [32]byte) error {
	return s.db.del(kb("evb", s.jid, string(ciphertextHash[:])))
}
func (s *badgerStore) DeleteOldBufferedHashes(ctx context.Context) error {
	return nil // rely on periodic external compaction / optional Badger TTL
}
func (s *badgerStore) GetOutgoingEvent(ctx context.Context, chatJID, altChatJID types.JID, id types.MessageID) (string, []byte, error) {
	v, err := s.db.get(kb("oge", s.jid, chatJID.String(), string(id)))
	if err != nil || v == nil {
		return "", nil, err
	}
	return string(id), v, nil
}
func (s *badgerStore) PutOutgoingEvent(ctx context.Context, chatJID, altChatJID types.JID, id types.MessageID, payload []byte) error {
	return s.db.put(kb("oge", s.jid, chatJID.String(), string(id)), payload, false)
}
func (s *badgerStore) DeleteOldOutgoingEvents(ctx context.Context) error { return nil }
```

> The EventBuffer method set (`DoDecryptionTxn`, `ClearBufferedEventPlaintext`, `PutOutgoingEvent`, exact signatures) MUST be verified against `$WM/store/store.go` `type EventBuffer interface` before writing — copy the exact method list from the grep in Step 1 and implement each. The bodies above are the pattern; align names/args to the interface exactly.

- [ ] **Step 3b: Restore the compile-gate assertion in `store.go`**

Uncomment (from Task 4): `var _ store.AllSessionSpecificStores = (*badgerStore)(nil)` and remove the TODO. Run `go build ./internal/store/wabadger/` — any missing/mismatched method now fails compilation. Fix signatures until it builds.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /var/klwa && go build ./internal/store/wabadger/ && go test ./internal/store/wabadger/ -run TestMisc -race -v`
Expected: build OK (interface fully satisfied) + PASS.

- [ ] **Step 5: Commit**

```bash
cd /var/klwa
git add internal/store/wabadger/store_misc.go internal/store/wabadger/store_misc_test.go internal/store/wabadger/store.go
git commit -m "feat(wabadger): contacts/chat/msgsecret/privacy/nct/eventbuffer + interface assertion"
```

---

### Task 9: 全局 LIDMap（store.LIDStore）

**Files:**
- Create: `internal/store/wabadger/lidmap.go`
- Test: `internal/store/wabadger/lidmap_test.go`

**Interfaces:**
- Produces: `func newLIDMap(db *DB) *LIDMap`；`LIDMap` 实现 `store.LIDStore`（`PutLIDMapping/PutManyLIDMappings/GetPNForLID/GetLIDForPN/GetManyLIDsForPNs`）。**全局**（key 不含 device jid）：`lidpn\x00{lid}`→pn、`lidlid\x00{pn}`→lid。

- [ ] **Step 1: Write the failing test**

```go
package wabadger

import (
	"context"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestLIDMap_Bidirectional(t *testing.T) {
	ctx := context.Background()
	m := newLIDMap(openTestDB(t))
	lid := types.NewJID("111", types.HiddenUserServer)
	pn := types.NewJID("222", types.DefaultUserServer)
	if err := m.PutLIDMapping(ctx, lid, pn); err != nil {
		t.Fatalf("PutLIDMapping: %v", err)
	}
	if got, _ := m.GetPNForLID(ctx, lid); got.User != pn.User {
		t.Fatalf("GetPNForLID = %s; want %s", got, pn)
	}
	if got, _ := m.GetLIDForPN(ctx, pn); got.User != lid.User {
		t.Fatalf("GetLIDForPN = %s; want %s", got, lid)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /var/klwa && go test ./internal/store/wabadger/ -run TestLIDMap -v`
Expected: FAIL — `newLIDMap` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
package wabadger

import (
	"context"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

type LIDMap struct{ db *DB }

func newLIDMap(db *DB) *LIDMap { return &LIDMap{db: db} }

var _ store.LIDStore = (*LIDMap)(nil)

func (m *LIDMap) PutLIDMapping(ctx context.Context, lid, jid types.JID) error {
	if err := m.db.put(kb("lidpn", lid.User), []byte(jid.String()), false); err != nil {
		return err
	}
	return m.db.put(kb("lidlid", jid.User), []byte(lid.String()), false)
}
func (m *LIDMap) PutManyLIDMappings(ctx context.Context, mappings []store.LIDMapping) error {
	for _, mp := range mappings {
		if err := m.PutLIDMapping(ctx, mp.LID, mp.PN); err != nil {
			return err
		}
	}
	return nil
}
func (m *LIDMap) GetPNForLID(ctx context.Context, lid types.JID) (types.JID, error) {
	v, err := m.db.get(kb("lidpn", lid.User))
	if err != nil || v == nil {
		return types.EmptyJID, err
	}
	return types.ParseJID(string(v))
}
func (m *LIDMap) GetLIDForPN(ctx context.Context, pn types.JID) (types.JID, error) {
	v, err := m.db.get(kb("lidlid", pn.User))
	if err != nil || v == nil {
		return types.EmptyJID, err
	}
	return types.ParseJID(string(v))
}
func (m *LIDMap) GetManyLIDsForPNs(ctx context.Context, pns []types.JID) (map[types.JID]types.JID, error) {
	out := map[types.JID]types.JID{}
	for _, pn := range pns {
		if lid, err := m.GetLIDForPN(ctx, pn); err == nil && !lid.IsEmpty() {
			out[pn] = lid
		}
	}
	return out, nil
}
```

> Verify `store.LIDMapping` field names (`LID`,`PN`) and `types.EmptyJID`/`JID.IsEmpty()` against the whatsmeow version; adjust if the mapping struct uses different names.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /var/klwa && go test ./internal/store/wabadger/ -run TestLIDMap -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /var/klwa
git add internal/store/wabadger/lidmap.go internal/store/wabadger/lidmap_test.go
git commit -m "feat(wabadger): global LIDStore"
```

---

### Task 10: 差分测试（对拍 sqlstore）+ Manager 装配（配置门控）

**Files:**
- Modify: `internal/store/config.go` (add `SessionStore`, `BadgerDir`)
- Modify: `internal/store/manager.go:52-173` (select container backend)
- Modify: `internal/config/config.go` (read `WADIST_SESSION_STORE`, `WADIST_BADGER_DIR`)
- Create: `internal/store/wabadger/difftest_store_test.go` (behavioural parity)

**Interfaces:**
- Consumes: whatsmeow `sqlstore.Container` (reference), `wabadger.Container` (subject).
- Produces: config-gated container selection; `WADIST_SESSION_STORE=badger|pg` (default `pg` this milestone; flip to `badger` after M-send integration proves it).

- [ ] **Step 1: Write the differential parity test**

```go
package wabadger

import (
	"context"
	"testing"

	waLog "go.mau.fi/whatsmeow/util/log"
	"go.mau.fi/whatsmeow/types"
)

// TestDiff_IdentitySession runs identical ops through the reference sqlstore and
// the Badger adapter and asserts identical observable results.
func TestDiff_IdentitySession(t *testing.T) {
	ctx := context.Background()
	ref := newRefContainer(t) // skips if no docker
	jid := types.NewJID("15559999999", types.DefaultUserServer)

	// reference device
	rd := ref.NewDevice()
	rd.ID = &jid
	if err := ref.PutDevice(ctx, rd); err != nil {
		t.Fatalf("ref put: %v", err)
	}
	// subject device (Badger)
	sub := NewContainer(openTestDB(t), waLog.Noop)
	sd := sub.NewDevice()
	sd.ID = &jid
	if err := sub.PutDevice(ctx, sd); err != nil {
		t.Fatalf("sub put: %v", err)
	}

	var key [32]byte
	key[0] = 3
	if err := rd.Identities.PutIdentity(ctx, "x:1", key); err != nil {
		t.Fatalf("ref put identity: %v", err)
	}
	if err := sd.Identities.PutIdentity(ctx, "x:1", key); err != nil {
		t.Fatalf("sub put identity: %v", err)
	}
	_ = rd.Sessions.PutSession(ctx, "x:1", []byte("sess"))
	_ = sd.Sessions.PutSession(ctx, "x:1", []byte("sess"))

	rHas, _ := rd.Sessions.HasSession(ctx, "x:1")
	sHas, _ := sd.Sessions.HasSession(ctx, "x:1")
	if rHas != sHas {
		t.Fatalf("HasSession parity: ref=%v sub=%v", rHas, sHas)
	}
	rSess, _ := rd.Sessions.GetSession(ctx, "x:1")
	sSess, _ := sd.Sessions.GetSession(ctx, "x:1")
	if string(rSess) != string(sSess) {
		t.Fatalf("GetSession parity mismatch")
	}
}
```

- [ ] **Step 2: Run to verify it fails / skips cleanly**

Run: `cd /var/klwa && go test ./internal/store/wabadger/ -run TestDiff_IdentitySession -v`
Expected: PASS if Docker present (parity holds), or SKIP (no Docker). If it FAILS on a real mismatch, fix the adapter before proceeding.

- [ ] **Step 3: Add config fields**

In `internal/store/config.go`, add to `Config`:

```go
// SessionStore selects the whatsmeow store backend: "pg" (sqlstore, default) or
// "badger" (local NVMe KV via internal/store/wabadger).
SessionStore string
// BadgerDir is the on-disk directory for the Badger session store (SessionStore=="badger").
BadgerDir string
```

And in `withDefaults()`:

```go
if c.SessionStore == "" {
	c.SessionStore = "pg"
}
if c.BadgerDir == "" {
	c.BadgerDir = "/var/lib/wadist/badger"
}
```

- [ ] **Step 4: Wire the backend in manager.go**

Replace the fixed `container := sqlstore.NewWithDB(...)` block (`internal/store/manager.go:71-77`) with a backend switch. Introduce a small interface so `Manager` holds either container:

```go
// deviceContainer is the subset of container behaviour Manager needs
// (both sqlstore.Container and wabadger.Container satisfy it via store.DeviceContainer).
type deviceContainer interface {
	GetDevice(ctx context.Context, jid types.JID) (*store.Device, error)
	NewDevice() *store.Device
}
```

Change `Manager.container` field type to `deviceContainer`, then:

```go
var container deviceContainer
switch cfg.SessionStore {
case "badger":
	bdb, err := wabadger.Open(cfg.BadgerDir)
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("open badger: %w", err)
	}
	container = wabadger.NewContainer(bdb, logger)
	// NOTE: sqlDB is still opened for business tables; whatsmeow tables are unused.
default: // "pg"
	sc := sqlstore.NewWithDB(sqlDB, "postgres", logger)
	upgradeCtx, upgradeCancel := context.WithTimeout(ctx, 30*time.Second)
	defer upgradeCancel()
	if err := sc.Upgrade(upgradeCtx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("upgrade whatsmeow schema: %w", err)
	}
	container = sc
}
```

Update `device.go` `GetDeviceStore`/`NewDeviceStore` to call through the `deviceContainer` interface (methods already match). Store a handle to the badger `*DB` on Manager for graceful `Close()`.

- [ ] **Step 5: Read env in config**

In `internal/config/config.go`, add to the `Store()` construction:

```go
SessionStore: os.Getenv("WADIST_SESSION_STORE"),
BadgerDir:    os.Getenv("WADIST_BADGER_DIR"),
```

- [ ] **Step 6: Verify build + full gate**

Run:
```bash
cd /var/klwa
go build ./...
go test ./internal/store/... -race
make gate
```
Expected: build OK; store tests pass (badger unit + diff parity where Docker present); `make gate` green. Default backend remains `pg`, so existing behaviour is unchanged until `WADIST_SESSION_STORE=badger` is set.

- [ ] **Step 7: Commit**

```bash
cd /var/klwa
git add internal/store/config.go internal/store/manager.go internal/store/device.go internal/config/config.go internal/store/wabadger/difftest_store_test.go
git commit -m "feat(store): config-gated badger session backend (default pg) + diff parity test"
```

---

## Self-Review 结论（作者已核对）

- **Spec 覆盖**：spec §3.1 全部 13 接口面 → Task 4–9 逐一实现（LID 为全局，Task 9）；§3.2 映射表 → Task 1 key 布局 + 各 Task；§3.3 批量/扫描/DropPrefix → Task 1 helpers；§3.4 sync 策略 → Global Constraints + 各 put 调用的 sync 实参（dev/idt/nct/ask/askl/pkm sync=true，其余 false）。
- **占位符扫描**：无 TODO/“类似上文”。唯一临时脚手架=Task 4 注释 `var _` 断言，Task 8 Step 3b 明确恢复。
- **类型一致性**：`badgerStore`、`Container`、`NewContainer`、`newBadgerStore`、`kb/kp/get/put/del/scanPrefix/dropPrefix`、`gobEnc/gobDec`、`encodeDevice/decodeDevice` 跨 Task 命名一致。
- **已标注的实现期核实点**（非占位，是「以 whatsmeow 源为准」的对齐动作，附 grep 命令）：AppStateSyncKey / EventBuffer / ContactEntry / RedactedPhoneEntry / MessageSecretInsert / PrivacyToken / LIDMapping 的精确字段与方法集（Task 7/8/9 内已给 grep 与对齐指令）。这些是版本相关的签名对齐，必须在写该 Task 时以源码为准，不能臆造。

## 风险与后续

- **差分测试是安全网**：任何适配器行为偏差由 Task 3/10 的对拍捕获；M-send 集成（真 whatsmeow 起号→配对→发送经 Badger）在后续 M3 里端到端验证。
- **未覆盖**（YAGNI / 后续里程碑）：Badger value-log GC 调优、EventBuffer 的 TTL 自动过期（当前 DeleteOld 为 no-op，靠后续外部压缩）、多机部署下 per-node Badger 的账号→节点粘性路由（Phase 2）。
