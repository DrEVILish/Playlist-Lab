package deemix

import "testing"

// TestLegacyCryptedStreamURL_MatchesReferenceImplementation locks the
// output against deemix's own vendored JS (deemix/decryption.js's
// generateCryptedStreamURL), captured by running:
//
//	node -e "console.log(require('./deemix/decryption.js').generateCryptedStreamURL('3135556', '000790eceb6cb6732d225c0585632b31', '3', 3))"
//
// from server/node_modules/deemix in the deemix-server checkout.
func TestLegacyCryptedStreamURL_MatchesReferenceImplementation(t *testing.T) {
	want := "https://e-cdns-proxy-0.dzcdn.net/mobile/1/c5b087825da1f0af0b3c813c049ea10ddefd5ab7cc639c0fb27cf87cf2bb6d490c6d00013555e2829900f8272075210423cde619779b919b21c6bd77e47538e4de8a94308d63912851d8f3203c6e4d57"
	got, ok := legacyCryptedStreamURL("3135556", "000790eceb6cb6732d225c0585632b31", "3", "3")
	if !ok {
		t.Fatal("legacyCryptedStreamURL reported failure")
	}
	if got != want {
		t.Fatalf("legacyCryptedStreamURL(...) =\n%s\nwant\n%s", got, want)
	}
}
