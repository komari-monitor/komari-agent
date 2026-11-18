package monitoring

import "testing"

func TestParseTemperatureValue(t *testing.T) {
	for _, tc := range []struct {
		input   string
		want    uint64
		invalid bool
	}{
		{input: "76 C", want: 76},
		{input: "76.30 C", want: 76},
		{input: "86.53 C", want: 87},
		{input: " N/A ", invalid: true},
		{input: "-0.25 C", invalid: true},
		{input: "NaN", invalid: true},
		{input: "+Inf", invalid: true},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := parseTemperatureValue(tc.input)
			if got != tc.want || (err != nil) != tc.invalid {
				t.Fatalf("got (%d, %v), want (%d, invalid=%v)", got, err, tc.want, tc.invalid)
			}
		})
	}
}
