package main

// This deliberately small GraphQL reader covers the operation and selection syntax
// exercised by the conformance documents, including aliases, variables and inline values.
import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type gqlField struct {
	name, alias string
	args        map[string]any
	children    []gqlField
}
type gqlParser struct {
	t    []string
	i    int
	vars map[string]any
}

var gqlTokens = regexp.MustCompile(`\.\.\.|[_A-Za-z][_0-9A-Za-z]*|-?[0-9]+|"(?:\\.|[^"\\])*"|[!$():=@\[\]{|},]`)

func parseGraph(q string, vars map[string]any) []gqlField {
	p := gqlParser{t: gqlTokens.FindAllString(q, -1), vars: vars}
	for p.i < len(p.t) && p.t[p.i] != "{" {
		p.i++
	}
	return p.fields()
}
func (p *gqlParser) fields() []gqlField {
	if p.i >= len(p.t) || p.t[p.i] != "{" {
		return nil
	}
	p.i++
	out := []gqlField{}
	for p.i < len(p.t) && p.t[p.i] != "}" {
		if p.peek() == "..." {
			p.take()
			if p.peek() == "on" {
				p.take()
				p.take()
			}
			out = append(out, p.fields()...)
			continue
		}
		if p.t[p.i] == "," {
			p.i++
			continue
		}
		n := p.take()
		f := gqlField{name: n, alias: n, args: map[string]any{}}
		if p.peek() == ":" {
			p.take()
			f.alias = n
			f.name = p.take()
		}
		if p.peek() == "(" {
			p.take()
			for p.peek() != "}" && p.peek() != ")" && p.i < len(p.t) {
				if p.peek() == "," {
					p.take()
					continue
				}
				k := p.take()
				if p.peek() == ":" {
					p.take()
				}
				f.args[k] = p.value()
			}
			if p.peek() == ")" {
				p.take()
			}
		}
		if p.peek() == "@" {
			for p.peek() == "@" {
				p.take()
				p.take()
				if p.peek() == "(" {
					p.skipGroup("(", ")")
				}
			}
		}
		if p.peek() == "{" {
			f.children = p.fields()
		}
		out = append(out, f)
	}
	if p.peek() == "}" {
		p.take()
	}
	return out
}
func (p *gqlParser) take() string {
	if p.i >= len(p.t) {
		return ""
	}
	x := p.t[p.i]
	p.i++
	return x
}
func (p *gqlParser) peek() string {
	if p.i >= len(p.t) {
		return ""
	}
	return p.t[p.i]
}
func (p *gqlParser) skipGroup(a, b string) {
	d := 0
	for p.i < len(p.t) {
		x := p.take()
		if x == a {
			d++
		}
		if x == b {
			d--
			if d == 0 {
				return
			}
		}
	}
}
func (p *gqlParser) value() any {
	x := p.take()
	switch x {
	case "$":
		return p.vars[p.take()]
	case "[":
		a := []any{}
		for p.peek() != "]" && p.i < len(p.t) {
			if p.peek() == "," {
				p.take()
				continue
			}
			a = append(a, p.value())
		}
		p.take()
		return a
	case "{":
		m := map[string]any{}
		for p.peek() != "}" && p.i < len(p.t) {
			if p.peek() == "," {
				p.take()
				continue
			}
			k := p.take()
			p.take()
			m[k] = p.value()
		}
		p.take()
		return m
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	if strings.HasPrefix(x, "\"") {
		v, _ := strconv.Unquote(x)
		return v
	}
	if n, e := strconv.Atoi(x); e == nil {
		return n
	}
	return x
}
func (s *Store) graphqlComplete(w http.ResponseWriter, r *http.Request) {
	var request any
	if json.NewDecoder(r.Body).Decode(&request) != nil {
		s.write(w, 400, map[string]string{"message": "Invalid JSON"}, nil)
		return
	}
	if batch, ok := request.([]any); ok {
		out := make([]any, 0, len(batch))
		for _, item := range batch {
			out = append(out, s.graphqlResult(item))
		}
		s.write(w, 200, out, nil)
		return
	}
	s.write(w, 200, s.graphqlResult(request), nil)
}
func (s *Store) graphqlResult(v any) map[string]any {
	in, _ := v.(map[string]any)
	q := str(in["query"])
	vars, _ := in["variables"].(map[string]any)
	root := "Query"
	if strings.Contains(strings.ToLower(q), "mutation") {
		root = "Mutation"
	}
	fields := parseGraph(q, vars)
	if msg := gqlValidationError(fields, root); msg != "" {
		return map[string]any{"data": nil, "errors": []any{map[string]any{"message": msg}}}
	}
	return map[string]any{"data": s.gqlResolve(fields, root, nil)}
}

func gqlValidationError(fs []gqlField, typ string) string {
	allowed := map[string]map[string]bool{
		"Query": {"viewer": true, "repository": true, "node": true, "__type": true, "__typename": true},
		"User":  {"login": true}, "Repository": {"id": true, "databaseId": true, "name": true, "nameWithOwner": true, "owner": true, "description": true, "defaultBranchRef": true, "viewerPermission": true, "hasIssuesEnabled": true, "issue": true, "issues": true, "pullRequests": true, "pullRequest": true},
		"Issue": {"id": true, "number": true, "title": true, "state": true, "body": true}, "PullRequest": {"id": true, "number": true, "title": true, "state": true, "merged": true, "headRefName": true, "baseRefName": true, "mergeable": true, "files": true},
	}
	for _, f := range fs {
		if typ == "Node" && f.name == "..." {
			continue
		}
		if m, ok := allowed[typ]; ok && !m[f.name] {
			return fmt.Sprintf("Cannot query field %q on type %q.", f.name, typ)
		}
		childType := map[string]string{"viewer": "User", "repository": "Repository", "node": "Node", "issue": "Issue", "pullRequest": "PullRequest", "owner": "User"}[f.name]
		if childType != "" {
			if msg := gqlValidationError(f.children, childType); msg != "" {
				return msg
			}
		}
	}
	return ""
}
func (s *Store) gqlResolve(fs []gqlField, typ string, obj any) map[string]any {
	out := map[string]any{}
	for _, f := range fs {
		v, nt := s.gqlValue(f, typ, obj)
		if len(f.children) > 0 {
			if a, ok := v.([]any); ok {
				z := make([]any, 0, len(a))
				for _, x := range a {
					z = append(z, s.gqlResolve(f.children, nt, x))
				}
				v = z
			} else if v != nil {
				v = s.gqlResolve(f.children, nt, v)
			}
		}
		out[f.alias] = v
	}
	return out
}
func (s *Store) gqlValue(f gqlField, t string, o any) (any, string) {
	a := f.args
	if strings.HasPrefix(t, "Repository") {
		if repo, ok := o.(*Repo); !ok || repo == nil {
			return nil, "Object"
		}
	}
	switch t + "." + f.name {
	case "Query.viewer":
		u := s.owner
		if u == "" {
			u = "test-user"
		}
		return map[string]any{"login": u}, "User"
	case "Query.repository":
		r := s.repo(str(a["owner"]), str(a["name"]))
		if r == nil {
			return nil, "Repository"
		}
		return r, "Repository"
	case "Query.node":
		id := str(a["id"])
		for _, r := range s.repos {
			if strings.HasPrefix(id, "R_") && id == "R_"+digest(key(r.Owner, r.Name))[:16] {
				return r, "Repository"
			}
			for _, i := range r.Issues {
				if id == "I_"+toString(i["id"]) {
					return i, "Issue"
				}
			}
			for _, p := range r.Pulls {
				if id == "P_"+toString(p["id"]) {
					return p, "PullRequest"
				}
			}
		}
		return nil, "Node"
	case "Query.__type":
		return map[string]any{"name": a["name"]}, "__Type"
	case "Mutation.createIssue":
		in, _ := a["input"].(map[string]any)
		r := s.gqlRepo(str(in["repositoryId"]))
		if r == nil {
			return nil, "CreateIssuePayload"
		}
		i := map[string]any{"id": r.NextID, "number": r.NextIssue, "title": in["title"], "body": in["body"], "state": "open", "labels": []any{}, "comments": []any{}}
		r.NextID++
		r.NextIssue++
		r.Issues = append(r.Issues, i)
		s.changed(r)
		return map[string]any{"issue": i}, "CreateIssuePayload"
	case "Mutation.closeIssue":
		in, _ := a["input"].(map[string]any)
		i := s.gqlIssue(str(in["issueId"]))
		if i != nil {
			i["state"] = "closed"
		}
		return map[string]any{"issue": i}, "CloseIssuePayload"
	case "Mutation.createPullRequest":
		in, _ := a["input"].(map[string]any)
		r := s.gqlRepo(str(in["repositoryId"]))
		if r == nil {
			return nil, "CreatePullRequestPayload"
		}
		head, base := str(in["headRefName"]), str(in["baseRefName"])
		hsha, bsha := r.Branches[head], r.Branches[base]
		files := pullFiles(r, hsha, bsha)
		p := map[string]any{"id": r.NextID, "number": r.NextPull, "title": in["title"], "state": "open", "head": map[string]any{"ref": head, "sha": hsha}, "base": map[string]any{"ref": base, "sha": bsha}, "files": files, "merged": false}
		r.NextID++
		r.NextPull++
		r.Pulls = append(r.Pulls, p)
		return map[string]any{"pullRequest": p}, "CreatePullRequestPayload"
	case "Mutation.mergePullRequest":
		in, _ := a["input"].(map[string]any)
		p := s.gqlPull(str(in["pullRequestId"]))
		if p != nil {
			p["merged"] = true
			p["state"] = "closed"
		}
		return map[string]any{"pullRequest": p}, "MergePullRequestPayload"
	case "Mutation.addComment":
		in, _ := a["input"].(map[string]any)
		i := s.gqlIssue(str(in["subjectId"]))
		if i == nil {
			return nil, "AddCommentPayload"
		}
		c := map[string]any{"id": len(i["comments"].([]any)) + 1, "body": in["body"]}
		i["comments"] = append(i["comments"].([]any), c)
		return map[string]any{"commentEdge": map[string]any{"node": map[string]any{"id": "C_" + toString(c["id"]), "body": c["body"]}}}, "AddCommentPayload"
	case "CreateIssuePayload.issue", "CloseIssuePayload.issue":
		return o.(map[string]any)["issue"], "Issue"
	case "CreatePullRequestPayload.pullRequest", "MergePullRequestPayload.pullRequest":
		return o.(map[string]any)["pullRequest"], "PullRequest"
	case "Repository.id":
		return "R_" + digest(key(o.(*Repo).Owner, o.(*Repo).Name))[:16], "String"
	case "Node.id":
		if r, ok := o.(*Repo); ok {
			return "R_" + digest(key(r.Owner, r.Name))[:16], "String"
		}
		return nil, "ID"
	case "Node.name":
		if r, ok := o.(*Repo); ok {
			return r.Name, "String"
		}
	case "Node.nameWithOwner":
		if r, ok := o.(*Repo); ok {
			return r.Owner + "/" + r.Name, "String"
		}
	case "Repository.databaseId":
		return hashID(key(o.(*Repo).Owner, o.(*Repo).Name)), "Int"
	case "Repository.name":
		return o.(*Repo).Name, "String"
	case "Repository.nameWithOwner":
		r := o.(*Repo)
		return r.Owner + "/" + r.Name, "String"
	case "Repository.owner":
		return map[string]any{"login": o.(*Repo).Owner}, "User"
	case "Repository.description":
		return o.(*Repo).Description, "String"
	case "Repository.defaultBranchRef":
		return map[string]any{"name": "main"}, "Ref"
	case "Repository.viewerPermission":
		return "ADMIN", "RepositoryPermission"
	case "Repository.hasIssuesEnabled":
		return true, "Boolean"
	case "Repository.issue":
		for _, i := range o.(*Repo).Issues {
			if toString(i["number"]) == toString(a["number"]) {
				return i, "Issue"
			}
		}
		return nil, "Issue"
	case "Repository.pullRequest":
		for _, p := range o.(*Repo).Pulls {
			if toString(p["number"]) == toString(a["number"]) {
				return p, "PullRequest"
			}
		}
		return nil, "PullRequest"
	case "Repository.issues":
		r := o.(*Repo)
		list := []any{}
		desc := false
		if ob, ok := a["orderBy"].(map[string]any); ok {
			desc = ob["direction"] == "DESC"
		}
		for _, i := range r.Issues {
			if states, ok := a["states"].([]any); ok && len(states) > 0 {
				yes := false
				for _, x := range states {
					yes = yes || strings.EqualFold(str(x), str(i["state"]))
				}
				if !yes {
					continue
				}
			}
			list = append(list, i)
		}
		if after := str(a["after"]); after != "" {
			var cursor int
			if _, err := fmt.Sscanf(after, "cursor:%d", &cursor); err == nil {
				filtered := list[:0]
				for _, item := range list {
					n := item.(map[string]any)["number"].(int)
					if (!desc && n > cursor) || (desc && n < cursor) {
						filtered = append(filtered, item)
					}
				}
				list = filtered
			}
		}
		total := len(list)
		sort.SliceStable(list, func(i, j int) bool {
			x := list[i].(map[string]any)["number"].(int)
			y := list[j].(map[string]any)["number"].(int)
			if desc {
				return x > y
			}
			return x < y
		})
		first := intArg(a["first"], len(list))
		list = paginate(list, 1, first)
		cur := any(nil)
		if len(list) > 0 {
			cur = "cursor:" + toString(list[len(list)-1].(map[string]any)["number"])
		}
		return map[string]any{"nodes": list, "pageInfo": map[string]any{"hasNextPage": total > len(list), "endCursor": cur}}, "IssueConnection"
	case "Repository.pullRequests":
		r := o.(*Repo)
		a1 := []any{}
		for _, p := range r.Pulls {
			if str(p["state"]) == "open" {
				a1 = append(a1, p)
			}
		}
		return map[string]any{"nodes": paginate(a1, 1, intArg(a["first"], len(a1)))}, "PullRequestConnection"
	case "Issue.id":
		return "I_" + toString(o.(map[string]any)["id"]), "ID"
	case "PullRequest.id":
		return "P_" + toString(o.(map[string]any)["id"]), "ID"
	case "Issue.number", "PullRequest.number":
		return o.(map[string]any)["number"], "Int"
	case "Issue.title", "PullRequest.title":
		return o.(map[string]any)["title"], "String"
	case "Issue.body":
		return o.(map[string]any)["body"], "String"
	case "Issue.state", "PullRequest.state":
		if o.(map[string]any)["merged"] == true {
			return "MERGED", "State"
		}
		return strings.ToUpper(str(o.(map[string]any)["state"])), "State"
	case "PullRequest.merged":
		return o.(map[string]any)["merged"], "Boolean"
	case "PullRequest.headRefName":
		return o.(map[string]any)["head"].(map[string]any)["ref"], "String"
	case "PullRequest.baseRefName":
		return o.(map[string]any)["base"].(map[string]any)["ref"], "String"
	case "PullRequest.mergeable":
		return "MERGEABLE", "MergeableState"
	case "PullRequest.files":
		files, _ := o.(map[string]any)["files"].([]any)
		limit := intArg(a["first"], len(files))
		if limit < 0 {
			limit = 0
		}
		if limit > len(files) {
			limit = len(files)
		}
		nodes := make([]any, 0, limit)
		for _, entry := range files[:limit] {
			file, _ := entry.(map[string]any)
			path, ok := file["filename"]
			if !ok {
				path = file["path"]
			}
			nodes = append(nodes, map[string]any{"path": path})
		}
		return map[string]any{"nodes": nodes}, "PullRequestFiles"
	case "IssueConnection.nodes":
		return o.(map[string]any)["nodes"], "Issue"
	case "PullRequestConnection.nodes":
		return o.(map[string]any)["nodes"], "PullRequest"
	case "IssueConnection.pageInfo":
		return o.(map[string]any)["pageInfo"], "PageInfo"
	case "PageInfo.hasNextPage":
		return o.(map[string]any)["hasNextPage"], "Boolean"
	case "PageInfo.endCursor":
		return o.(map[string]any)["endCursor"], "String"
	case "PullRequestFiles.nodes":
		return o.(map[string]any)["nodes"], "PullRequestChangedFile"
	case "PullRequestChangedFile.path":
		return o.(map[string]any)["path"], "String"
	case "User.login":
		return o.(map[string]any)["login"], "String"
	case "__Type.name":
		return o.(map[string]any)["name"], "String"
	case "__Type.fields":
		name := str(o.(map[string]any)["name"])
		fields := map[string][]string{"Issue": {"id", "number", "title", "state", "body"}, "PullRequest": {"id", "number", "state", "merged", "headRefName", "baseRefName", "mergeable", "files"}, "Repository": {"id", "databaseId", "name", "nameWithOwner", "owner", "defaultBranchRef", "viewerPermission", "hasIssuesEnabled", "issue", "issues", "pullRequests", "pullRequest"}}
		out := []any{}
		for _, x := range fields[name] {
			out = append(out, map[string]any{"name": x})
		}
		return out, "__Field"
	case "__Type.enumValues":
		if str(o.(map[string]any)["name"]) == "SearchType" {
			return []any{map[string]any{"name": "ISSUE"}, map[string]any{"name": "REPOSITORY"}}, "__EnumValue"
		}
		return []any{}, "__EnumValue"
	case "Query.__typename":
		return "Query", "String"
	}
	if m, ok := o.(map[string]any); ok {
		return m[f.name], "Object"
	}
	return nil, "Object"
}
func (s *Store) gqlRepo(id string) *Repo {
	for _, r := range s.repos {
		if id == "R_"+digest(key(r.Owner, r.Name))[:16] {
			return r
		}
	}
	return nil
}
func (s *Store) gqlIssue(id string) map[string]any {
	for _, r := range s.repos {
		for _, i := range r.Issues {
			if id == "I_"+toString(i["id"]) {
				return i
			}
		}
	}
	return nil
}
func (s *Store) gqlPull(id string) map[string]any {
	for _, r := range s.repos {
		for _, p := range r.Pulls {
			if id == "P_"+toString(p["id"]) {
				return p
			}
		}
	}
	return nil
}
func hashID(s string) int64 { n, _ := strconv.ParseInt(digest(s)[:7], 16, 64); return n }
func intArg(v any, d int) int {
	if n, ok := v.(int); ok {
		return n
	}
	if n, ok := v.(float64); ok {
		return int(n)
	}
	return d
}
func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}
