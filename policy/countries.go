package policy

import (
	"sort"
	"strings"
)

// callingCodes maps ISO 3166-1 alpha-2 codes to the E.164 prefixes (without
// "+") their numbers start with.
//
// Most countries own one country calling code. Where several share one, the
// prefix is narrowed as far as the numbering plan allows: the Caribbean NANP
// members by their area code (+1 268 is Antigua), Kazakhstan and Russia by the
// first digit after +7. The United States and Canada share +1 with no such
// split — their area codes interleave — so allowing either allows both, and
// the API says so.
var callingCodes = map[string][]string{
	"AD": {"376"}, "AE": {"971"}, "AF": {"93"}, "AG": {"1268"}, "AI": {"1264"},
	"AL": {"355"}, "AM": {"374"}, "AO": {"244"}, "AR": {"54"}, "AS": {"1684"},
	"AT": {"43"}, "AU": {"61"}, "AW": {"297"}, "AZ": {"994"}, "BA": {"387"},
	"BB": {"1246"}, "BD": {"880"}, "BE": {"32"}, "BF": {"226"}, "BG": {"359"},
	"BH": {"973"}, "BI": {"257"}, "BJ": {"229"}, "BM": {"1441"}, "BN": {"673"},
	"BO": {"591"}, "BR": {"55"}, "BS": {"1242"}, "BT": {"975"}, "BW": {"267"},
	"BY": {"375"}, "BZ": {"501"}, "CA": {"1"}, "CD": {"243"}, "CF": {"236"},
	"CG": {"242"}, "CH": {"41"}, "CI": {"225"}, "CK": {"682"}, "CL": {"56"},
	"CM": {"237"}, "CN": {"86"}, "CO": {"57"}, "CR": {"506"}, "CU": {"53"},
	"CV": {"238"}, "CW": {"599"}, "CY": {"357"}, "CZ": {"420"}, "DE": {"49"},
	"DJ": {"253"}, "DK": {"45"}, "DM": {"1767"}, "DO": {"1809", "1829", "1849"}, "DZ": {"213"},
	"EC": {"593"}, "EE": {"372"}, "EG": {"20"}, "ER": {"291"}, "ES": {"34"},
	"ET": {"251"}, "FI": {"358"}, "FJ": {"679"}, "FK": {"500"}, "FM": {"691"},
	"FO": {"298"}, "FR": {"33"}, "GA": {"241"}, "GB": {"44"}, "GD": {"1473"},
	"GE": {"995"}, "GF": {"594"}, "GH": {"233"}, "GI": {"350"}, "GL": {"299"},
	"GM": {"220"}, "GN": {"224"}, "GP": {"590"}, "GQ": {"240"}, "GR": {"30"},
	"GT": {"502"}, "GU": {"1671"}, "GW": {"245"}, "GY": {"592"}, "HK": {"852"},
	"HN": {"504"}, "HR": {"385"}, "HT": {"509"}, "HU": {"36"}, "ID": {"62"},
	"IE": {"353"}, "IL": {"972"}, "IN": {"91"}, "IQ": {"964"}, "IR": {"98"},
	"IS": {"354"}, "IT": {"39"}, "JM": {"1876", "1658"}, "JO": {"962"}, "JP": {"81"},
	"KE": {"254"}, "KG": {"996"}, "KH": {"855"}, "KI": {"686"}, "KM": {"269"},
	"KN": {"1869"}, "KP": {"850"}, "KR": {"82"}, "KW": {"965"}, "KY": {"1345"},
	"KZ": {"76", "77"}, "LA": {"856"}, "LB": {"961"}, "LC": {"1758"}, "LI": {"423"},
	"LK": {"94"}, "LR": {"231"}, "LS": {"266"}, "LT": {"370"}, "LU": {"352"},
	"LV": {"371"}, "LY": {"218"}, "MA": {"212"}, "MC": {"377"}, "MD": {"373"},
	"ME": {"382"}, "MG": {"261"}, "MH": {"692"}, "MK": {"389"}, "ML": {"223"},
	"MM": {"95"}, "MN": {"976"}, "MO": {"853"}, "MP": {"1670"}, "MQ": {"596"},
	"MR": {"222"}, "MS": {"1664"}, "MT": {"356"}, "MU": {"230"}, "MV": {"960"},
	"MW": {"265"}, "MX": {"52"}, "MY": {"60"}, "MZ": {"258"}, "NA": {"264"},
	"NC": {"687"}, "NE": {"227"}, "NG": {"234"}, "NI": {"505"}, "NL": {"31"},
	"NO": {"47"}, "NP": {"977"}, "NR": {"674"}, "NZ": {"64"}, "OM": {"968"},
	"PA": {"507"}, "PE": {"51"}, "PF": {"689"}, "PG": {"675"}, "PH": {"63"},
	"PK": {"92"}, "PL": {"48"}, "PM": {"508"}, "PR": {"1787", "1939"}, "PS": {"970"},
	"PT": {"351"}, "PW": {"680"}, "PY": {"595"}, "QA": {"974"}, "RE": {"262"},
	"RO": {"40"}, "RS": {"381"}, "RU": {"73", "74", "78", "79"}, "RW": {"250"}, "SA": {"966"},
	"SB": {"677"}, "SC": {"248"}, "SD": {"249"}, "SE": {"46"}, "SG": {"65"},
	"SI": {"386"}, "SK": {"421"}, "SL": {"232"}, "SM": {"378"}, "SN": {"221"},
	"SO": {"252"}, "SR": {"597"}, "SS": {"211"}, "ST": {"239"}, "SV": {"503"},
	"SX": {"1721"}, "SY": {"963"}, "SZ": {"268"}, "TC": {"1649"}, "TD": {"235"},
	"TG": {"228"}, "TH": {"66"}, "TJ": {"992"}, "TL": {"670"}, "TM": {"993"},
	"TN": {"216"}, "TO": {"676"}, "TR": {"90"}, "TT": {"1868"}, "TV": {"688"},
	"TW": {"886"}, "TZ": {"255"}, "UA": {"380"}, "UG": {"256"}, "US": {"1"},
	"UY": {"598"}, "UZ": {"998"}, "VA": {"379"}, "VC": {"1784"}, "VE": {"58"},
	"VG": {"1284"}, "VI": {"1340"}, "VN": {"84"}, "VU": {"678"}, "WS": {"685"},
	"XK": {"383"}, "YE": {"967"}, "YT": {"262"}, "ZA": {"27"}, "ZM": {"260"},
	"ZW": {"263"},
}

// Country is one entry of the selectable list.
type Country struct {
	Code     string   `json:"code"`
	Prefixes []string `json:"prefixes"`
}

// Countries lists every known country, sorted by code, for pickers.
func Countries() []Country {
	out := make([]Country, 0, len(callingCodes))
	for code, prefixes := range callingCodes {
		out = append(out, Country{Code: code, Prefixes: prefixes})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// KnownCountry reports whether code is in the table.
func KnownCountry(code string) bool {
	_, ok := callingCodes[code]
	return ok
}

// inCountry reports whether an E.164 number belongs to country code.
func inCountry(e164, code string) bool {
	digits := strings.TrimPrefix(e164, "+")
	for _, p := range callingCodes[code] {
		if strings.HasPrefix(digits, p) {
			return true
		}
	}
	return false
}
