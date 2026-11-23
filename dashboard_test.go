package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/fragshq/mneme/errs"
	"github.com/fragshq/mneme/query"
	"github.com/fragshq/mneme/schema"
)

func TestDashboardAddr(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "off": "", "0": "", "false": "",
		"on": defaultDashboardAddr, "1": defaultDashboardAddr, "TRUE": defaultDashboardAddr,
		"127.0.0.1:9000": "127.0.0.1:9000", ":9000": ":9000",
	} {
		if addr, err := dashboardAddr(in); err != nil || addr != want {
			t.Errorf("%q: got %q %v", in, addr, err)
		}
	}
	for _, bad := range []string{"maybe", "9000", "http://localhost:9000"} {
		if _, err := dashboardAddr(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

// TestDashboardTenants checks that the dashboard reaches every tenant of the
// storage, whichever tenant the MCP server is pinned to.
func TestDashboardTenants(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			ctx := context.Background()
			cfg := b.cfg(t)
			open := func(tenant string) *storage {
				t.Helper()
				c := cfg
				c.Tenant = tenant
				st, err := openStorage(ctx, c)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(st.close)
				return st
			}
			seed := func(st *storage, owner string) {
				t.Helper()
				eng := st.engine
				if _, err := eng.Define(ctx, schema.Definition{Name: "note", Schema: &schema.JSONSchema{Type: "object"}}); err != nil {
					t.Fatal(err)
				}
				if _, err := eng.Insert(ctx, "note", json.RawMessage(`{"owner":"`+owner+`"}`)); err != nil {
					t.Fatal(err)
				}
			}
			acme := open("acme")
			seed(acme, "acme")
			ts := acme.tenants
			if ts.Current() != "acme" {
				t.Fatalf("current: %q", ts.Current())
			}
			if _, _, err := ts.Engine(ctx, "bad name"); !errors.Is(err, errs.ErrInvalid) {
				t.Fatalf("invalid tenant name: %v", err)
			}
			owners := func(tenant string) string {
				t.Helper()
				c, eng, err := ts.Engine(ctx, tenant)
				if err != nil {
					t.Fatal(err)
				}
				page, err := eng.List(c, "note", query.Query{})
				if err != nil {
					t.Fatal(err)
				}
				out := []string{}
				for _, r := range page.Items {
					row := map[string]string{}
					_ = json.Unmarshal(r.Data, &row)
					out = append(out, row["owner"])
				}
				return strings.Join(out, ",")
			}
			if got := owners("acme"); got != "acme" {
				t.Fatalf("the dashboard sees the MCP tenant's data: %q", got)
			}
			if b.name == storageMemory {
				// Each process has its own memory: other servers' tenants
				// are not there.
				if names, err := ts.List(ctx); err != nil || strings.Join(names, ",") != "acme" {
					t.Fatalf("memory tenants: %v %v", names, err)
				}
				return
			}
			seed(open(""), "default")
			seed(open("globex"), "globex")
			names, err := ts.List(ctx)
			if err != nil || strings.Join(names, ",") != ",acme,globex" {
				t.Fatalf("tenants: %q %v", names, err)
			}
			if got := owners(""); got != "default" {
				t.Fatalf("default space: %q", got)
			}
			if got := owners("globex"); got != "globex" {
				t.Fatalf("globex: %q", got)
			}
		})
	}
}

// TestDashboardServes starts the real listener and fetches the API and UI.
func TestDashboardServes(t *testing.T) {
	st, err := openStorage(context.Background(), config{Storage: storageMemory})
	if err != nil {
		t.Fatal(err)
	}
	defer st.close()
	addr := freeAddr(t)
	stop := startDashboard(addr, st.tenants)
	defer stop()
	for path, want := range map[string]string{"/api/tenants": `"current":true`, "/": "Mneme workbench"} {
		res, err := http.Get("http://" + addr + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || !strings.Contains(string(body), want) {
			t.Fatalf("%s: %d %s", path, res.StatusCode, body)
		}
	}
	// A second server on the same port must not fail: it only logs.
	startDashboard(addr, st.tenants)()
}

// freeAddr returns a loopback address with a port nothing listens on.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}
