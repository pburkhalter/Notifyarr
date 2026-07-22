package waha

import "strings"

// JID suffix constants from WhatsApp/Signal-Protocol's addressing scheme.
// `@c.us` is WAHA-style for personal contacts, `@s.whatsapp.net` is the
// upstream wire form — both surface in the same WAHA install depending on
// engine and event source.
const (
	suffixContact = "@c.us"
	suffixSignal  = "@s.whatsapp.net"
)

// ParsePhoneFromJID returns the digits before "@c.us" / "@s.whatsapp.net".
// Returns "" if the jid isn't a personal phone (e.g. groups, broadcasts).
func ParsePhoneFromJID(jid string) string {
	for _, suf := range []string{suffixContact, suffixSignal} {
		if rest, ok := strings.CutSuffix(jid, suf); ok {
			return rest
		}
	}
	return ""
}

// FormatJID turns a digits-only phone number into a WAHA-style personal
// jid ("<phone>@c.us"). Inverse of ParsePhoneFromJID.
func FormatJID(phone string) string {
	if phone == "" {
		return ""
	}
	return phone + suffixContact
}
