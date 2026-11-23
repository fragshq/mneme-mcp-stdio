package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

// resolve runs the root command with args and returns the configuration it
// would serve with.
func resolve(t *testing.T, args ...string) config {
	t.Helper()
	got := config{}
	cmd := newRootCmd(func(ctx context.Context, cfg config) error {
		got = cfg
		return nil
	})
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	return got
}

// clearEnv unsets every setting's variable for the test, so the developer's
// environment can't leak in.
func clearEnv(t *testing.T) {
	t.Helper()
	newRootCmd(nil).Flags().VisitAll(func(f *pflag.Flag) {
		t.Setenv(envName(f.Name), "")
	})
}

func TestConfigDefaults(t *testing.T) {
	clearEnv(t)
	want := config{Storage: storageRedis, RedisAddr: "localhost:6379", Dashboard: "off", MaxRecordTypes: 10, MaxRecordsPerType: 100}
	if got := resolve(t); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestConfigPrecedence checks that each setting comes from its variable, and
// that a flag wins over it.
func TestConfigPrecedence(t *testing.T) {
	clearEnv(t)
	t.Setenv("MNEME_STORAGE", storagePostgresShared)
	t.Setenv("MNEME_REDIS_ADDR", "env:1")
	t.Setenv("MNEME_PG_DSN", "postgres://env")
	t.Setenv("MNEME_PREFIX", "env_")
	t.Setenv("MNEME_TENANT", "env-tenant")
	t.Setenv("MNEME_DASHBOARD", "on")
	t.Setenv("MNEME_MAX_RECORD_TYPES", "3")
	t.Setenv("MNEME_MAX_RECORDS_PER_TYPE", "0")

	fromEnv := config{Storage: storagePostgresShared, RedisAddr: "env:1", PGDSN: "postgres://env", Prefix: "env_", Tenant: "env-tenant", Dashboard: "on", MaxRecordTypes: 3, MaxRecordsPerType: 0}
	if got := resolve(t); got != fromEnv {
		t.Fatalf("from env: got %+v, want %+v", got, fromEnv)
	}

	fromFlags := config{Storage: storageMemory, RedisAddr: "flag:1", PGDSN: "postgres://flag", Prefix: "flag_", Tenant: "flag-tenant", Dashboard: "127.0.0.1:9000", MaxRecordTypes: 5, MaxRecordsPerType: 50}
	got := resolve(t,
		"--storage", storageMemory, "--redis-addr", "flag:1", "--pg-dsn", "postgres://flag",
		"--prefix", "flag_", "--tenant", "flag-tenant", "--dashboard=127.0.0.1:9000",
		"--max-record-types", "5", "--max-records-per-type", "50")
	if got != fromFlags {
		t.Fatalf("from flags: got %+v, want %+v", got, fromFlags)
	}

	mixed := fromEnv
	mixed.Tenant = "acme"
	if got := resolve(t, "--tenant", "acme"); got != mixed {
		t.Fatalf("one flag over env: got %+v, want %+v", got, mixed)
	}
}

// TestConfigBareDashboard checks that --dashboard without a value turns the
// dashboard on.
func TestConfigBareDashboard(t *testing.T) {
	clearEnv(t)
	if got := resolve(t, "--dashboard").Dashboard; got != "on" {
		t.Fatalf("got %q", got)
	}
}

// TestConfigHelpNamesVariables checks that --help shows every flag with its
// variable, and that it doesn't start the server.
func TestConfigHelpNamesVariables(t *testing.T) {
	cmd := newRootCmd(func(ctx context.Context, cfg config) error {
		t.Fatal("--help must not serve")
		return nil
	})
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{flagStorage, flagRedisAddr, flagPGDSN, flagPrefix, flagTenant, flagDashboard, flagMaxTypes, flagMaxRecs} {
		if !strings.Contains(out.String(), "--"+f) || !strings.Contains(out.String(), "$"+envName(f)) {
			t.Errorf("help lacks --%s or $%s:\n%s", f, envName(f), out)
		}
	}
}

func TestConfigRejectsArgs(t *testing.T) {
	cmd := newRootCmd(func(ctx context.Context, cfg config) error { return nil })
	cmd.SetArgs([]string{"serve"})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("want an error for a positional argument")
	}
}

// TestConfigRejectsBadQuotas checks that a malformed or negative quota is an
// error, never a silent 0 (which would mean unlimited).
func TestConfigRejectsBadQuotas(t *testing.T) {
	for _, bad := range []string{"lots", "-1", "1.5"} {
		clearEnv(t)
		t.Setenv("MNEME_MAX_RECORDS_PER_TYPE", bad)
		cmd := newRootCmd(func(ctx context.Context, cfg config) error {
			t.Fatalf("%q: must not serve", bad)
			return nil
		})
		cmd.SetArgs(nil)
		cmd.SetErr(&bytes.Buffer{})
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "MNEME_MAX_RECORDS_PER_TYPE") {
			t.Fatalf("%q: want an error naming the variable, got %v", bad, err)
		}
	}
	clearEnv(t)
	cmd := newRootCmd(func(ctx context.Context, cfg config) error { return nil })
	cmd.SetArgs([]string{"--max-record-types", "-2"})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("a negative flag: want an error")
	}
}
