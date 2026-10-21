package signing

import "testing"

func TestSignVerify(t *testing.T) {
	pub, priv, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	m := Manifest{Version: "0.2.0", OS: "windows", Arch: "amd64", URL: "https://example.org/x.exe", SHA256: "ab"}
	s, err := Sign(m, priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(s, pub); err != nil {
		t.Fatal(err)
	}
	s.URL = "https://evil.example/x.exe"
	if Verify(s, pub) == nil {
		t.Fatal("tampered manifest must not verify")
	}
	otherPub, _, _ := GenerateKey()
	s.URL = m.URL
	if Verify(s, otherPub) == nil {
		t.Fatal("manifest must not verify with another key")
	}
}
