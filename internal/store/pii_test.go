// internal/store/pii_test.go
package store

import (
	"bytes"
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/acme/wadist/internal/crypto"
)

// applyAllMigrationsPII reads and applies all migration files in lexicographic order.
// Reuses the same approach as rls_test.go's applyAllMigrationsRLS.
func applyAllMigrationsPII(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	applyAllMigrationsRLS(t, ctx, pool)
}

// testFixedKEK is a fixed 32-byte KEK for tests.
var testFixedKEK = []byte("pii-test-kek-32bytes-xxxxxxxxxxx")

// testBlindKey is a fixed 32-byte blind index key for tests.
var testBlindKey = []byte("pii-blind-key-32bytes-xxxxxxxxxxx")[:32]

func setupPIITest(t *testing.T) (ctx context.Context, m *Manager, c *crypto.Cipher, pool *pgxpool.Pool) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration")
	}
	ctx = context.Background()

	superDSN := testDSN(t)
	pool, err := pgxpool.New(ctx, superDSN)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	applyAllMigrationsPII(t, ctx, pool)

	kr, err := crypto.NewKeyRepo(pool, testFixedKEK)
	if err != nil {
		t.Fatalf("NewKeyRepo: %v", err)
	}

	// Provision DEK for tenant 1.
	if _, err := kr.CreateTenantKey(ctx, 1); err != nil {
		t.Fatalf("CreateTenantKey(1): %v", err)
	}

	c = crypto.NewCipher(kr)

	m = &Manager{
		bizPool:    pool,
		tenantPool: pool,
		systemPool: pool,
	}
	return ctx, m, c, pool
}

// seedCampaign inserts the required campaign_templates + campaigns rows and returns
// the campaign id. campaign_recipients has a FK to campaigns(id).
func seedCampaign(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenantID int64) int64 {
	t.Helper()
	var tmplID int64
	err := pool.QueryRow(ctx,
		`INSERT INTO campaign_templates (tenant_id, kind, body) VALUES ($1, 'sms', 'hello') RETURNING id`,
		tenantID).Scan(&tmplID)
	if err != nil {
		t.Fatalf("seed campaign_template: %v", err)
	}
	var campID int64
	err = pool.QueryRow(ctx,
		`INSERT INTO campaigns (tenant_id, template_id) VALUES ($1, $2) RETURNING id`,
		tenantID, tmplID).Scan(&campID)
	if err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	return campID
}

func TestAddRecipientPII_RoundTrip(t *testing.T) {
	ctx, m, c, pool := setupPIITest(t)

	campID := seedCampaign(t, ctx, pool, 1)

	phone := "+14155551234"
	country := "US"
	vars := []byte(`{"name":"Alice"}`)

	id, err := m.AddRecipientPII(ctx, c, testBlindKey, 1, campID, phone, country, vars)
	if err != nil {
		t.Fatalf("AddRecipientPII: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive id, got %d", id)
	}

	// Read back encrypted values.
	var phoneBack string
	var phoneEnc, phoneBidx []byte
	err = pool.QueryRow(ctx,
		`SELECT phone, phone_enc, phone_bidx FROM campaign_recipients WHERE id=$1`, id).
		Scan(&phoneBack, &phoneEnc, &phoneBidx)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	// Plaintext phone retained.
	if phoneBack != phone {
		t.Errorf("plaintext phone = %q, want %q", phoneBack, phone)
	}

	// Decrypt round-trip.
	decrypted, err := c.Decrypt(ctx, 1, phoneEnc)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if string(decrypted) != phone {
		t.Errorf("decrypted phone = %q, want %q", string(decrypted), phone)
	}

	// Blind index matches.
	expectedBidx := crypto.BlindIndex(testBlindKey, phone)
	if !bytes.Equal(phoneBidx, expectedBidx) {
		t.Errorf("phone_bidx mismatch: got %x, want %x", phoneBidx, expectedBidx)
	}
}

func TestAddRecipientPII_Dedup(t *testing.T) {
	ctx, m, c, pool := setupPIITest(t)

	campID := seedCampaign(t, ctx, pool, 1)

	phone := "+14155559999"
	country := "US"
	vars := []byte(`{}`)

	id1, err := m.AddRecipientPII(ctx, c, testBlindKey, 1, campID, phone, country, vars)
	if err != nil {
		t.Fatalf("first AddRecipientPII: %v", err)
	}

	// Insert duplicate — same campaign + phone.
	id2, err := m.AddRecipientPII(ctx, c, testBlindKey, 1, campID, phone, country, vars)
	if err != nil {
		t.Fatalf("duplicate AddRecipientPII: %v", err)
	}

	if id2 != id1 {
		t.Errorf("dedup: expected same id %d, got %d", id1, id2)
	}

	// Confirm only one row exists.
	var count int
	err = pool.QueryRow(ctx,
		`SELECT count(*) FROM campaign_recipients WHERE campaign_id=$1 AND phone=$2`, campID, phone).
		Scan(&count)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row after dedup, got %d", count)
	}
}

