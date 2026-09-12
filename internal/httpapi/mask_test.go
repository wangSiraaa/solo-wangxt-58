package httpapi

import "testing"

func TestMaskIDNumber(t *testing.T) {
	cases := map[string]string{
		"110101194901011234": "1101************34",
		"11010119490101123X": "1101************3X",
		"123456":             "******",
		"1234567":            "1234*67",
	}
	for in, want := range cases {
		if got := MaskIDNumber(in); got != want {
			t.Errorf("MaskIDNumber(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMaskPhone(t *testing.T) {
	if got := MaskPhone("13800001234"); got != "138****1234" {
		t.Errorf("MaskPhone = %q, want 138****1234", got)
	}
}

func TestRedact(t *testing.T) {
	in := "GET /x?id=110101194901011234&y=1 staff=w1"
	out := Redact(in)
	if want := "1101************34"; !contains(out, want) {
		t.Errorf("Redact(%q) = %q, want masked %q", in, out, want)
	}
	if contains(out, "110101194901011234") {
		t.Errorf("Redact leaked full id: %q", out)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
