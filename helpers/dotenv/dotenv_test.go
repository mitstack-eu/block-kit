package dotenv_test

import (
	"slices"
	"testing"

	"github.com/mitstack-eu/block-kit/helpers/dotenv"
)

func TestNames_ReadsEachAssignedVariableNameOnceInFileOrderAndNoValue(t *testing.T) {
	text := "# APP_NAME=commented\r\nAPP_KEY=base64:c2VjcmV0PQ==\r\nexport DB_HOST=localhost\n  MAIL_FROM = shop@example.com\nAPP_KEY=again\n1ST_RUN=yes\nWITH-DASH=no\nno assignment\n\n"

	got := dotenv.Names(text)

	if want := []string{"APP_KEY", "DB_HOST", "MAIL_FROM"}; !slices.Equal(got, want) {
		t.Errorf("Names(%q) = %q, want %q", text, got, want)
	}
}

func TestNames_ReadsNoNameFromTextWithNoAssignment(t *testing.T) {
	for _, text := range []string{"", "# only a comment\n", "APP_KEY\n"} {
		if got := dotenv.Names(text); len(got) != 0 {
			t.Errorf("Names(%q) = %q, want no name", text, got)
		}
	}
}