func TestAddRecipientPII_CrossTenantNoDedup(t *testing.T) {
	ctx, m, c, pool := setupPIITest(t)

	// Provision DEK for tenant 2.
	kr, err := crypto.NewKeyRepo(pool, testFixedKEK)
	if err != nil {
		t.Fatalf("NewKeyRepo: %v", err)
	}
	if _, err := kr.CreateTenantKey(ctx, 2); err != nil {
		t.Fatalf("CreateTenantKey(2): %v", err)
	}

	// Two separate campaigns for two tenants.
	camp1 := seedCampaign(t, ctx, pool, 1)
	camp2 := seedCampaign(t, ctx, pool, 2)

	phone := "+14155550001"
	country := "US"
	vars := []byte(`{}`)

	id1, err := m.AddRecipientPII(ctx, c, testBlindKey, 1, camp1, phone, country, vars)
	if err != nil {
		t.Fatalf("tenant1 AddRecipientPII: %v", err)
	}

	id2, err := m.AddRecipientPII(ctx, c, testBlindKey, 2, camp2, phone, country, vars)
	if err != nil {
		t.Fatalf("tenant2 AddRecipientPII: %v", err)
	}

	// Should be different rows (different campaigns).
	if id1 == id2 {
		t.Errorf("cross-tenant same phone: expected different ids, got id1=%d id2=%d", id1, id2)
	}

	// phone_bidx is the same (global blind key).
	var bidx1, bidx2 []byte
	if err := pool.QueryRow(ctx, `SELECT phone_bidx FROM campaign_recipients WHERE id=$1`, id1).Scan(&bidx1); err != nil {
		t.Fatalf("read bidx1: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT phone_bidx FROM campaign_recipients WHERE id=$1`, id2).Scan(&bidx2); err != nil {
		t.Fatalf("read bidx2: %v", err)
	}
	if !bytes.Equal(bidx1, bidx2) {
		t.Errorf("expected same phone_bidx across tenants, got %x vs %x", bidx1, bidx2)
	}

	// phone_enc differs (different DEKs per tenant).
	var enc1, enc2 []byte
	if err := pool.QueryRow(ctx, `SELECT phone_enc FROM campaign_recipients WHERE id=$1`, id1).Scan(&enc1); err != nil {
		t.Fatalf("read enc1: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT phone_enc FROM campaign_recipients WHERE id=$1`, id2).Scan(&enc2); err != nil {
		t.Fatalf("read enc2: %v", err)
	}
	if bytes.Equal(enc1, enc2) {
		t.Errorf("expected different phone_enc for different tenants (different DEKs), but they match")
	}
}

func TestSetDevicePhonePII(t *testing.T) {
	ctx, m, c, pool := setupPIITest(t)

	// Seed an account_device.
	jid := "testjid@s.whatsapp.net"
	phone := "+14155557777"
	_, err := pool.Exec(ctx,
		`INSERT INTO account_devices (tenant_id, account_jid, phone_number) VALUES ($1,$2,$3)`,
		int64(1), jid, phone)
	if err != nil {
		t.Fatalf("seed account_device: %v", err)
	}

	if err := m.SetDevicePhonePII(ctx, c, 1, jid, phone); err != nil {
		t.Fatalf("SetDevicePhonePII: %v", err)
	}

	var phoneEnc []byte
	if err := pool.QueryRow(ctx,
		`SELECT phone_number_enc FROM account_devices WHERE account_jid=$1`, jid).
		Scan(&phoneEnc); err != nil {
		t.Fatalf("read phone_number_enc: %v", err)
	}

	decrypted, err := c.Decrypt(ctx, 1, phoneEnc)
	if err != nil {
		t.Fatalf("Decrypt phone_number_enc: %v", err)
	}
	if string(decrypted) != phone {
		t.Errorf("decrypted = %q, want %q", string(decrypted), phone)
	}
}

func TestAddRecipientPII_NilCipher(t *testing.T) {
	ctx, m, _, _ := setupPIITest(t)
	_, err := m.AddRecipientPII(ctx, nil, testBlindKey, 1, 1, "+1", "US", nil)
	if err == nil {
		t.Fatal("expected error for nil Cipher, got nil")
	}
}

func TestAddRecipientPII_BadBlindKey(t *testing.T) {
	ctx, m, c, _ := setupPIITest(t)
	_, err := m.AddRecipientPII(ctx, c, []byte("tooshort"), 1, 1, "+1", "US", nil)
	if err == nil {
		t.Fatal("expected error for blind key != 32 bytes, got nil")
	}
}
