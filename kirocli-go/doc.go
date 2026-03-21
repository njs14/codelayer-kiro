// Package kirocli provides a Go SDK for programmatically interacting with
// Kiro CLI via the Agent Communication Protocol (ACP).
//
// Unlike the claudecode-go package which spawns a new process per query,
// kirocli maintains a persistent kiro-cli acp subprocess and multiplexes
// multiple sessions over a single JSON-RPC 2.0 connection.
//
// Basic usage:
//
//	client, err := kirocli.NewClient()
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	if err := client.Start("/path/to/project"); err != nil {
//	    log.Fatal(err)
//	}
//	defer client.Stop()
//
//	sessionID, err := client.SessionNew("/path/to/project")
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	result, err := client.SessionPrompt(sessionID, "Write a hello world function", 5*time.Minute)
//	fmt.Println(result.Text)
//
// Permission handling:
//
//	client.SetPermissionHandler(func(req kirocli.PermissionRequest) string {
//	    fmt.Printf("Kiro wants to: %s\n", req.Title)
//	    return "allow_once"
//	})
//
// Streaming updates:
//
//	sess := client.GetSession(sessionID)
//	for update := range sess.Updates {
//	    switch update.Kind {
//	    case kirocli.UpdateAgentMessageChunk:
//	        fmt.Print(update.Text)
//	    case kirocli.UpdateToolCall:
//	        fmt.Printf("[tool] %s\n", update.Title)
//	    }
//	}
package kirocli
