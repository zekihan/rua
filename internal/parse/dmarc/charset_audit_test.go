package dmarc

import (
	"reflect"
	"strings"
	"testing"
)

func TestAggregateDeclaredLatin1Encoding(t *testing.T) {
	base := string(fixture(t, "dmarc", "latin1.xml"))
	utf8XML := strings.Replace(base, "ISO-8859-1", "UTF-8", 1)
	utf8XML = strings.Replace(utf8XML, "Caf\xe9", "Café €", 1)

	want, err := ParseAggregate([]byte(utf8XML))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		encoding string
		org      string
	}{
		{encoding: "ISO-8859-1", org: "Caf\xe9 &#8364;"},
		{encoding: "windows-1252", org: "Caf\xe9 \x80"},
		{encoding: "us-ascii", org: "Caf&#233; &#8364;"},
	}
	for _, tt := range tests {
		t.Run(tt.encoding, func(t *testing.T) {
			input := strings.Replace(base, "ISO-8859-1", tt.encoding, 1)
			input = strings.Replace(input, "Caf\xe9", tt.org, 1)

			got, err := ParseAggregate([]byte(input))
			if err != nil {
				t.Fatal(err)
			}

			if !reflect.DeepEqual(got, want) {
				t.Fatalf("decoded aggregate = %+v, want %+v", got, want)
			}
		})
	}
}
