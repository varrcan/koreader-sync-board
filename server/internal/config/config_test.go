package config

import "testing"

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{Listen: ":8080", DataDir: "/data"}
	if c != want {
		t.Fatalf("got %+v, want %+v", c, want)
	}
}

func TestLoadOverrides(t *testing.T) {
	c, err := Load(env(map[string]string{
		"KSB_LISTEN":              "127.0.0.1:9000",
		"KSB_DATA_DIR":            "/tmp/ksb",
		"KSB_KOSYNC_REGISTRATION": "true",
		"KSB_KOSYNC_SELF_DELETE":  "1",
		"ADMIN_USER":              "root",
		"ADMIN_PASSWORD":          "secret",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Listen: "127.0.0.1:9000", DataDir: "/tmp/ksb",
		KosyncRegistration: true, KosyncSelfDelete: true,
		AdminUser: "root", AdminPassword: "secret",
	}
	if c != want {
		t.Fatalf("got %+v, want %+v", c, want)
	}
}

func TestLoadInvalidBool(t *testing.T) {
	if _, err := Load(env(map[string]string{"KSB_KOSYNC_REGISTRATION": "yes please"})); err == nil {
		t.Fatal("expected error")
	}
}
