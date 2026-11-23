// Command mneme-mcp-stdio is a minimal workbench MCP server (stdio) exposing
// Mneme on top of a storage plugin chosen at startup.
//
// Each setting is a flag or an environment variable; a flag wins:
//
//	--storage     MNEME_STORAGE     redis (default), memory (nothing persisted),
//	                                postgres-shared (one table, record_type column) or
//	                                postgres-per-type (one table per record type)
//	--redis-addr  MNEME_REDIS_ADDR  Redis address (default localhost:6379)
//	--pg-dsn      MNEME_PG_DSN      Postgres URL, required by the postgres-* backends
//	--prefix      MNEME_PREFIX      key prefix for Redis (default mneme-workbench) or
//	                                table prefix for Postgres (default mneme_)
//	--tenant      MNEME_TENANT      optional; when set, every operation runs as this
//	                                one tenant (letters, digits, '_', '.', '-'; max 64).
//	                                Unset, the server is single-tenant
//	--dashboard   MNEME_DASHBOARD   optional web dashboard: off (default), on (serves
//	                                http://127.0.0.1:47080/) or a host:port to listen on
//
// See config.go, and --help.
package main

import (
	"context"
	"encoding/json"
	"log"
	"os"

	"github.com/fragshq/mneme/script"
	"github.com/fragshq/mneme/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fragshq/mneme"
)

func main() {
	log.SetOutput(os.Stderr) // stdout carries the MCP protocol
	if err := newRootCmd(serve).ExecuteContext(context.Background()); err != nil {
		os.Exit(1) // cobra has already printed the error to stderr
	}
}

// serve runs the MCP server over stdio until the client disconnects.
func serve(ctx context.Context, cfg config) error {
	addr, err := dashboardAddr(cfg.Dashboard)
	if err != nil {
		return err
	}
	st, err := openStorage(ctx, cfg)
	if err != nil {
		return err
	}
	defer st.close()
	if addr != "" {
		defer startDashboard(addr, st.tenants)()
	}

	server := newServer(&mcp.Implementation{Name: "mneme-workbench", Version: version}, st.engine)
	return server.Run(ctx, &mcp.StdioTransport{})
}

// newServer returns an MCP server exposing every Mneme tool, with
// tools.Instructions as its server instructions. Names, descriptions, input
// schemas, handlers and instructions all come from package mneme/tools;
// this server only maps them onto MCP.
func newServer(impl *mcp.Implementation, eng *mneme.Engine) *mcp.Server {
	ts := tools.All(eng, script.Options{})
	s := mcp.NewServer(impl, &mcp.ServerOptions{Instructions: tools.Instructions(ts)})
	for _, t := range ts {
		s.AddTool(&mcp.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema}, handler(t))
	}
	return s
}

// handler runs t on the raw MCP arguments. A result (always a JSON object)
// is returned twice: as pretty JSON text, for clients that only read text,
// and as structuredContent. Errors become tool errors (isError) whose text is
// the error message, without structuredContent.
func handler(t tools.Tool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		out, err := t.Call(ctx, req.Params.Arguments)
		if err != nil {
			res := &mcp.CallToolResult{}
			res.SetError(err)
			return res, nil
		}
		text, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: string(text)}},
			StructuredContent: json.RawMessage(text),
		}, nil
	}
}
