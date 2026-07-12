// Package phonenorm normalizes raw phone strings to E.164. It is a pragmatic
// normalizer covering the country codes we sell into; unknown/short inputs are
// rejected (ok=false) so the caller can count them as invalid without aborting a
// whole import. libphonenumber-grade validation is a future upgrade.
package phonenorm

import (
	"sort"
	"strings"
)

// callingCode maps ISO alpha-2 -> {international dialing code, national number
// length range [min,max] excluding the country code}.
type ccInfo struct {
	code     string
	min, max int
}

var byCountry = map[string]ccInfo{
	"US": {"1", 10, 10},
	"CA": {"1", 10, 10},
	"CN": {"86", 11, 11},
	"GB": {"44", 9, 10},
	"IN": {"91", 10, 10},
	"BR": {"55", 10, 11},
	"ID": {"62", 9, 12},
	"NG": {"234", 7, 11},
	"MX": {"52", 10, 10},
	"PH": {"63", 10, 10},
}

// byPrefix lets us detect the country when the raw string already carries a
// calling code (longest prefix wins).
func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteByte(byte(r))
		}
	}
	return b.String()
}

func Normalize(raw, defaultCountry string) (string, string, bool) {
	hadPlus := strings.HasPrefix(strings.TrimSpace(raw), "+")
	d := digitsOnly(raw)
	if d == "" {
		return "", "", false
	}
	// Strip a leading international access prefix "00" (e.g. 008613... ).
	if strings.HasPrefix(d, "00") {
		d = strings.TrimPrefix(d, "00")
		hadPlus = true
	}

	// 1) If it starts with a known calling code, trust it.
	if hadPlus || len(d) > 11 {
		// Sort countries for deterministic iteration when multiple share a code.
		// For ambiguous codes like "1" (US/CA), prefer US (comes first in reverse sort).
		var countries []string
		for cc := range byCountry {
			countries = append(countries, cc)
		}
		sort.Sort(sort.Reverse(sort.StringSlice(countries)))
		for _, cc := range countries {
			info := byCountry[cc]
			if strings.HasPrefix(d, info.code) {
				nat := strings.TrimPrefix(d, info.code)
				nat = strings.TrimPrefix(nat, "0") // national trunk 0
				if len(nat) >= info.min && len(nat) <= info.max {
					return "+" + info.code + nat, cc, true
				}
			}
		}
	}

	// 2) Otherwise treat it as a national number for defaultCountry.
	info, known := byCountry[defaultCountry]
	if !known {
		return "", "", false
	}
	nat := strings.TrimPrefix(d, "0")
	// If it already includes the country code (no plus), strip it.
	if strings.HasPrefix(nat, info.code) && len(nat)-len(info.code) >= info.min {
		nat = strings.TrimPrefix(nat, info.code)
		nat = strings.TrimPrefix(nat, "0")
	}
	if len(nat) < info.min || len(nat) > info.max {
		return "", "", false
	}
	return "+" + info.code + nat, defaultCountry, true
}
