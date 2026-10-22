package server

import "testing"

func TestShortPasswordRequiresExplicitOptIn(t *testing.T) {
	t.Setenv("MEDIASHELF_ALLOW_SHORT_PASSWORD", "")
	if a, err := New(t.TempDir(), "123456", t.TempDir()); err == nil {
		a.db.Close()
		t.Fatal("short password accepted without opt-in")
	}
	t.Setenv("MEDIASHELF_ALLOW_SHORT_PASSWORD", "true")
	a, err := New(t.TempDir(), "123456", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.db.Close()
	if r := req(t, a, "GET", "/api/v1/works", "", nil); r.Code != 401 {
		t.Fatal("short-password mode disabled authentication")
	}
	if r := req(t, a, "POST", "/api/v1/login", "", Object{"password": "wrong!"}); r.Code != 401 {
		t.Fatal("wrong password accepted")
	}
	if r := req(t, a, "POST", "/api/v1/login", "", Object{"password": "123456"}); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	for _, p := range []string{"", "12345"} {
		if a, err := New(t.TempDir(), p, t.TempDir()); err == nil {
			a.db.Close()
			t.Fatal("password shorter than six accepted")
		}
	}
}
