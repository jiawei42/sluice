// Command fakeserver runs the test MCP server over stdio, or over Streamable
// HTTP with -http :8080 (optionally requiring an Authorization header value).
package main

import (
	"context"
	"flag"
	"log"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jiawei42/sluice/internal/fakeserver"
)

func main() {
	addr := flag.String("http", "", "serve Streamable HTTP on this address instead of stdio")
	auth := flag.String("auth", "", "required Authorization header value (http only)")
	flag.Parse()

	if *addr == "" {
		if err := fakeserver.New().Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			log.Fatal(err)
		}
		return
	}
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return fakeserver.New() }, nil)
	log.Printf("fakeserver listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, fakeserver.RequireAuth(*auth, h)))
}
