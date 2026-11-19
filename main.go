package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

type Repo struct {
	Owner, Name, Description string
	Private                  bool
	Issues                   []map[string]any
	Labels                   []map[string]any
	Pulls                    []map[string]any
	Files                    map[string]string
	Branches                 map[string]string
	NextIssue, NextID        int
	Objects                  map[string]GitObject
	Tags                     map[string]string
	GitCache                 map[string][]byte
	Commits                  []map[string]any
	BranchCommits            map[string][]string
	NextPull                 int
	IssueCache               map[string][]byte
	Milestones               []map[string]any
	Collaborators            []string
}
type Store struct {
	mu      sync.RWMutex
	repos   map[string]*Repo
	owner   string
	request uint64
}

var db = Store{repos: map[string]*Repo{}}

func key(o, n string) string { return strings.ToLower(o) + "/" + strings.ToLower(n) }
func digest(s string) string { h := sha1.Sum([]byte(s)); return hex.EncodeToString(h[:]) }
func (s *Store) learn(owner string) {
	if s.owner == "" {
		s.owner = owner
	}
	if s.owner != owner {
		return
	}
	for k, r := range s.repos {
		if r.Owner == "" {
			delete(s.repos, k)
			r.Owner = owner
			s.repos[key(owner, r.Name)] = r
		}
	}
}
func (s *Store) repo(owner, name string) *Repo {
	if s.owner != "" && owner != s.owner {
		return nil
	}
	r := s.repos[key(owner, name)]
	if r == nil && s.owner == "" {
		r = s.repos[key("", name)]
	}
	if r != nil {
		s.learn(owner)
	}
	return r
}
func (s *Store) newRepo(owner, name, desc string) *Repo {
	r := &Repo{Owner: owner, Name: name, Description: desc, Files: map[string]string{}, Branches: map[string]string{"main": ""}, Tags: map[string]string{}, Objects: map[string]GitObject{}, GitCache: map[string][]byte{}, NextIssue: 1, NextID: 1, NextPull: 1, IssueCache: map[string][]byte{}, BranchCommits: map[string][]string{}, Milestones: []map[string]any{}, Collaborators: []string{}}
	s.repos[key(owner, name)] = r
	return r
}
func (s *Store) write(w http.ResponseWriter, code int, v any, headers map[string]string) {
	b, _ := json.Marshal(v)
	s.request++
	w.Header().Set("content-type", "application/json; charset=utf-8")
	w.Header().Set("content-length", strconv.Itoa(len(b)))
	w.Header().Set("x-github-request-id", fmt.Sprintf("FORGE%08x", s.request))
	for k, v := range headers {
		w.Header().Set(k, v)
	}
	w.WriteHeader(code)
	_, _ = w.Write(b)
}
func (s *Store) fail(w http.ResponseWriter, code int, msg string) {
	s.write(w, code, map[string]string{"message": msg}, nil)
}
func repoJSON(r *Repo) map[string]any {
	sum := digest(key(r.Owner, r.Name))
	n, _ := strconv.ParseInt(sum[:7], 16, 64)
	open := 0
	for _, i := range r.Issues {
		if i["state"] == "open" {
			open++
		}
	}
	return map[string]any{"id": n, "node_id": "R_" + sum[:16], "name": r.Name, "full_name": r.Owner + "/" + r.Name, "owner": map[string]any{"login": r.Owner, "id": 1, "type": "User"}, "private": r.Private, "description": r.Description, "html_url": "http://forge/" + r.Owner + "/" + r.Name, "url": "/api/v3/repos/" + r.Owner + "/" + r.Name, "default_branch": "main", "has_issues": true, "has_downloads": true, "has_wiki": true, "open_issues_count": open, "permissions": map[string]bool{"admin": true, "push": true, "pull": true}}
}
func readJSON(r *http.Request) map[string]any {
	b, _ := io.ReadAll(r.Body)
	v := map[string]any{}
	_ = json.Unmarshal(b, &v)
	return v
}
func readJSONStrict(r *http.Request) (map[string]any, error) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	v := map[string]any{}
	if len(b) == 0 {
		return v, nil
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return v, nil
}
func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
func (s *Store) serve(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if r.Method == "GET" && p == "/healthz" {
		s.write(w, 200, map[string]bool{"ok": true}, nil)
		return
	}
	known := p == "/api/v3" || p == "/api/v3/" || p == "/api/v3/meta" || p == "/api/v3/user" || p == "/api/graphql" || p == "/mcp" || strings.HasPrefix(p, "/api/v3/") || strings.HasSuffix(p, ".git/info/refs") || strings.HasSuffix(p, ".git/git-upload-pack") || strings.HasSuffix(p, ".git/git-receive-pack")
	if !known {
		s.fail(w, 404, "Not Found")
		return
	}
	git := strings.Contains(p, ".git/")
	token := os.Getenv("MAKEFUN_SHOP_TOKEN")
	if !git && (token == "" || r.Header.Get("Authorization") != "Bearer "+token) {
		s.fail(w, 401, "Requires authentication")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p == "/api/v3" || p == "/api/v3/" {
		if r.Method != "GET" {
			s.fail(w, 404, "Not Found")
		} else {
			s.write(w, 200, map[string]string{"current_user_url": "/api/v3/user", "rate_limit_url": "/api/v3/rate_limit"}, map[string]string{"x-oauth-scopes": "repo, read:org"})
		}
		return
	}
	if p == "/api/v3/meta" {
		s.write(w, 200, map[string]string{"installed_version": "3.0.0"}, nil)
		return
	}
	if p == "/api/v3/user" {
		u := s.owner
		if u == "" {
			u = "test-user"
		}
		s.write(w, 200, map[string]any{"login": u, "id": 1, "node_id": "U_1", "type": "User", "name": nil}, nil)
		return
	}
	if p == "/api/v3/user/repos" {
		if r.Method == "POST" {
			b, err := readJSONStrict(r)
			if err != nil {
				s.fail(w, 400, "Invalid JSON")
				return
			}
			name := str(b["name"])
			if name == "" {
				s.fail(w, 422, "name is required")
				return
			}
			if s.repos[key(s.owner, name)] != nil || s.repos[key("", name)] != nil {
				s.fail(w, 422, "Repository already exists")
				return
			}
			x := s.newRepo("", name, str(b["description"]))
			x.Private, _ = b["private"].(bool)
			if s.owner != "" {
				s.learn(s.owner)
			}
			s.write(w, 201, repoJSON(x), nil)
			return
		}
		if r.Method == "GET" {
			all := []*Repo{}
			for _, x := range s.repos {
				if x.Owner == "" || x.Owner == s.owner {
					all = append(all, x)
				}
			}
			sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
			if page < 1 {
				page = 1
			}
			if per < 1 {
				per = 30
			}
			start := (page - 1) * per
			end := start + per
			if start > len(all) {
				start = len(all)
			}
			if end > len(all) {
				end = len(all)
			}
			v := []any{}
			for _, x := range all[start:end] {
				v = append(v, repoJSON(x))
			}
			h := map[string]string{}
			if page*per < len(all) {
				host := r.Host
				if host == "" {
					host = "localhost"
				}
				h["link"] = fmt.Sprintf("<http://%s%s?per_page=%d&page=%d>; rel=\"next\"", host, p, per, page+1)
			}
			s.write(w, 200, v, h)
			return
		}
	}
	if p == "/api/v3/rate_limit" && r.Method == "GET" {
		s.write(w, 200, map[string]any{"resources": map[string]any{"core": map[string]any{"limit": 5000, "remaining": 5000, "reset": 0, "used": 0}, "search": map[string]any{"limit": 30, "remaining": 30, "reset": 0, "used": 0}}, "rate": map[string]any{"limit": 5000, "remaining": 5000, "reset": 0, "used": 0}}, nil)
		return
	}
	if strings.HasPrefix(p, "/api/v3/search/") && r.Method == "GET" {
		q := strings.ToLower(r.URL.Query().Get("q"))
		items := []any{}
		if strings.TrimPrefix(p, "/api/v3/search/") == "repositories" {
			for _, repo := range s.repos {
				if (s.owner == "" || repo.Owner == s.owner) && strings.Contains(strings.ToLower(repo.Name+" "+repo.Description+" "+repo.Owner), q) {
					items = append(items, repoJSON(repo))
				}
			}
			sort.Slice(items, func(i, j int) bool {
				return str(items[i].(map[string]any)["full_name"]) < str(items[j].(map[string]any)["full_name"])
			})
		} else if strings.TrimPrefix(p, "/api/v3/search/") == "issues" {
			for _, repo := range s.repos {
				for _, issue := range repo.Issues {
					if strings.Contains(strings.ToLower(str(issue["title"])+" "+str(issue["body"])), q) {
						items = append(items, s.issue(repo, issue))
					}
				}
			}
		}
		s.write(w, 200, map[string]any{"total_count": len(items), "incomplete_results": false, "items": items}, nil)
		return
	}
	if p == "/api/graphql" && r.Method == "POST" {
		s.graphql(w, r)
		return
	}
	if p == "/mcp" && r.Method == "POST" {
		s.mcp(w, r)
		return
	}
	if strings.HasPrefix(p, "/api/v3/repos/") {
		s.restRepo(w, r)
		return
	}
	if git {
		s.gitHTTP(w, r)
		return
	}
	s.fail(w, 404, "Not Found")
}
func (s *Store) restRepo(w http.ResponseWriter, r *http.Request) {
	seg := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v3/repos/"), "/")
	if len(seg) < 2 {
		s.fail(w, 404, "Not Found")
		return
	}
	owner, _ := strconv.Unquote("\"" + seg[0] + "\"")
	name, _ := strconv.Unquote("\"" + seg[1] + "\"")
	owner = strings.ReplaceAll(owner, "%20", " ")
	name = strings.ReplaceAll(name, "%20", " ")
	tail := ""
	if len(seg) > 2 {
		tail = "/" + strings.Join(seg[2:], "/")
	}
	x := s.repo(owner, name)
	if x == nil {
		s.fail(w, 404, "Not Found")
		return
	}
	s.restComplete(w, r, x, owner, name, tail)
	return
}
func (s *Store) issue(r *Repo, i map[string]any) map[string]any {
	v := map[string]any{}
	for k, x := range i {
		v[k] = x
	}
	n := i["number"]
	v["url"] = fmt.Sprintf("/api/v3/repos/%s/%s/issues/%v", r.Owner, r.Name, n)
	v["repository_url"] = fmt.Sprintf("/api/v3/repos/%s/%s", r.Owner, r.Name)
	v["comments"] = 0
	v["node_id"] = fmt.Sprintf("I_%v", i["id"])
	v["html_url"] = fmt.Sprintf("http://forge/%s/%s/issues/%v", r.Owner, r.Name, n)
	return v
}
func (s *Store) graphql(w http.ResponseWriter, r *http.Request) { s.graphqlComplete(w, r) }
func mustJSON(v any) string                                     { b, _ := json.Marshal(v); return string(b) }
func (s *Store) mcpOld(w http.ResponseWriter, r *http.Request) {
	var q map[string]any
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		s.write(w, 400, map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32700, "message": "Parse error"}}, nil)
		return
	}
	id := q["id"]
	method := str(q["method"])
	result := any(map[string]any{})
	tools := []any{}
	for _, item := range []struct {
		name, description string
		properties        map[string]any
	}{
		{"create_repo", "Create a repository", map[string]any{"name": map[string]string{"type": "string"}}},
		{"get_repo", "Get a repository", map[string]any{"owner": map[string]string{"type": "string"}, "repo": map[string]string{"type": "string"}}},
		{"create_issue", "Create an issue", map[string]any{"owner": map[string]string{"type": "string"}, "repo": map[string]string{"type": "string"}, "title": map[string]string{"type": "string"}, "body": map[string]string{"type": "string"}}},
		{"list_issues", "List issues", map[string]any{"owner": map[string]string{"type": "string"}, "repo": map[string]string{"type": "string"}, "state": map[string]string{"type": "string"}}},
		{"create_pull_request", "Create a pull request", map[string]any{"owner": map[string]string{"type": "string"}, "repo": map[string]string{"type": "string"}, "title": map[string]string{"type": "string"}, "head": map[string]string{"type": "string"}, "base": map[string]string{"type": "string"}}},
		{"merge_pull_request", "Merge a pull request", map[string]any{"owner": map[string]string{"type": "string"}, "repo": map[string]string{"type": "string"}, "pull_number": map[string]string{"type": "number"}}},
		{"search_repositories", "Search repositories", map[string]any{"query": map[string]string{"type": "string"}}},
	} {
		tools = append(tools, map[string]any{"name": item.name, "description": item.description, "inputSchema": map[string]any{"type": "object", "properties": item.properties}})
	}
	switch method {
	case "server/discover", "initialize":
		result = map[string]any{"supportedVersions": []string{"2026-07-28", "2025-11-25", "2025-03-26"}, "protocolVersion": "2025-11-25", "serverInfo": map[string]string{"name": "forge", "version": "1.0.0"}, "capabilities": map[string]any{"tools": map[string]any{}}}
	case "notifications/initialized":
		return
	case "tools/list":
		result = map[string]any{"tools": tools}
	case "tools/call":
		params, _ := q["params"].(map[string]any)
		args, _ := params["arguments"].(map[string]any)
		if params["name"] == "create_repo" {
			name := str(args["name"])
			if name == "" {
				s.write(w, 200, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32602, "message": "Invalid params"}}, nil)
				return
			}
			if s.repos[key(s.owner, name)] != nil || s.repos[key("", name)] != nil {
				s.write(w, 200, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32602, "message": "repository already exists"}}, nil)
				return
			}
			repo := s.newRepo("", name, str(args["description"]))
			if s.owner != "" {
				s.learn(s.owner)
			}
			result = map[string]any{"content": []any{map[string]string{"type": "text", "text": fmt.Sprintf("Created repository %s", name)}}}
			_ = repo
		}
	default:
		s.write(w, 200, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": "Method not found"}}, nil)
		return
	}
	s.write(w, 200, map[string]any{"jsonrpc": "2.0", "id": id, "result": result}, nil)
}
func (s *Store) gitHTTP(w http.ResponseWriter, r *http.Request) {
	s.gitHandler(w, r)
}
func main() {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	fmt.Printf("FORGE_PORT=%d\n", port)
	srv := &http.Server{Handler: http.HandlerFunc(db.serve)}
	go func() { _ = srv.Serve(ln) }()
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	<-ch
	_ = srv.Close()
}
