// Package webserver is the workbench's web dashboard: a JSON API, generated
// from spec/openapi.yaml (api.gen.go), and a plain HTML/JS/CSS UI embedded
// in the binary. It browses every tenant of the storage and edits records
// through the same Mneme engine operations the MCP tools use.
package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"slices"
	"strings"

	"github.com/fragshq/mneme/errs"
	"github.com/fragshq/mneme/jsonx"
	"github.com/fragshq/mneme/query"
	"github.com/fragshq/mneme/schema"
	"github.com/labstack/echo/v4"
	"github.com/theirish81/edjson"

	"github.com/fragshq/mneme"
)

// Tenants gives the dashboard access to every tenant of the storage. The
// tenant "" is the default space: the data of a server without tenancy.
type Tenants interface {
	// List returns the tenants holding record types, in any order.
	List(ctx context.Context) ([]string, error)
	// Current is the tenant the MCP server works on.
	Current() string
	// Engine returns the engine serving tenant and the context to call it
	// with. An invalid tenant name fails with errs.ErrInvalid.
	Engine(ctx context.Context, tenant string) (context.Context, *mneme.Engine, error)
}

// Options configures the dashboard.
type Options struct {
	Tenants Tenants
	// LoopbackOnly rejects requests whose Host header is not a loopback
	// name, which defeats DNS-rebinding attacks on a dashboard bound to
	// 127.0.0.1. Set it whenever the listener is on a loopback address.
	LoopbackOnly bool
}

// New returns the dashboard: the API under /api and the UI at /.
// Server-side failures go to the standard logger, which a stdio server
// points at stderr.
func New(opts Options) *echo.Echo {
	e := echo.New()
	e.HideBanner = true // stdout may carry a protocol (MCP): never write to it
	e.HidePort = true
	e.Logger.SetOutput(log.Writer())
	// echo.New made StdLogger, which Start gives the http.Server as its
	// error log, on stdout; use the standard logger like everything else.
	e.StdLogger = log.Default()
	e.HTTPErrorHandler = errorHandler
	if opts.LoopbackOnly {
		e.Use(loopbackOnly)
	}
	e.Use(jsonBodies)
	RegisterHandlersWithBaseURL(e, &server{tenants: opts.Tenants}, "/api")
	e.StaticFS("/", echo.MustSubFS(uiFiles, "ui"))
	return e
}

// loopbackOnly rejects requests addressed to a non-loopback host name.
func loopbackOnly(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		host := c.Request().Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.Trim(host, "[]")
		if ip := net.ParseIP(host); (ip != nil && ip.IsLoopback()) || strings.EqualFold(host, "localhost") {
			return next(c)
		}
		return echo.NewHTTPError(http.StatusForbidden, "the dashboard only answers to localhost")
	}
}

// jsonBodies requires a JSON content type on writes, so a cross-site HTML
// form (which can only send form or text bodies) can never reach them.
func jsonBodies(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		switch c.Request().Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch:
			if ct := c.Request().Header.Get(echo.HeaderContentType); !strings.HasPrefix(ct, echo.MIMEApplicationJSON) {
				return echo.NewHTTPError(http.StatusUnsupportedMediaType, "requests with a body must be application/json")
			}
		}
		return next(c)
	}
}

// errorHandler renders every error as an Error object, with the status its
// kind calls for.
func errorHandler(err error, c echo.Context) {
	if c.Response().Committed {
		return
	}
	status, body := http.StatusInternalServerError, Error{Error: err.Error()}
	he := (*echo.HTTPError)(nil)
	ve := (*schema.ValidationError)(nil)
	switch {
	case errors.As(err, &he):
		status, body.Error = he.Code, fmt.Sprint(he.Message)
	case errors.As(err, &ve):
		status = http.StatusBadRequest
		if err := edjson.Copy(&body.Issues, ve.Issues); err != nil {
			log.Printf("dashboard: %v", err)
		}
	case errors.Is(err, errs.ErrInvalid), errors.Is(err, errs.ErrNoTenant):
		status = http.StatusBadRequest
	case errors.Is(err, errs.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, errs.ErrConflict), errors.Is(err, errs.ErrQuota):
		status = http.StatusConflict
	default:
		log.Printf("dashboard: %s %s: %v", c.Request().Method, c.Request().URL.Path, err)
	}
	if err := c.JSON(status, body); err != nil {
		log.Printf("dashboard: writing an error response: %v", err)
	}
}

