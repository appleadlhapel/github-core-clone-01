package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// mcp serves the Streamable HTTP JSON-RPC endpoint. MCP calls deliberately use the
// same Repo instances and owner resolution path as REST and GraphQL.
func (s *Store) mcp(w http.ResponseWriter, req *http.Request) {
	var q map[string]any
	if err := json.NewDecoder(req.Body).Decode(&q); err != nil {
		s.mcpReply(w, 400, nil, nil, -32700, "Parse error")
		return
	}
	id, method := q["id"], str(q["method"])
	params, _ := q["params"].(map[string]any)
	if method == "notifications/initialized" {
		s.request++
		w.Header().Set("x-github-request-id", fmt.Sprintf("FORGE%08x", s.request))
		w.WriteHeader(202)
		return
	}
	switch method {
	case "server/discover":
		s.mcpReply(w, 200, id, map[string]any{"supportedVersions": []string{"2026-07-28", "2025-11-25"}, "serverInfo": map[string]string{"name": "github-core-clone", "version": "1.0.0"}, "capabilities": map[string]any{"tools": map[string]any{}}}, 0, "")
	case "initialize":
		version := str(params["protocolVersion"])
		if version == "" {
			version = "2025-11-25"
		}
		s.mcpReply(w, 200, id, map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "github-core-clone", "version": "1.0.0"}}, 0, "")
	case "ping":
		s.mcpReply(w, 200, id, map[string]any{}, 0, "")
	case "tools/list":
		s.mcpReply(w, 200, id, map[string]any{"tools": mcpToolList}, 0, "")
	case "tools/call":
		args, _ := params["arguments"].(map[string]any)
		if args == nil {
			args = map[string]any{}
		}
		name := strings.ToLower(str(params["name"]))
		if i := strings.LastIndex(name, "__"); i >= 0 {
			name = name[i+2:]
		}
		result, code, msg := s.mcpCall(name, args)
		s.mcpReply(w, 200, id, result, code, msg)
	default:
		s.mcpReply(w, 200, id, nil, -32601, "Method not found")
	}
}

func (s *Store) mcpReply(w http.ResponseWriter, status int, id, result any, code int, message string) {
	body := map[string]any{"jsonrpc": "2.0", "id": id}
	if code != 0 {
		body["error"] = map[string]any{"code": code, "message": message}
	} else {
		body["result"] = result
	}
	s.write(w, status, body, nil)
}

var mcpToolList = func() []any {
	defs := []struct {
		name, description string
		props             map[string]string
		required          []string
	}{
		{"create_repo", "Create a repository", map[string]string{"name": "string", "description": "string", "private": "boolean"}, []string{"name"}},
		{"get_repo", "Get a repository", map[string]string{"owner": "string", "repo": "string"}, []string{"owner", "repo"}},
		{"create_issue", "Create an issue", map[string]string{"owner": "string", "repo": "string", "title": "string", "body": "string"}, []string{"owner", "repo", "title"}},
		{"list_issues", "List issues", map[string]string{"owner": "string", "repo": "string", "state": "string"}, []string{"owner", "repo"}},
		{"create_pull_request", "Create a pull request", map[string]string{"owner": "string", "repo": "string", "title": "string", "head": "string", "base": "string"}, []string{"owner", "repo", "title", "head", "base"}},
		{"merge_pull_request", "Merge a pull request", map[string]string{"owner": "string", "repo": "string", "pull_number": "number"}, []string{"owner", "repo", "pull_number"}},
		{"search_repositories", "Search repositories", map[string]string{"query": "string"}, []string{"query"}},
	}
	out := make([]any, 0, len(defs))
	for _, d := range defs {
		properties := map[string]any{}
		for k, typ := range d.props {
			properties[k] = map[string]string{"type": typ}
		}
		out = append(out, map[string]any{"name": d.name, "description": d.description, "inputSchema": map[string]any{"type": "object", "properties": properties, "required": d.required}})
	}
	return out
}()

func mcpTextResult(v any) any {
	b, _ := json.Marshal(v)
	return map[string]any{"content": []any{map[string]string{"type": "text", "text": string(b)}}, "structuredContent": v}
}

