package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/fragshq/mneme/query"
	"github.com/fragshq/mneme/schema"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fragshq/mneme"
)

// isolatedPG returns a DSN whose search_path is a fresh, empty schema, or
// skips the test when MNEME_PG_DSN is not set.
func isolatedPG(t *testing.T) string {
	t.Helper()
	base := os.Getenv("MNEME_PG_DSN")
	if base == "" {
		t.Skip("MNEME_PG_DSN not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("mneme_mcp_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(ctx, "DROP SCHEMA "+name+" CASCADE")
		admin.Close()
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", name)
	u.RawQuery = q.Encode()
	return u.String()
}

// TestToolsEndToEnd drives every tool through a real MCP client session, on
// every backend. Redis runs on miniredis; Postgres needs MNEME_PG_DSN.
func TestToolsEndToEnd(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			st, err := openStorage(context.Background(), b.cfg(t))
			if err != nil {
				t.Fatal(err)
			}
			defer st.close()
			testTools(t, st.engine)
		})
	}
}

// backends lists every storage backend with a function returning the
// configuration of an empty instance of it.
var backends = []struct {
	name string
	cfg  func(t *testing.T) config
}{
	{storageRedis, func(t *testing.T) config {
		return config{Storage: storageRedis, RedisAddr: miniredis.RunT(t).Addr()}
	}},
	{storageMemory, func(t *testing.T) config {
		return config{Storage: storageMemory}
	}},
	{storagePostgresShared, func(t *testing.T) config {
		return config{Storage: storagePostgresShared, PGDSN: isolatedPG(t)}
	}},
	{storagePostgresPerType, func(t *testing.T) config {
		return config{Storage: storagePostgresPerType, PGDSN: isolatedPG(t)}
	}},
}

// TestTenant checks that --tenant pins the server to one tenant: each
// tenant, and the single-tenant default, sees only its own record types and records,
// even on the same database and prefix.
func TestTenant(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			cfg := b.cfg(t)
			open := func(tenant string) *mneme.Engine {
				t.Helper()
				c := cfg
				c.Tenant = tenant
				st, err := openStorage(context.Background(), c)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(st.close)
				return st.engine
			}
			ctx := context.Background()
			engines := map[string]*mneme.Engine{"": open(""), "acme": open("acme"), "globex": open("globex")}
			for tenant, eng := range engines {
				if _, err := eng.Define(ctx, schema.Definition{Name: "note", Schema: &schema.JSONSchema{Type: "object"}}); err != nil {
					t.Fatalf("%q: %v", tenant, err)
				}
				if _, err := eng.Insert(ctx, "note", json.RawMessage(fmt.Sprintf(`{"owner":%q}`, tenant))); err != nil {
					t.Fatalf("%q: %v", tenant, err)
				}
			}
			if _, err := engines["acme"].Insert(ctx, "note", json.RawMessage(`{"owner":"acme"}`)); err != nil {
				t.Fatal(err)
			}
			for tenant, eng := range engines {
				vs, err := eng.ListVersions(ctx, "note")
				if err != nil || len(vs) != 1 {
					t.Fatalf("%q: each tenant has its own single version, got %d %v", tenant, len(vs), err)
				}
				page, err := eng.List(ctx, "note", query.Query{})
				if err != nil {
					t.Fatal(err)
				}
				want := map[string]int{"": 1, "acme": 2, "globex": 1}[tenant]
				if len(page.Items) != want {
					t.Fatalf("%q: want %d rows, got %d", tenant, want, len(page.Items))
				}
				for _, r := range page.Items {
					row := map[string]string{}
					if err := json.Unmarshal(r.Data, &row); err != nil || row["owner"] != tenant {
						t.Fatalf("%q sees another tenant's row: %s", tenant, r.Data)
					}
				}
			}
		})
	}
}

func TestOpenStorageErrors(t *testing.T) {
	for _, cfg := range []config{
		{Storage: "mongo"},
		{Storage: storageMemory, Tenant: "acme}:x"},
		{Storage: storageMemory, Tenant: "has space"},
		{Storage: storagePostgresShared},
		{Storage: storagePostgresPerType, PGDSN: "postgres://u:p@127.0.0.1:1/db?connect_timeout=1"},
	} {
		if _, err := openStorage(context.Background(), cfg); err == nil {
			t.Fatalf("%+v: want an error", cfg)
		}
	}
}

