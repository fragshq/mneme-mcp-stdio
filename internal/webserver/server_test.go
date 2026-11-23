package webserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/fragshq/mneme"
	"github.com/fragshq/mneme/errs"
	"github.com/fragshq/mneme/idgen"
	"github.com/fragshq/mneme/jsonx"
	"github.com/fragshq/mneme/schema"
	"github.com/fragshq/mneme/store/memstore"

	"mneme-mcp-stdio/internal/webserver"
)

// fakeTenants serves one memstore engine per tenant name.
type fakeTenants struct {
	current string
	engines map[string]*mneme.Engine
}

func (f *fakeTenants) List(context.Context) ([]string, error) {
	out := []string{}
	for t := range f.engines {
		out = append(out, t)
	}
	return out, nil
}

func (f *fakeTenants) Current() string { return f.current }

func (f *fakeTenants) Engine(ctx context.Context, tenant string) (context.Context, *mneme.Engine, error) {
	e, ok := f.engines[tenant]
	if !ok {
		return ctx, nil, fmt.Errorf("%w: tenant %q", errs.ErrNotFound, tenant)
	}
	return ctx, e, nil
}

func newEngine(t *testing.T) *mneme.Engine {
	t.Helper()
	m := memstore.New()
	e, err := mneme.New(mneme.Config{Schemas: m, Records: m, Sequences: m})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// fixture defines "task" (two versions) in the default space and returns
// the dashboard handler.
func fixture(t *testing.T) (http.Handler, *fakeTenants) {
	t.Helper()
	ctx := context.Background()
	def := newEngine(t)
	str := &schema.JSONSchema{Type: "string"}
	if _, err := def.Define(ctx, schema.Definition{Name: "task", Schema: &schema.JSONSchema{Type: "object",
		Properties: map[string]*schema.JSONSchema{"title": str}, Required: []string{"title"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := def.Define(ctx, schema.Definition{Name: "task", Title: "Task",
		IDStrategy: idgen.NewSpec(idgen.Fixed("T-"), idgen.Sequence(idgen.SequenceOptions{Name: "t"})),
		Schema: &schema.JSONSchema{Type: "object",
			Properties: map[string]*schema.JSONSchema{"title": str, "qty": {Type: "integer"},
				"owner": {Type: "object", Properties: map[string]*schema.JSONSchema{"id": str}, Required: []string{"id"}}},
			Required: []string{"title", "qty"}}}); err != nil {
		t.Fatal(err)
	}
	ts := &fakeTenants{current: "", engines: map[string]*mneme.Engine{"": def, "acme": newEngine(t)}}
	return webserver.New(webserver.Options{Tenants: ts}), ts
}

type response struct {
	status int
	body   string
	header http.Header
}

func do(t *testing.T, h http.Handler, method string, target string, body string) response {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	raw, _ := io.ReadAll(rec.Result().Body)
	return response{status: rec.Code, body: string(raw), header: rec.Header()}
}

func decode[T any](t *testing.T, r response) T {
	t.Helper()
	out := *new(T)
	if err := json.Unmarshal([]byte(r.body), &out); err != nil {
		t.Fatalf("decoding %s: %v", r.body, err)
	}
	return out
}

func TestTenants(t *testing.T) {
	h, _ := fixture(t)
	r := do(t, h, "GET", "/api/tenants", "")
	if r.status != 200 || r.body != `{"tenants":[{"current":true,"name":""},{"current":false,"name":"acme"}]}`+"\n" {
		t.Fatalf("%d %s", r.status, r.body)
	}
}

func TestRecordType(t *testing.T) {
	h, _ := fixture(t)
	if r := do(t, h, "GET", "/api/record-types", ""); r.status != 200 || !strings.Contains(r.body, `"latestVersion":2`) || !strings.Contains(r.body, `"title":"Task"`) {
		t.Fatalf("list: %d %s", r.status, r.body)
	}
	if r := do(t, h, "GET", "/api/record-types?tenant=acme", ""); r.status != 200 || r.body != "[]\n" {
		t.Fatalf("another tenant has no types: %d %s", r.status, r.body)
	}
	d := decode[webserver.RecordTypeDetails](t, do(t, h, "GET", "/api/record-types/task", ""))
	if d.Definition.Version != 2 || fmt.Sprint(d.Versions) != "[1 2]" || d.Definition.IDStrategy == nil || d.Definition.Schema.Type != "object" {
		t.Fatalf("latest: %+v", d)
	}
	if d.Definition.Indexes == nil {
		t.Fatal("indexes must be [] rather than null")
	}
	paths := []string{}
	for _, f := range d.Fields {
		paths = append(paths, f.Path)
		if f.Types == nil || f.Versions == nil {
			t.Fatalf("field arrays must not be null: %+v", f)
		}
	}
	if strings.Join(paths, ",") != "/owner,/owner/id,/qty,/title" {
		t.Fatalf("fields across versions: %v", paths)
	}
	v1 := decode[webserver.RecordTypeDetails](t, do(t, h, "GET", "/api/record-types/task?version=1", ""))
	if v1.Definition.Version != 1 || v1.Definition.IDStrategy != nil {
		t.Fatalf("v1 has the default ID strategy: %+v", v1.Definition)
	}
	if r := do(t, h, "GET", "/api/record-types/ghost", ""); r.status != 404 || !strings.Contains(r.body, `"error"`) {
		t.Fatalf("unknown type: %d %s", r.status, r.body)
	}
}

func TestRecordLifecycle(t *testing.T) {
	h, _ := fixture(t)
	r := do(t, h, "POST", "/api/record-types/task/records", `{"data":{"title":"paint","qty":3},"labels":["urgent","home"]}`)
	if r.status != 201 {
		t.Fatalf("insert: %d %s", r.status, r.body)
	}
	rec := decode[webserver.Record](t, r)
	if rec.ID != "T-1" || rec.RecordType != "task" || rec.SchemaVersion != 2 || !jsonx.Equal(rec.Data, json.RawMessage(`{"title":"paint","qty":3}`)) ||
		strings.Join(rec.Labels, ",") != "home,urgent" || rec.CreatedAt.IsZero() {
		t.Fatalf("inserted: %+v", rec)
	}
	do(t, h, "POST", "/api/record-types/task/records", `{"data":{"title":"mow","qty":10}}`)

	if r := do(t, h, "GET", "/api/record-types/task/records/T-1", ""); r.status != 200 || !strings.Contains(r.body, `"id":"T-1"`) {
		t.Fatalf("get: %d %s", r.status, r.body)
	}
	page := decode[webserver.RecordPage](t, do(t, h, "GET", "/api/record-types/task/records?sort=-/qty", ""))
	if len(page.Items) != 2 || page.Items[0].ID != "T-2" || page.NextCursor != "" {
		t.Fatalf("sorted list: %+v", page)
	}
	page = decode[webserver.RecordPage](t, do(t, h, "GET", "/api/record-types/task/records?limit=1", ""))
	if len(page.Items) != 1 || page.NextCursor == "" || page.Limit != 1 || page.Total != 2 || !page.HasMore {
		t.Fatalf("paged list: %+v", page)
	}
	next := decode[webserver.RecordPage](t, do(t, h, "GET", "/api/record-types/task/records?limit=1&cursor="+url.QueryEscape(page.NextCursor), ""))
	if len(next.Items) != 1 || next.Items[0].ID != "T-2" {
		t.Fatalf("next page: %+v", next)
	}
	filter := url.QueryEscape(`{"op":"contains","field":"$labels","value":"urgent"}`)
	if page := decode[webserver.RecordPage](t, do(t, h, "GET", "/api/record-types/task/records?filter="+filter, "")); len(page.Items) != 1 || page.Items[0].ID != "T-1" {
		t.Fatalf("filtered list: %+v", page)
	}
	if r := do(t, h, "GET", "/api/record-types/task/labels", ""); r.body != `[{"count":1,"label":"home"},{"count":1,"label":"urgent"}]`+"\n" {
		t.Fatalf("labels: %s", r.body)
	}
	if r := do(t, h, "GET", "/api/record-types/task/labels?filter="+url.QueryEscape(`{"op":"gt","field":"qty","value":5}`), ""); r.body != "[]\n" {
		t.Fatalf("filtered labels: %s", r.body)
	}

	r = do(t, h, "PUT", "/api/record-types/task/records/T-1", `{"data":{"title":"paint walls","qty":4}}`)
	if up := decode[webserver.Record](t, r); r.status != 200 || !jsonx.Equal(up.Data, json.RawMessage(`{"title":"paint walls","qty":4}`)) || len(up.Labels) != 0 || up.ID != "T-1" {
		t.Fatalf("update is a full replace, labels included: %d %s", r.status, r.body)
	}
	if r := do(t, h, "DELETE", "/api/record-types/task/records/T-1", ""); r.status != 204 {
		t.Fatalf("delete: %d %s", r.status, r.body)
	}
	if r := do(t, h, "GET", "/api/record-types/task/records/T-1", ""); r.status != 404 {
		t.Fatalf("deleted: %d %s", r.status, r.body)
	}
	if r := do(t, h, "DELETE", "/api/record-types/task/records/T-1", ""); r.status != 404 {
		t.Fatalf("delete twice: %d %s", r.status, r.body)
	}
}

func TestValidationIssues(t *testing.T) {
	h, _ := fixture(t)
	r := do(t, h, "POST", "/api/record-types/task/records", `{"data":{"title":7,"owner":{}}}`)
	if r.status != 400 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	e := decode[webserver.Error](t, r)
	if len(e.Issues) == 0 {
		t.Fatalf("issues expected: %s", r.body)
	}
	paths := map[string]bool{}
	for _, i := range e.Issues {
		paths[i.Path] = true
	}
	for _, want := range []string{"/title", "", "/owner"} {
		if !paths[want] {
			t.Fatalf("missing an issue at %q: %s", want, r.body)
		}
	}
}

func TestBadRequests(t *testing.T) {
	h, _ := fixture(t)
	cases := []struct {
		method, target, body string
		status               int
	}{
		{"GET", "/api/record-types/task/records?filter=" + url.QueryEscape(`{"op":"nope"}`), "", 400},
		{"GET", "/api/record-types/task/records?filter=notjson", "", 400},
		{"GET", "/api/record-types/task/records?limit=ten", "", 400},
		{"GET", "/api/record-types/task/records?sort=" + url.QueryEscape("/a,/a"), "", 400},
		{"GET", "/api/record-types/task/records?tenant=initech", "", 404},
		{"POST", "/api/record-types/task/records", `{"labels":["x"]}`, 400},
		{"POST", "/api/record-types/task/records", `{"data":{"title":"x","qty":1},"labels":[""]}`, 400},
		{"PUT", "/api/record-types/task/records/T-9", `{"data":{"title":"x","qty":1}}`, 404},
	}
	for _, c := range cases {
		if r := do(t, h, c.method, c.target, c.body); r.status != c.status || !strings.Contains(r.body, `"error"`) {
			t.Errorf("%s %s %s: want %d, got %d %s", c.method, c.target, c.body, c.status, r.status, r.body)
		}
	}
}

// Writes need a JSON body, so cross-site HTML forms can't reach them.
func TestWritesRequireJSON(t *testing.T) {
	h, _ := fixture(t)
	req := httptest.NewRequest("POST", "/api/record-types/task/records", strings.NewReader(`{"data":{"title":"x","qty":1}}`))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain write: %d", rec.Code)
	}
	if page := decode[webserver.RecordPage](t, do(t, h, "GET", "/api/record-types/task/records", "")); len(page.Items) != 0 {
		t.Fatal("the rejected write must store nothing")
	}
}

func TestLoopbackOnly(t *testing.T) {
	_, ts := fixture(t)
	h := webserver.New(webserver.Options{Tenants: ts, LoopbackOnly: true})
	for host, want := range map[string]int{
		"127.0.0.1:47080": 200, "localhost:47080": 200, "[::1]:47080": 200, "LOCALHOST": 200,
		"evil.example:47080": 403, "192.168.1.5:47080": 403,
	} {
		req := httptest.NewRequest("GET", "/api/tenants", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Host %s: want %d, got %d", host, want, rec.Code)
		}
	}
}

func TestUI(t *testing.T) {
	h, _ := fixture(t)
	for path, want := range map[string]string{
		"/":          "<title>Mneme workbench</title>",
		"/app.js":    "import { buildForm",
		"/form.js":   "export function buildForm",
		"/dom.js":    "export function h(",
		"/style.css": ":root",
	} {
		r := do(t, h, "GET", path, "")
		if r.status != 200 || !strings.Contains(r.body, want) {
			t.Errorf("%s: %d, missing %q", path, r.status, want)
		}
	}
	if ct := do(t, h, "GET", "/app.js", "").header.Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("app.js must be served as JavaScript (module scripts require it), got %q", ct)
	}
}

func TestDefineAndDeleteRecordType(t *testing.T) {
	h, _ := fixture(t)
	// A new record type in another tenant, then a second version of it.
	r := do(t, h, "POST", "/api/record-types?tenant=acme", `{"name":"invoice","title":"Invoice",
		"schema":{"type":"object","properties":{"total":{"type":"number"}},"required":["total"]},
		"idStrategy":{"segments":[{"fixed":"INV-"},{"sequence":{"name":"inv","pad":4}}]},"indexes":["total"]}`)
	if r.status != 201 {
		t.Fatalf("define: %d %s", r.status, r.body)
	}
	d := decode[webserver.Definition](t, r)
	if d.Name != "invoice" || d.Version != 1 || d.Title != "Invoice" || d.IDStrategy == nil || fmt.Sprint(d.Indexes) != "[total]" {
		t.Fatalf("defined: %+v", d)
	}
	r = do(t, h, "POST", "/api/record-types?tenant=acme", `{"name":"invoice","schema":{"type":"object"}}`)
	if d := decode[webserver.Definition](t, r); r.status != 201 || d.Version != 2 || d.IDStrategy != nil || d.Indexes == nil {
		t.Fatalf("redefine: %d %s", r.status, r.body)
	}
	if r := do(t, h, "GET", "/api/record-types", ""); strings.Contains(r.body, "invoice") {
		t.Fatalf("the default space must not see acme's type: %s", r.body)
	}
	if r := do(t, h, "POST", "/api/record-types/invoice/records?tenant=acme", `{"data":{"anything":true}}`); r.status != 201 {
		t.Fatalf("insert against v2: %d %s", r.status, r.body)
	}

	// Validation stores nothing.
	if r := do(t, h, "POST", "/api/record-types/validate", `{"name":"draft","schema":{"type":"object"}}`); r.status != 200 || r.body != `{"valid":true}`+"\n" {
		t.Fatalf("validate: %d %s", r.status, r.body)
	}
	if r := do(t, h, "GET", "/api/record-types/draft", ""); r.status != 404 {
		t.Fatalf("validate must not store: %d", r.status)
	}
	for _, bad := range []string{
		`{"name":"Bad Name","schema":{"type":"object"}}`,
		`{"name":"x","schema":{"type":"string"}}`,
		`{"name":"x","schema":{"type":"object","properties":{"a":{"type":"nope"}}}}`,
		`{"name":"x","schema":{"type":"object"},"idStrategy":{"segments":[{"bogus":"y"}]}}`,
		`{"name":"x","schema":{"type":"object"},"idStrategy":{"segmentz":[]}}`,
		`{"name":"x"}`,
	} {
		for _, path := range []string{"/api/record-types/validate", "/api/record-types"} {
			if r := do(t, h, "POST", path, bad); r.status != 400 {
				t.Errorf("%s %s: want 400, got %d %s", path, bad, r.status, r.body)
			}
		}
	}

	// Deleting the type removes it with its data; the name is free again.
	if r := do(t, h, "DELETE", "/api/record-types/invoice?tenant=acme", ""); r.status != 204 {
		t.Fatalf("delete type: %d %s", r.status, r.body)
	}
	if r := do(t, h, "GET", "/api/record-types/invoice?tenant=acme", ""); r.status != 404 {
		t.Fatalf("deleted type: %d", r.status)
	}
	if r := do(t, h, "DELETE", "/api/record-types/invoice?tenant=acme", ""); r.status != 404 {
		t.Fatalf("delete twice: %d", r.status)
	}
	if r := do(t, h, "POST", "/api/record-types?tenant=acme", `{"name":"invoice","schema":{"type":"object"}}`); decode[webserver.Definition](t, r).Version != 1 {
		t.Fatalf("a recreated type starts at v1: %s", r.body)
	}
	if r := do(t, h, "GET", "/api/record-types/invoice/records?tenant=acme", ""); !strings.Contains(r.body, `"items":[]`) {
		t.Fatalf("a recreated type has no records: %s", r.body)
	}
}