func (s *Store) mcpCall(name string, a map[string]any) (any, int, string) {
	owner, repoName := str(a["owner"]), str(a["repo"])
	if owner == "" {
		owner = s.owner
		if owner == "" {
			owner = "test-user"
		}
	}
	if name == "create_repo" {
		n := str(a["name"])
		if n == "" {
			return nil, -32602, "name is required"
		}
		if s.repos[key(s.owner, n)] != nil || s.repos[key("", n)] != nil {
			return nil, -32602, "repository already exists"
		}
		r := s.newRepo("", n, str(a["description"]))
		r.Private, _ = a["private"].(bool)
		if s.owner != "" {
			s.learn(s.owner)
		}
		return mcpTextResult(repoJSON(r)), 0, ""
	}
	if name == "search_repositories" {
		q := strings.ToLower(str(a["query"]))
		list := []any{}
		for _, r := range s.repos {
			if (s.owner == "" || r.Owner == s.owner) && strings.Contains(strings.ToLower(r.Name+" "+r.Description+" "+r.Owner), q) {
				list = append(list, repoJSON(r))
			}
		}
		sort.Slice(list, func(i, j int) bool {
			return str(list[i].(map[string]any)["full_name"]) < str(list[j].(map[string]any)["full_name"])
		})
		return mcpTextResult(list), 0, ""
	}
	if name != "get_repo" && name != "create_issue" && name != "list_issues" && name != "create_pull_request" && name != "merge_pull_request" {
		return nil, -32602, "Unknown tool or missing arguments"
	}
	if repoName == "" {
		return nil, -32602, "repository not found"
	}
	r := s.repo(owner, repoName)
	if r == nil {
		return nil, -32602, "repository not found"
	}
	switch name {
	case "get_repo":
		return mcpTextResult(repoJSON(r)), 0, ""
	case "create_issue":
		if str(a["title"]) == "" {
			return nil, -32602, "title is required"
		}
		i := map[string]any{"id": r.NextID, "number": r.NextIssue, "title": str(a["title"]), "body": str(a["body"]), "state": "open", "labels": []any{}, "comments": []any{}, "assignee": nil, "milestone": nil, "created_at": "2000-01-01T00:00:00Z", "updated_at": "2000-01-01T00:00:00Z"}
		r.NextID++
		r.NextIssue++
		r.Issues = append(r.Issues, i)
		s.changed(r)
		return mcpTextResult(s.issue(r, i)), 0, ""
	case "list_issues":
		state := str(a["state"])
		out := []any{}
		for _, i := range r.Issues {
			if state == "" || state == "all" || str(i["state"]) == state {
				out = append(out, s.issue(r, i))
			}
		}
		return mcpTextResult(out), 0, ""
	case "create_pull_request":
		head, base := str(a["head"]), str(a["base"])
		if head == "" || base == "" || str(a["title"]) == "" {
			return nil, -32602, "title, head and base are required"
		}
		p := map[string]any{"id": r.NextID, "number": r.NextPull, "title": str(a["title"]), "body": str(a["body"]), "state": "open", "merged": false, "head": map[string]any{"ref": head, "sha": r.Branches[head]}, "base": map[string]any{"ref": base, "sha": r.Branches[base]}, "files": []any{}, "created_at": "2000-01-01T00:00:00Z", "merged_at": nil}
		r.NextID++
		r.NextPull++
		r.Pulls = append(r.Pulls, p)
		s.changed(r)
		return mcpTextResult(s.pull(r, p)), 0, ""
	case "merge_pull_request":
		n, ok := a["pull_number"].(float64)
		if !ok {
			return nil, -32602, "pull_number is required"
		}
		for _, p := range r.Pulls {
			if p["number"] == int(n) {
				p["merged"], p["state"] = true, "closed"
				id := digest(fmt.Sprintf("Merge PR %d", int(n)))
				p["merge_commit_sha"] = id
				p["merged_at"] = "2000-01-01T00:00:00Z"
				s.changed(r)
				v := map[string]any{"number": int(n), "merged": true, "sha": id}
				return mcpTextResult(v), 0, ""
			}
		}
		return nil, -32602, "pull request not found"
	}
	return nil, -32602, "Unknown tool or missing arguments"
}
