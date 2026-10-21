package config

import "testing"

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir, "https://hive.example")
	if err != nil {
		t.Fatal(err)
	}
	s := st.Get()
	if !s.IdleOnly || !s.PauseOnFullscreen || s.Autostart || s.Consent != 0 {
		t.Fatalf("unsafe defaults: %+v", s)
	}
	st.Update(func(s *Settings) { s.MaxGPUPercent = 500; s.Consent = ConsentVersion })
	st.SetIdentity(Identity{NodeID: "n1", Token: "tok"})

	st2, err := Open(dir, "https://other")
	if err != nil {
		t.Fatal(err)
	}
	if got := st2.Get(); got.MaxGPUPercent != 100 || got.Consent != ConsentVersion || got.Coordinator != "https://hive.example" {
		t.Fatalf("settings not persisted or not clamped: %+v", got)
	}
	if st2.Identity().Token != "tok" {
		t.Fatal("identity not persisted")
	}
}