// server implements ServerInterface.
type server struct {
	tenants Tenants
}

var _ ServerInterface = (*server)(nil)

func (s *server) ListTenants(c echo.Context) error {
	names, err := s.tenants.List(c.Request().Context())
	if err != nil {
		return err
	}
	current := s.tenants.Current()
	names = slices.Compact(slices.Sorted(slices.Values(append(names, current))))
	out := TenantList{Tenants: []Tenant{}}
	for _, n := range names {
		out.Tenants = append(out.Tenants, Tenant{Name: n, Current: n == current})
	}
	return c.JSON(http.StatusOK, out)
}

func (s *server) ListRecordTypes(c echo.Context, params ListRecordTypesParams) error {
	ctx, e, err := s.tenants.Engine(c.Request().Context(), params.Tenant)
	if err != nil {
		return err
	}
	defs, err := e.ListRecordTypes(ctx)
	return edjson.JSON[[]RecordTypeSummary](c, http.StatusOK, defs, err)
}

// recordTypeDetails is the domain form of RecordTypeDetails.
type recordTypeDetails struct {
	Definition schema.Definition
	Versions   []int
	Fields     []schema.Field
}

func (s *server) GetRecordType(c echo.Context, name Name, params GetRecordTypeParams) error {
	ctx, e, err := s.tenants.Engine(c.Request().Context(), params.Tenant)
	if err != nil {
		return err
	}
	d, err := e.GetRecordType(ctx, name, params.Version)
	if err != nil {
		return err
	}
	desc, err := e.DescribeRecordType(ctx, name)
	if err != nil {
		return err
	}
	out := RecordTypeDetails{}
	if err := edjson.Copy(&out, recordTypeDetails{Definition: d, Versions: desc.Versions, Fields: desc.Fields}); err != nil {
		return err
	}
	// Arrays the spec requires are [] rather than null.
	if out.Definition.Indexes == nil {
		out.Definition.Indexes = []string{}
	}
	for i := range out.Fields {
		if out.Fields[i].Types == nil {
			out.Fields[i].Types = []string{}
		}
	}
	return c.JSON(http.StatusOK, out)
}

func (s *server) DefineRecordType(c echo.Context, params DefineRecordTypeParams) error {
	ctx, e, err := s.tenants.Engine(c.Request().Context(), params.Tenant)
	if err != nil {
		return err
	}
	d, err := definitionInput(c)
	if err != nil {
		return err
	}
	stored, err := e.Define(ctx, d)
	if err != nil {
		return err
	}
	out := Definition{}
	if err := edjson.Copy(&out, stored); err != nil {
		return err
	}
	if out.Indexes == nil {
		out.Indexes = []string{}
	}
	return c.JSON(http.StatusCreated, out)
}

func (s *server) ValidateRecordType(c echo.Context) error {
	d, err := definitionInput(c)
	if err != nil {
		return err
	}
	// Validation needs no storage, so any tenant's engine will do.
	_, e, err := s.tenants.Engine(c.Request().Context(), s.tenants.Current())
	if err != nil {
		return err
	}
	if err := e.ValidateDefinition(d); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, Validation{Valid: true})
}

