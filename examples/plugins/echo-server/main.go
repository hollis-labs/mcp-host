// Command echo-server is a real, standalone MCP server: it speaks the
// MCP protocol itself, over stdio, with no dependency on mcp-host. Build
// and run it directly with any MCP client to see that independence —
// it does not know it will typically be spawned and supervised by
// mcp-host as a process-mode logical server (see examples/config/host.yaml).
//
// This is T6's process-mode example: a genuinely independent process,
// reached over stdio or HTTP, matching the ADR's process-mode
// definition exactly ("a fully independent, real MCP server, any
// language").
package main

import (
	"context"
	"log"

	gmcpserver "github.com/hollis-labs/go-mcp/server"
)

func main() {
	srv := gmcpserver.NewServer("echo-server", "0.1.0")
	srv.RegisterTool(gmcpserver.Tool{
		Name:        "echo",
		Description: "Echoes back the given message.",
		InputSchema: gmcpserver.InputSchema(gmcpserver.StringProp("message", "message to echo", true)),

		ReadOnlyHint:    true,
		DestructiveHint: false,
		IdempotentHint:  true,
		OpenWorldHint:   false,

		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			msg, _ := args["message"].(string)
			return map[string]any{"echo": msg}, nil
		},
	})

	if err := srv.Run(context.Background()); err != nil {
		log.Fatalf("echo-server: %v", err)
	}
}
