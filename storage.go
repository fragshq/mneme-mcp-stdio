package main

import (
	"cmp"
	"context"
	"fmt"
	"regexp"

	"github.com/fragshq/mneme/errs"
	"github.com/fragshq/mneme/store"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/fragshq/mneme"

	"mneme-mcp-stdio/internal/webserver"

	memorystore "github.com/fragshq/mneme-plugins/mneme-memory-storage"
	pgstore "github.com/fragshq/mneme-plugins/mneme-postgres-storage"
	redisstore "github.com/fragshq/mneme-plugins/mneme-redis-storage"
)

// Storage backends selectable with --storage.
const (
	storageRedis           = "redis"
	storageMemory          = "memory"
	storagePostgresShared  = "postgres-shared"
	storagePostgresPerType = "postgres-per-type"
)

// tenantRe bounds --tenant to characters that are safe in a Redis key
// and a Postgres value alike.
var tenantRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// storage is what openStorage connects.
type storage struct {
	// engine serves the one tenant the MCP server works on.
	engine *mneme.Engine
	// tenants opens every tenant of the same storage, for the dashboard.
	tenants *tenants
	// close releases the connection.
	close func()
}

// backend is one storage plugin opened twice: bare serves the default
// (single-tenant) space, named serves tenants through a TenantFunc.
type backend struct {
	bare  store.Store
	named store.Store
	// list returns the tenants holding record types, each as its values
	// (none for the bare space).
	list  func(ctx context.Context) ([][]string, error)
	close func()
}

// openStorage connects the backend named by cfg.Storage. cfg.Tenant, when
// set, pins every MCP operation to that tenant; unset, the server is
// single-tenant. The dashboard reaches every tenant.
func openStorage(ctx context.Context, cfg config) (*storage, error) {
	tenant := cfg.Tenant
	if tenant != "" && !tenantRe.MatchString(tenant) {
		return nil, fmt.Errorf("%s=%q must match %s", setting(flagTenant), tenant, tenantRe)
	}
	// The named stores' tenant, a single value: the one a dashboard request
	// picked, else the MCP server's own.
	named := func(ctx context.Context) []string {
		if t, _ := ctx.Value(dashboardTenantKey{}).(string); t != "" {
			return []string{t}
		}
		return []string{tenant}
	}
	b, err := openBackend(ctx, cfg, named)
	if err != nil {
		return nil, err
	}
	ts := &tenants{current: tenant, list: b.list, bare: newEngine(b.bare, cfg), named: newEngine(b.named, cfg)}
	eng := ts.bare
	if tenant != "" {
		eng = ts.named
	}
	return &storage{engine: eng, tenants: ts, close: b.close}, nil
}

// openBackend opens one storage plugin, bare and with tenancy.
func openBackend(ctx context.Context, cfg config, named store.TenantFunc) (backend, error) {
	kind := cfg.Storage
	switch kind {
	case storageMemory:
		bare := memorystore.New(memorystore.Options{})
		tenanted := memorystore.New(memorystore.Options{Tenant: named})
		list := func(ctx context.Context) ([][]string, error) {
			a, _ := bare.Tenants(ctx)
			b, _ := tenanted.Tenants(ctx)
			return append(a, b...), nil // Tenants.List may return any order
		}
		return backend{bare: bare, named: tenanted, list: list, close: func() {}}, nil
	case storageRedis:
		rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
		if err := rdb.Ping(ctx).Err(); err != nil {
			rdb.Close()
			return backend{}, fmt.Errorf("redis: %w", err)
		}
		prefix := cmp.Or(cfg.Prefix, "mneme-workbench")
		bare := redisstore.New(rdb, redisstore.Options{Prefix: prefix})
		tenanted := redisstore.New(rdb, redisstore.Options{Prefix: prefix, Tenant: named})
		return backend{bare: bare, named: tenanted, list: bare.Tenants, close: func() { rdb.Close() }}, nil
	case storagePostgresShared, storagePostgresPerType:
		if cfg.PGDSN == "" {
			return backend{}, fmt.Errorf("%s=%s needs %s", setting(flagStorage), kind, setting(flagPGDSN))
		}
		pool, err := pgxpool.New(ctx, cfg.PGDSN)
		if err != nil {
			return backend{}, fmt.Errorf("postgres: %w", err)
		}
		layout := pgstore.SharedTable
		if kind == storagePostgresPerType {
			layout = pgstore.TablePerType
		}
		// Postgres table names need a plain identifier, so the Redis default
		// (with a hyphen) doesn't carry over.
		opts := pgstore.Options{Layout: layout, Prefix: cmp.Or(cfg.Prefix, "mneme_")}
		bare, err := pgstore.New(ctx, pool, opts)
		if err == nil {
			opts.Tenant = named
			tenanted := (*pgstore.Store)(nil)
			if tenanted, err = pgstore.New(ctx, pool, opts); err == nil {
				return backend{bare: bare, named: tenanted, list: bare.Tenants, close: pool.Close}, nil
			}
		}
		pool.Close()
		return backend{}, fmt.Errorf("postgres: %w", err)
	}
	return backend{}, fmt.Errorf("%s=%q: want %s, %s, %s or %s", setting(flagStorage), kind, storageRedis, storageMemory, storagePostgresShared, storagePostgresPerType)
}

// newEngine builds an engine over one store, with cfg's quotas.
func newEngine(s store.Store, cfg config) *mneme.Engine {
	e, err := mneme.New(mneme.Config{
		Schemas:           s,
		Records:           s,
		Sequences:         s,
		MaxRecordTypes:    cfg.MaxRecordTypes,
		MaxRecordsPerType: cfg.MaxRecordsPerType,
	})
	if err != nil {
		panic(err) // New fails only on nil stores
	}
	return e
}

// dashboardTenantKey carries, in a dashboard request's context, the tenant
// it picked.
type dashboardTenantKey struct{}

// tenants implements webserver.Tenants over one storage.
type tenants struct {
	current string
	list    func(ctx context.Context) ([][]string, error)
	// bare serves the default space; named serves the other tenants, picked
	// from the context.
	bare  *mneme.Engine
	named *mneme.Engine
}

var _ webserver.Tenants = (*tenants)(nil)

func (t *tenants) Current() string { return t.current }

// List names each tenant by its single value ("" for the bare space). The
// workbench uses one tenancy field; a tenant with several values, written by
// another application, shows as its store.TenantKey.
func (t *tenants) List(ctx context.Context) ([]string, error) {
	all, err := t.list(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(all))
	for _, values := range all {
		out = append(out, store.TenantKey(values))
	}
	return out, nil
}

func (t *tenants) Engine(ctx context.Context, tenant string) (context.Context, *mneme.Engine, error) {
	if tenant == "" {
		return ctx, t.bare, nil
	}
	if !tenantRe.MatchString(tenant) {
		return ctx, nil, errs.Invalidf("tenant %q must match %s", tenant, tenantRe)
	}
	return context.WithValue(ctx, dashboardTenantKey{}, tenant), t.named, nil
}
