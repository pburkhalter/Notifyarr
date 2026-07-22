package waha

import "testing"

func TestParsePhoneFromJID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"41791112233@c.us", "41791112233"},
		{"41791112233@s.whatsapp.net", "41791112233"},
		{"120363@g.us", ""},
		{"", ""},
		{"not-a-jid", ""},
		{"@c.us", ""},
	}
	for _, c := range cases {
		got := ParsePhoneFromJID(c.in)
		if got != c.want {
			t.Errorf("ParsePhoneFromJID(%q)=%q want %q", c.in, got, c.want)
		}
	}
}
