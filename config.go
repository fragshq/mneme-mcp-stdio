package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// version is the server's version, reported by --version and to MCP clients.
const version = "0.1.0"

// Every setting is a flag and an environment variable: the flag name
// upper-cased, with dashes as underscores, after envPrefix (--pg-dsn is
// MNEME_PG_DSN). A flag wins over its variable, which wins over the default.
const envPrefix = "MNEME"

// Flag names.
const (
	flagStorage   = "storage"
	flagRedisAddr = "redis-addr"
	flagPGDSN     = "pg-dsn"
	flagPrefix    = "prefix"
	flagTenant    = "tenant"
	flagDashboard = "dashboard"
	flagMaxTypes  = "max-record-types"
	flagMaxRecs   = "max-records-per-type"
)

// Default quotas, per tenant.
const (
	defaultMaxRecordTypes    = 10
	defaultMaxRecordsPerType = 100
)

// envReplacer turns a flag name into the suffix of its variable.
var envReplacer = strings.NewReplacer("-", "_")

// config is the resolved configuration of one server run.
type config struct {
	// Storage is the backend: storageRedis, storageMemory,
	// storagePostgresShared or storagePostgresPerType.
	Storage string
	// RedisAddr is the Redis address, used by storageRedis.
	RedisAddr string
	// PGDSN is the Postgres URL, required by the postgres-* backends.
	PGDSN string
	// Prefix is the Redis key prefix or the Postgres table prefix; "" means
	// the backend's default.
	Prefix string
	// Tenant, when set, pins every MCP operation to this tenant; "" means
	// single-tenant.
	Tenant string
	// Dashboard is off, on or a host:port (see dashboardAddr).
	Dashboard string
	// MaxRecordTypes caps the record types of each tenant; 0 means unlimited.
	MaxRecordTypes int
	// MaxRecordsPerType caps the records of each record type, per tenant; 0
	// means unlimited.
	MaxRecordsPerType int
}

// newRootCmd returns the server's command. It resolves the configuration
// from flags and environment, then hands it to run.
func newRootCmd(run func(ctx context.Context, cfg config) error) *cobra.Command {
	v := viper.New()
	v.SetEnvPrefix(envPrefix)
	v.SetEnvKeyReplacer(envReplacer)
	v.AutomaticEnv()

	cmd := &cobra.Command{
		Use:   "mneme-mcp-stdio",
		Short: "Workbench MCP server (stdio) exposing Mneme over a storage plugin",
		Long: `mneme-mcp-stdio is a minimal workbench MCP server, speaking MCP over stdin and
stdout, that exposes every Mneme tool on top of a storage plugin chosen at
startup. It has no auth and isn't meant for production.

Every flag can also be set through the environment variable shown next to it.
A flag wins over its variable.`,
		Version:      version,
		Args:         cobra.NoArgs,
		SilenceUsage: true, // a failing run is not a usage mistake
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config{
				Storage:   v.GetString(flagStorage),
				RedisAddr: v.GetString(flagRedisAddr),
				PGDSN:     v.GetString(flagPGDSN),
				Prefix:    v.GetString(flagPrefix),
				Tenant:    v.GetString(flagTenant),
				Dashboard: v.GetString(flagDashboard),
			}
			err := error(nil)
			if cfg.MaxRecordTypes, err = quota(v, flagMaxTypes); err != nil {
				return err
			}
			if cfg.MaxRecordsPerType, err = quota(v, flagMaxRecs); err != nil {
				return err
			}
			return run(cmd.Context(), cfg)
		},
	}

	flags := cmd.Flags()
	flags.String(flagStorage, storageRedis, "storage backend: redis, memory (nothing persisted), postgres-shared (one table) or postgres-per-type (one table per record type)")
	flags.String(flagRedisAddr, "localhost:6379", "Redis address, for --storage=redis")
	flags.String(flagPGDSN, "", "Postgres URL, required by the postgres-* backends")
	flags.String(flagPrefix, "", `Redis key prefix (default "mneme-workbench") or Postgres table prefix (default "mneme_")`)
	flags.String(flagTenant, "", "run every operation as this tenant (letters, digits, '_', '.', '-'; max 64); unset, the server is single-tenant")
	flags.String(flagDashboard, "off", `web dashboard: off, on (http://`+defaultDashboardAddr+`/) or a host:port to listen on`)
	flags.Lookup(flagDashboard).NoOptDefVal = "on" // a bare --dashboard turns it on
	flags.Int(flagMaxTypes, defaultMaxRecordTypes, "maximum record types per tenant; 0 means unlimited")
	flags.Int(flagMaxRecs, defaultMaxRecordsPerType, "maximum records per record type, per tenant; 0 means unlimited")
	flags.VisitAll(func(f *pflag.Flag) {
		f.Usage += fmt.Sprintf(" [$%s]", envName(f.Name))
	})
	if err := v.BindPFlags(flags); err != nil {
		panic(err) // fails only on a nil flag set
	}
	return cmd
}

// quota reads a quota setting: a non-negative integer, 0 meaning unlimited.
// It parses the value itself because viper's GetInt turns a malformed
// variable into 0, which would silently lift the cap.
func quota(v *viper.Viper, flag string) (int, error) {
	raw := strings.TrimSpace(v.GetString(flag))
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s=%q: want a non-negative integer (0 means unlimited)", setting(flag), raw)
	}
	return n, nil
}

// envName returns the environment variable of a flag.
func envName(flag string) string {
	return envPrefix + "_" + strings.ToUpper(envReplacer.Replace(flag))
}

// setting names a flag and its variable in error messages, since clients
// set either: "--pg-dsn/MNEME_PG_DSN".
func setting(flag string) string {
	return "--" + flag + "/" + envName(flag)
}