func (s *server) DeleteRecordType(c echo.Context, name Name, params DeleteRecordTypeParams) error {
	ctx, e, err := s.tenants.Engine(c.Request().Context(), params.Tenant)
	if err != nil {
		return err
	}
	if err := e.DeleteRecordType(ctx, name); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// definitionInput decodes a DefinitionInput body into a schema.Definition.
func definitionInput(c echo.Context) (schema.Definition, error) {
	in := DefinitionInput{}
	if err := c.Bind(&in); err != nil {
		return schema.Definition{}, err
	}
	return schema.Definition{Name: in.Name, Title: in.Title, Description: in.Description,
		IDStrategy: in.IDStrategy, Indexes: in.Indexes, Schema: in.Schema}, nil
}

func (s *server) ListRecords(c echo.Context, name Name, params ListRecordsParams) error {
	ctx, e, err := s.tenants.Engine(c.Request().Context(), params.Tenant)
	if err != nil {
		return err
	}
	q := query.Query{Limit: params.Limit, Cursor: params.Cursor}
	if q.Filter, err = query.ParseFilter([]byte(params.Filter)); err != nil {
		return err
	}
	if q.Sort, err = parseSort(params.Sort); err != nil {
		return err
	}
	page, err := e.List(ctx, name, q)
	return edjson.JSON[RecordPage](c, http.StatusOK, page, err)
}

func (s *server) GetRecord(c echo.Context, name Name, id string, params GetRecordParams) error {
	ctx, e, err := s.tenants.Engine(c.Request().Context(), params.Tenant)
	if err != nil {
		return err
	}
	r, err := e.Get(ctx, name, id)
	return edjson.JSON[Record](c, http.StatusOK, r, err)
}

func (s *server) InsertRecord(c echo.Context, name Name, params InsertRecordParams) error {
	ctx, e, err := s.tenants.Engine(c.Request().Context(), params.Tenant)
	if err != nil {
		return err
	}
	data, labels, err := recordInput(c)
	if err != nil {
		return err
	}
	r, err := e.Insert(ctx, name, data, labels...)
	return edjson.JSON[Record](c, http.StatusCreated, r, err)
}

func (s *server) UpdateRecord(c echo.Context, name Name, id string, params UpdateRecordParams) error {
	ctx, e, err := s.tenants.Engine(c.Request().Context(), params.Tenant)
	if err != nil {
		return err
	}
	data, labels, err := recordInput(c)
	if err != nil {
		return err
	}
	r, err := e.Update(ctx, name, id, data, labels...)
	return edjson.JSON[Record](c, http.StatusOK, r, err)
}

func (s *server) DeleteRecord(c echo.Context, name Name, id string, params DeleteRecordParams) error {
	ctx, e, err := s.tenants.Engine(c.Request().Context(), params.Tenant)
	if err != nil {
		return err
	}
	if err := e.Delete(ctx, name, id); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (s *server) ListLabels(c echo.Context, name Name, params ListLabelsParams) error {
	ctx, e, err := s.tenants.Engine(c.Request().Context(), params.Tenant)
	if err != nil {
		return err
	}
	f, err := query.ParseFilter([]byte(params.Filter))
	if err != nil {
		return err
	}
	labels, err := e.ListLabels(ctx, name, f)
	return edjson.JSON[[]LabelCount](c, http.StatusOK, labels, err)
}

// recordInput decodes a RecordInput body into engine arguments.
func recordInput(c echo.Context) (json.RawMessage, []string, error) {
	in := RecordInput{}
	if err := c.Bind(&in); err != nil {
		return nil, nil, err
	}
	if jsonx.IsNull(in.Data) {
		return nil, nil, errs.Invalidf("data is required")
	}
	return in.Data, in.Labels, nil
}

// parseSort reads "a,-b" into query.Sorts, through its lenient JSON form so
// the "-field" rule stays in one place. Empty is nil (the default sort).
func parseSort(raw string) (query.Sorts, error) {
	fields := []string{}
	for _, f := range strings.Split(raw, ",") {
		if f = strings.TrimSpace(f); f != "" {
			fields = append(fields, f)
		}
	}
	if len(fields) == 0 {
		return nil, nil
	}
	js, _ := json.Marshal(fields) // strings always encode
	out := query.Sorts{}
	if err := json.Unmarshal(js, &out); err != nil {
		return nil, errs.Invalidf("sort: %v", err)
	}
	return out, nil
}
