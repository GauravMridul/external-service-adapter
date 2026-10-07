package sequence_service

import "testing"

// Covers customExtractPincode, the port of the Apex fetchOfficePincode helper.
// Steps 1 and 2 mirror the Apex; step 3 (last standalone 6-digit token) only fires where the
// Apex would have returned null.

func TestCustomExtractPincode(t *testing.T) {
	ep := NewExpressionProcessor()

	cases := []struct {
		name   string
		params []string
		want   string
	}{
		// --- Step 1: trailing PIN code (Apex substring(length-6, length)) ---
		{"trailing pincode after city", []string{"Plot 5, MG Road, Bengaluru 560001"}, "560001"},
		{"trailing pincode with no space", []string{"MG Road Bengaluru560001"}, "560001"},
		{"address is only the pincode", []string{"560001"}, "560001"},
		{"trailing whitespace is trimmed first", []string{"  12 Residency Rd, Pune 411001  "}, "411001"},

		// --- Step 2: a comma part that is exactly the PIN code (Apex split(',') loop) ---
		{"pincode as its own comma part", []string{"Plot 5, MG Road, Bengaluru, 560001, India"}, "560001"},
		{"first matching comma part wins", []string{"111111, MG Road, 222222, India"}, "111111"},

		// --- Step 3: last standalone token (extension beyond the Apex) ---
		{"pincode mid-address followed by state", []string{"Plot 5, Bengaluru 560001, Karnataka, India"}, "560001"},
		{"rightmost token wins over a leading plot number", []string{"Flat 123456, Bengaluru 560001, Karnataka, India"}, "560001"},
		{"hyphen separated pincode", []string{"MG Road, Bengaluru-560001, Karnataka"}, "560001"},

		// --- No PIN code present ---
		{"no digits at all", []string{"MG Road, Bengaluru, Karnataka"}, ""},
		{"only shorter digit runs", []string{"Flat 12, Block 345, MG Road"}, ""},
		{"only longer digit runs", []string{"Ref 1234567890, MG Road"}, ""},
		{"empty input", []string{""}, ""},
		{"no params", []string{}, ""},
		{"shorter than a pincode", []string{"12345"}, ""},

		// --- Non-digit characters must not be accepted as a pincode ---
		{"six trailing chars that are not all digits", []string{"MG Road, Pune 4110A1"}, ""},
		{"decimal is not a pincode", []string{"Amount 1234.5"}, ""},

		// --- Multiple params: first one that yields a pincode wins ---
		{"falls through to the explicit pincode field", []string{"MG Road, Bengaluru", "560001"}, "560001"},
		{"address wins when it resolves", []string{"MG Road, Bengaluru 560001", "999999"}, "560001"},
		{"all params blank", []string{"", ""}, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ep.customExtractPincode(c.params); got != c.want {
				t.Fatalf("customExtractPincode(%q) = %q, want %q", c.params, got, c.want)
			}
		})
	}
}

// Guards the Apex-parity claim: for every input the Apex resolved, step 1 or step 2 must produce
// the same answer, so step 3 can never override an Apex-visible result.
func TestCustomExtractPincode_ApexParity(t *testing.T) {
	ep := NewExpressionProcessor()

	// The Apex returns the trailing 6 chars when numeric, even if an earlier comma part also
	// looks like a pincode. Step 1 runs first here too.
	if got := ep.customExtractPincode([]string{"111111, MG Road, Bengaluru 560001"}); got != "560001" {
		t.Fatalf("expected trailing pincode 560001 to win (Apex step order), got %q", got)
	}

	// The Apex comma loop takes the FIRST exact-length part, not the last.
	if got := ep.customExtractPincode([]string{"111111, 222222, India"}); got != "111111" {
		t.Fatalf("expected first matching comma part 111111, got %q", got)
	}
}

func TestIsAllASCIIDigits(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"560001", true},
		{"0", true},
		{"", false},
		{"56000A", false},
		{" 560001", false},
		{"-560001", false},
		{"5600.1", false},
		{"५६०००१", false}, // Devanagari digits: Apex isNumeric() accepts these, a PIN code must not
	}
	for _, c := range cases {
		if got := isAllASCIIDigits(c.in); got != c.want {
			t.Fatalf("isAllASCIIDigits(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
