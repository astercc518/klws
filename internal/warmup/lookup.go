package warmup

import "context"

// DBLookup resolves a jid's connection info via the account_devices row it's
// paired against (proxy country for Lang, account_instances for the live
// Evolution instance/online state). Built on Store's own pool rather than
// importing internal/store, avoiding a package cycle (store already imports
// warmup for the admin console wiring).
type DBLookup struct{ store *Store }

// NewDBLookup constructs a DBLookup backed by store's pool (must be
// SystemPool — see Store's own doc comment).
func NewDBLookup(store *Store) *DBLookup { return &DBLookup{store: store} }

// WarmupAccount implements AccountLookup. phone_number is NOT read directly:
// account_devices.phone_number may be an encrypted column (see 0008
// phone_number_enc), so the phone digits are derived from the jid itself via
// split_part, matching the pattern already used by EnrollDeviceForInstance.
func (l *DBLookup) WarmupAccount(ctx context.Context, jid string) (Account, error) {
	var a Account
	var country string
	var state *string
	err := l.store.pool.QueryRow(ctx, `
SELECT d.account_jid, COALESCE(ai.instance_name,''), split_part(d.account_jid,'@',1),
       COALESCE(p.country_code,''), ai.state
  FROM account_devices d
  LEFT JOIN proxy_pool p ON p.id = d.proxy_id
  LEFT JOIN account_instances ai ON ai.jid = d.account_jid
 WHERE d.account_jid = $1`, jid).Scan(&a.JID, &a.InstanceName, &a.PhoneNumber, &country, &state)
	if err != nil {
		return Account{}, err
	}
	a.Lang = langForCountry(country)
	a.Online = state != nil && (*state == "open" || *state == "connected")
	return a, nil
}

// langForCountry maps a proxy country code to a warmup script language.
// BR/PT -> pt, CN/HK/TW -> zh, everything else (including empty/unknown) -> en.
func langForCountry(cc string) string {
	switch cc {
	case "BR", "PT":
		return "pt"
	case "CN", "HK", "TW":
		return "zh"
	default:
		return "en"
	}
}