func testTools(t *testing.T, eng *mneme.Engine) {
	ctx := context.Background()
	server := newServer(&mcp.Implementation{Name: "test", Version: "0"}, eng)
	st1, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	call := func(name string, args string) (string, bool) {
		t.Helper()
		a := map[string]any{}
		if err := json.Unmarshal([]byte(args), &a); err != nil {
			t.Fatal(err)
		}
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: a})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		text := ""
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				text += tc.Text
			}
		}
		checkStructured(t, name, text, res)
		return text, res.IsError
	}
	mustOK := func(name string, args string) string {
		t.Helper()
		out, isErr := call(name, args)
		if isErr {
			t.Fatalf("%s returned an error: %s", name, out)
		}
		return out
	}

	if got := cs.InitializeResult().Instructions; got == "" || !strings.Contains(got, "count_records") {
		t.Fatalf("server instructions: %q", got)
	}

	tools, err := cs.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 13 {
		t.Fatalf("want 13 tools, got %d (%v)", len(tools.Tools), err)
	}

	mustOK("define_record_type", `{"name":"risk","title":"Risk",
		"schema":{"type":"object","properties":{"title":{"type":"string"}},"required":["title"]},
		"id_strategy":{"segments":[{"fixed":"RID-"},{"sequence":{"name":"rid","pad":4}}]}}`)
	if out := mustOK("list_record_types", `{}`); !strings.Contains(out, `"risk"`) {
		t.Fatalf("list_record_types: %s", out)
	}
	if out := mustOK("insert_record", `{"record_type":"risk","data":{"title":"flood"},"labels":["water","urgent"]}`); !strings.Contains(out, `"RID-0001"`) || !strings.Contains(out, `"urgent"`) {
		t.Fatalf("insert_record: %s", out)
	}
	if out, isErr := call("insert_record", `{"record_type":"risk","data":{"nope":1}}`); !isErr || !strings.Contains(out, "title") {
		t.Fatalf("invalid record should be a tool error mentioning the missing field, got %v: %s", isErr, out)
	}
	mustOK("define_record_type", `{"name":"risk",
		"schema":{"type":"object","properties":{"title":{"type":"string"},"severity":{"type":"integer"}},"required":["title","severity"]},
		"id_strategy":{"segments":[{"fixed":"RID-"},{"sequence":{"name":"rid","pad":4}}]}}`)
	mustOK("insert_record", `{"record_type":"risk","data":{"title":"fire","severity":4}}`)

	sch := mustOK("get_record_type", `{"name":"risk"}`)
	if !strings.Contains(sch, `"severity"`) || !strings.Contains(sch, `"version": 2`) {
		t.Fatalf("get_record_type latest: %s", sch)
	}
	if sch := mustOK("get_record_type", `{"name":"risk","version":1}`); strings.Contains(sch, `"required": [
          "title",
          "severity"`) {
		t.Fatalf("version 1 should not require severity: %s", sch)
	}

	list := mustOK("list_records", `{"record_type":"risk","filter":{"op":"not","filters":[{"op":"exists","field":"/severity"}]}}`)
	if !strings.Contains(list, "flood") || strings.Contains(list, "fire") {
		t.Fatalf("list_records filter: %s", list)
	}
	page := mustOK("list_records", `{"record_type":"risk","limit":1,"sort":[{"field":"$id","desc":true}]}`)
	if !strings.Contains(page, "RID-0002") || !strings.Contains(page, "nextCursor") {
		t.Fatalf("list_records page: %s", page)
	}
	if out := mustOK("get_record", `{"record_type":"risk","id":"RID-0001"}`); !strings.Contains(out, `"schemaVersion": 1`) {
		t.Fatalf("get_record: %s", out)
	}
	if _, isErr := call("get_record", `{"record_type":"risk","id":"RID-9999"}`); !isErr {
		t.Fatal("missing record should be a tool error")
	}

	// update: full replace against the latest schema migrates RID-0001 to v2
	if out, isErr := call("update_record", `{"record_type":"risk","id":"RID-0001","data":{"title":"flood"}}`); !isErr || !strings.Contains(out, "severity") {
		t.Fatalf("update with the v1 shape should fail on severity, got %v: %s", isErr, out)
	}
	up := mustOK("update_record", `{"record_type":"risk","id":"RID-0001","data":{"title":"flood","severity":3}}`)
	if !strings.Contains(up, `"schemaVersion": 2`) || !strings.Contains(up, `"RID-0001"`) {
		t.Fatalf("update_record: %s", up)
	}
	if out := mustOK("list_records", `{"record_type":"risk","filter":{"op":"eq","field":"/severity","value":3}}`); !strings.Contains(out, "RID-0001") {
		t.Fatalf("updated record not found by its new data: %s", out)
	}
	if out := mustOK("list_records", `{"record_type":"risk","filter":{"op":"contains","field":"$labels","value":"urgent"}}`); strings.Contains(out, "RID-0001") {
		t.Fatalf("update without labels must clear them: %s", out)
	}
	mustOK("update_record", `{"record_type":"risk","id":"RID-0001","data":{"title":"flood","severity":3},"labels":["reviewed"]}`)
	if out := mustOK("list_records", `{"record_type":"risk","filter":{"op":"contains","field":"$labels","value":"reviewed"}}`); !strings.Contains(out, "RID-0001") || strings.Contains(out, "RID-0002") {
		t.Fatalf("filter by label: %s", out)
	}
	mustOK("update_record", `{"record_type":"risk","id":"RID-0002","data":{"title":"fire","severity":4},"labels":["water","reviewed"]}`)
	compact := func(s string) string { return strings.Join(strings.Fields(s), "") }
	if out := compact(mustOK("list_labels", `{"record_type":"risk"}`)); out != `{"labels":[{"label":"reviewed","count":2},{"label":"water","count":1}]}` {
		t.Fatalf("list_labels: %s", out)
	}
	if out := compact(mustOK("list_labels", `{"record_type":"risk","filter":{"op":"eq","field":"severity","value":3}}`)); out != `{"labels":[{"label":"reviewed","count":1}]}` {
		t.Fatalf("list_labels filtered: %s", out)
	}

	// run_script: a join-free aggregate, the console, and errors
	scriptArgs := func(code string) string {
		raw, _ := json.Marshal(map[string]string{"code": code})
		return string(raw)
	}
	out := mustOK("run_script", scriptArgs(`const rows = list("risk", {sort: "-severity"})
console.log("rows:", rows.length)
return {
  titles: rows.map(r => r.data.title),
  unrated: rows.filter(r => r.data.severity === undefined).length,
  versions: [...new Set(rows.map(r => r.schemaVersion))].sort(),
  missing: get("risk", "RID-9999")
}`))
	for _, want := range []string{`"titles": [`, `"fire"`, `"flood"`, `"unrated": 0`, `"versions": [`, `"missing": null`, `"rows: 2"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("run_script lacks %s: %s", want, out)
		}
	}
	if out := mustOK("run_script", scriptArgs(`describe("risk").versions.length`)); !strings.Contains(out, `"value": 2`) {
		t.Fatalf("run_script describe: %s", out)
	}
	if out, isErr := call("run_script", scriptArgs(`console.log("before")
list("ghost")`)); !isErr || !strings.Contains(out, "ghost") || !strings.Contains(out, "before") {
		t.Fatalf("run_script error should be a tool error with the logs, got %v: %s", isErr, out)
	}
	if out, isErr := call("run_script", scriptArgs(`return (`)); !isErr || !strings.Contains(out, "syntax error") {
		t.Fatalf("run_script syntax error: %v: %s", isErr, out)
	}
	if out, isErr := call("run_script", scriptArgs(`typeof insert`)); isErr || !strings.Contains(out, `"undefined"`) {
		t.Fatalf("run_script must have no write functions: %s", out)
	}

	// delete
	mustOK("delete_record", `{"record_type":"risk","id":"RID-0002"}`)
	if _, isErr := call("get_record", `{"record_type":"risk","id":"RID-0002"}`); !isErr {
		t.Fatal("deleted row should be gone")
	}
	if _, isErr := call("delete_record", `{"record_type":"risk","id":"RID-0002"}`); !isErr {
		t.Fatal("deleting twice should be a tool error")
	}
}

// checkStructured asserts that a successful result carries the same JSON
// object as its text in structuredContent, and that an error carries none.
func checkStructured(t *testing.T, name string, text string, res *mcp.CallToolResult) {
	t.Helper()
	if res.IsError {
		if res.StructuredContent != nil {
			t.Fatalf("%s: a tool error must have no structuredContent: %v", name, res.StructuredContent)
		}
		return
	}
	want := any(nil)
	if err := json.Unmarshal([]byte(text), &want); err != nil {
		t.Fatalf("%s: text is not JSON: %v", name, err)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	got := any(nil)
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: structuredContent %s does not match the text %s", name, raw, text)
	}
}

// TestQuotasPerTenant checks that the quotas count each tenant on its own:
// one tenant at its limits doesn't stop another, on the same database.
func TestQuotasPerTenant(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			ctx := context.Background()
			cfg := b.cfg(t)
			cfg.MaxRecordTypes = 1
			cfg.MaxRecordsPerType = 1
			cfg.Tenant = "acme"
			st, err := openStorage(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer st.close()
			define := func(eng *mneme.Engine, c context.Context, name string) error {
				_, err := eng.Define(c, schema.Definition{Name: name, Schema: &schema.JSONSchema{Type: "object"}})
				return err
			}
			insert := func(eng *mneme.Engine, c context.Context) error {
				_, err := eng.Insert(c, "note", json.RawMessage(`{}`))
				return err
			}
			// acme, the MCP server's tenant, fills both quotas.
			if err := define(st.engine, ctx, "note"); err != nil {
				t.Fatal(err)
			}
			if err := define(st.engine, ctx, "other"); !errors.Is(err, mneme.ErrQuota) {
				t.Fatalf("acme's second record type: want ErrQuota, got %v", err)
			}
			if err := insert(st.engine, ctx); err != nil {
				t.Fatal(err)
			}
			if err := insert(st.engine, ctx); !errors.Is(err, mneme.ErrQuota) {
				t.Fatalf("acme's second record: want ErrQuota, got %v", err)
			}
			// globex, reached as the dashboard does, still has room.
			c, globex, err := st.tenants.Engine(ctx, "globex")
			if err != nil {
				t.Fatal(err)
			}
			if err := define(globex, c, "note"); err != nil {
				t.Fatalf("globex has its own record type quota: %v", err)
			}
			if err := insert(globex, c); err != nil {
				t.Fatalf("globex has its own record quota: %v", err)
			}
		})
	}
}
