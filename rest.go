package main

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

func (s *Store) restComplete(w http.ResponseWriter, req *http.Request, repo *Repo, owner, name, tail string) {
	method := req.Method
	if tail == "" {
		switch method {
		case http.MethodGet:
			s.write(w, 200, repoJSON(repo), nil)
		case http.MethodPatch:
			b, err := readJSONStrict(req)
			if err != nil { s.fail(w, 400, "Invalid JSON"); return }
			if v, ok := b["description"].(string); ok { repo.Description = v }
			s.changed(repo)
			s.write(w, 200, repoJSON(repo), nil)
		case http.MethodDelete:
			delete(s.repos, key(owner, name))
			s.request++
			w.Header().Set("x-github-request-id", fmt.Sprintf("FORGE%08x", s.request))
			w.WriteHeader(204)
		default:
			s.fail(w, 404, "Not Found")
		}
		return
	}
	if tail == "/issues" && method == http.MethodPost {
		b, err := readJSONStrict(req); if err != nil { s.fail(w, 400, "Invalid JSON"); return }
		labels, _ := b["labels"].([]any); if labels == nil { labels = []any{} }; labelObjects := []any{}
		for _, label := range labels { name := str(label); found := false; for _, known := range repo.Labels { if known["name"] == name { labelObjects = append(labelObjects, known); found = true; break } }; if !found { s.fail(w, 422, "Validation Failed"); return } }
		i := map[string]any{"id": repo.NextID, "number": repo.NextIssue, "title": str(b["title"]), "body": str(b["body"]), "state": "open", "labels": labelObjects, "comments": []any{}, "assignee": nil, "milestone": nil, "created_at": "2000-01-01T00:00:00Z", "updated_at": "2000-01-01T00:00:00Z"}
		repo.NextID++; repo.NextIssue++; repo.Issues = append(repo.Issues, i); s.changed(repo)
		s.write(w, 201, s.issue(repo, i), nil); return
	}
	if tail == "/issues" && method == http.MethodGet {
		state := req.URL.Query().Get("state"); if state == "" { state = "open" }
		direction := req.URL.Query().Get("direction"); page, per := queryInt(req, "page", 1), queryInt(req, "per_page", 30)
		items := []any{}; list := append([]map[string]any{}, repo.Issues...)
		if state != "all" { filtered := list[:0]; for _, i := range list { if i["state"] == state { filtered = append(filtered, i) } }; list = filtered }
		sort.SliceStable(list, func(i, j int) bool { a, _ := list[i]["number"].(int); b, _ := list[j]["number"].(int); if direction == "asc" { return a < b }; return a > b })
		for _, i := range pageSlice(len(list), page, per) { items = append(items, s.issue(repo, list[i])) }; s.write(w, 200, items, nil); return
	}
	if strings.HasPrefix(tail, "/issues/") { s.restIssue(w, req, repo, strings.TrimPrefix(tail, "/issues/")); return }
	if tail == "/labels" {
		if method == http.MethodGet { s.write(w, 200, repo.Labels, nil); return }
		if method == http.MethodPost { b, err := readJSONStrict(req); if err != nil { s.fail(w, 400, "Invalid JSON"); return }; l := map[string]any{"id": repo.NextID, "name": str(b["name"]), "color": fallback(b["color"], "ededed"), "default": false, "description": fallback(b["description"], "")}; repo.NextID++; repo.Labels = append(repo.Labels, l); s.changed(repo); s.write(w, 201, l, nil); return }
	}
	if tail == "/pulls" && method == http.MethodPost { s.restCreatePull(w, req, repo); return }
	if tail == "/pulls" && method == http.MethodGet { state := req.URL.Query().Get("state"); if state == "" { state = "open" }; out := []any{}; for _, p := range repo.Pulls { if state == "all" || p["state"] == state { out = append(out, pullResponse(repo, p)) } }; s.write(w, 200, out, nil); return }
	if strings.HasPrefix(tail, "/pulls/") { s.restPull(w, req, repo, strings.TrimPrefix(tail, "/pulls/")); return }
	if strings.HasPrefix(tail, "/contents") { s.restContents(w, req, repo, owner, name, tail); return }
	if tail == "/branches" && method == http.MethodGet { out := []any{}; for branch, sha := range repo.Branches { out = append(out, map[string]any{"name": branch, "commit": map[string]any{"sha": sha}}) }; sort.Slice(out, func(i,j int) bool { return out[i].(map[string]any)["name"].(string) < out[j].(map[string]any)["name"].(string) }); s.write(w, 200, out, nil); return }
	if tail == "/git/refs" && method == http.MethodPost { b := readJSON(req); ref := str(b["ref"]); if ref == "" { ref = "refs/heads/main" }; branch := strings.TrimPrefix(ref, "refs/heads/"); sha := str(b["sha"]); if sha == "" { sha = repo.Branches["main"] }; repo.Branches[branch] = sha; repo.BranchCommits[branch] = append([]string{}, repo.BranchCommits["main"]...); s.changed(repo); s.write(w, 201, map[string]any{"ref": ref, "object": map[string]any{"sha": sha, "type": "commit"}}, nil); return }
	if tail == "/commits" && method == http.MethodGet { branch := req.URL.Query().Get("sha"); if branch == "" { branch = "main" }; tip := repo.Branches[branch]; out := []any{}; for _, c := range repo.Commits { if c["sha"] == tip { out = append(out, c) } else { for _, id := range repo.BranchCommits[branch] { if id == c["sha"] { out = append(out, c); break } } } }; s.write(w, 200, out, nil); return }
	if tail == "/merges" && method == http.MethodPost { s.write(w, 201, map[string]any{"sha": restCommit(s, repo, "Update files", "main")}, nil); return }
	s.fail(w, 404, "Not Found")
}

func (s *Store) restIssue(w http.ResponseWriter, req *http.Request, repo *Repo, tail string) {
	parts := strings.Split(tail, "/"); n, err := strconv.Atoi(parts[0]); if err != nil { s.fail(w, 404, "Not Found"); return }
	var issue map[string]any; for _, item := range repo.Issues { if item["number"] == n { issue = item; break } }; if issue == nil { s.fail(w, 404, "Not Found"); return }
	extra := strings.Join(parts[1:], "/")
	if extra == "" && req.Method == http.MethodGet { s.write(w, 200, s.issue(repo, issue), nil); return }
	if extra == "" && req.Method == http.MethodPatch { b, e := readJSONStrict(req); if e != nil { s.fail(w, 400, "Invalid JSON"); return }; for _, k := range []string{"title", "body", "state"} { if v, ok := b[k]; ok { issue[k] = v } }; if raw, ok := b["labels"].([]any); ok { labels := []any{}; for _, item := range raw { name := str(item); for _, known := range repo.Labels { if known["name"] == name { labels = append(labels, known); break } } }; issue["labels"] = labels }; s.changed(repo); s.write(w, 200, s.issue(repo, issue), nil); return }
	comments, _ := issue["comments"].([]any)
	if extra == "comments" && req.Method == http.MethodGet { s.write(w, 200, comments, nil); return }
	if extra == "comments" && req.Method == http.MethodPost { b := readJSON(req); c := map[string]any{"id": repo.NextID, "body": str(b["body"]), "user": map[string]any{"login": repo.Owner}}; repo.NextID++; issue["comments"] = append(comments, c); s.changed(repo); s.write(w, 201, c, nil); return }
	s.fail(w, 404, "Not Found")
}

func (s *Store) restCreatePull(w http.ResponseWriter, req *http.Request, repo *Repo) {
	b, err := readJSONStrict(req); if err != nil { s.fail(w, 400, "Invalid JSON"); return }
	head, base := str(b["head"]), str(b["base"]); if base == "" { base = "main" }; href, bref := lastRef(head), lastRef(base)
	hsha := repo.Branches[href]; if hsha == "" { hsha = digest(head) }; bsha := repo.Branches[bref]; if bsha == "" { bref = "main"; bsha = repo.Branches[bref] }
	files := pullFiles(repo, hsha, bsha); mapped := []any{}; for _, raw := range files { filename := ""; switch f := raw.(type) { case string: filename = f; case map[string]any: filename = str(f["filename"]) }; mapped = append(mapped, map[string]any{"filename": filename, "status": "modified", "additions": 1, "deletions": 0, "changes": 1, "blob_url": "", "raw_url": "", "contents_url": ""}) }
	p := map[string]any{"id": repo.NextID, "number": repo.NextPull, "title": str(b["title"]), "body": str(b["body"]), "state": "open", "merged": false, "head": map[string]any{"ref": href, "sha": hsha}, "base": map[string]any{"ref": bref, "sha": bsha}, "files": mapped, "created_at": "2000-01-01T00:00:00Z"}; repo.NextID++; repo.NextPull++; repo.Pulls = append(repo.Pulls, p); s.changed(repo); s.write(w, 201, pullResponse(repo, p), nil)
}

func (s *Store) restPull(w http.ResponseWriter, req *http.Request, repo *Repo, tail string) {
	parts := strings.Split(tail, "/"); n, err := strconv.Atoi(parts[0]); if err != nil { s.fail(w, 404, "Not Found"); return }; var p map[string]any; for _, item := range repo.Pulls { if item["number"] == n { p = item; break } }; if p == nil { s.fail(w, 404, "Not Found"); return }; extra := strings.Join(parts[1:], "/")
	if extra == "" && req.Method == http.MethodGet { s.write(w, 200, pullResponse(repo, p), nil); return }; if extra == "files" && req.Method == http.MethodGet { s.write(w, 200, p["files"], nil); return }
	if extra == "merge" && req.Method == http.MethodPut { base,_:=p["base"].(map[string]any); head,_:=p["head"].(map[string]any); conflict:=p["merged"]==true; for _, other:=range repo.Pulls { ob,_:=other["base"].(map[string]any); oh,_:=other["head"].(map[string]any); if other["number"]!=p["number"] && other["merged"]==true && ob["ref"]==base["ref"] && oh["ref"]!=head["ref"] && ob["sha"]!=base["sha"] { conflict=true } }; if conflict { s.write(w, 405, map[string]string{"message":"Pull Request is not mergeable"}, nil); return }; sha := restCommit(s, repo, "Merge pull request", "main"); p["merged"] = true; p["state"] = "closed"; p["merge_commit_sha"] = sha; base["sha"] = sha; p["merged_at"] = "2000-01-01T00:00:00Z"; s.changed(repo); s.write(w, 200, map[string]any{"sha":sha,"merged":true,"message":"Pull Request successfully merged"}, nil); return }
	s.fail(w, 404, "Not Found")
}

func (s *Store) restContents(w http.ResponseWriter, req *http.Request, repo *Repo, owner, name, tail string) {
	path := strings.TrimPrefix(tail, "/contents"); path, _ = url.PathUnescape(strings.TrimPrefix(path, "/"))
	if req.Method == http.MethodGet { if value, ok := repo.Files[path]; ok { s.write(w, 200, map[string]any{"name":lastRef(path),"path":path,"sha":digest(value),"size":len(value),"type":"file","encoding":"base64","content":base64.StdEncoding.EncodeToString([]byte(value))}, nil); return }; prefix := path; if prefix != "" { prefix += "/" }; listing := []any{}; seen := map[string]bool{}; for p, value := range repo.Files { if strings.HasPrefix(p, prefix) { remainder := strings.TrimPrefix(p, prefix); segment := strings.Split(remainder, "/")[0]; if !seen[segment] { seen[segment]=true; listing=append(listing,map[string]any{"name":segment,"path":prefix+segment,"sha":digest(value),"size":len(value),"type":"file","url":fmt.Sprintf("/api/v3/repos/%s/%s/contents/%s",owner,name,prefix+segment)}) } } }; if len(listing)>0 { s.write(w,200,listing,nil) } else { s.fail(w,404,"Not Found") }; return }
	if req.Method == http.MethodPut { b,e := readJSONStrict(req); if e != nil { s.fail(w,400,"Invalid JSON"); return }; old, exists := repo.Files[path]; oldSHA := digest(old); if (exists && str(b["sha"]) != oldSHA) || (!exists && b["sha"] != nil && b["sha"] != "") { s.fail(w,409,"sha does not match"); return }; value, _ := base64.StdEncoding.DecodeString(str(b["content"])); repo.Files[path]=string(value); branch:=str(b["branch"]); if branch=="" { branch="main" }; sha:=restCommit(s,repo,fallback(b["message"],"Update").(string),branch); s.changed(repo); s.write(w,map[bool]int{true:201,false:200}[!exists],map[string]any{"content":map[string]any{"name":lastRef(path),"path":path,"sha":digest(string(value)),"size":len(value),"encoding":"base64"},"commit":map[string]any{"sha":sha,"message":fallback(b["message"],"Update")}},nil); return }
	if req.Method == http.MethodDelete { b:=readJSON(req); old,exists:=repo.Files[path]; if !exists { s.fail(w,404,"Not Found"); return }; if b["sha"]!=nil && str(b["sha"])!=digest(old) { s.fail(w,409,"sha does not match"); return }; delete(repo.Files,path); branch:=str(b["branch"]); if branch=="" { branch="main" }; sha:=restCommit(s,repo,fallback(b["message"],"Delete").(string),branch); s.changed(repo); s.write(w,200,map[string]any{"commit":map[string]any{"sha":sha}},nil); return }
	s.fail(w,404,"Not Found")
}

func restCommit(s *Store, repo *Repo, message, branch string) string { id:=digest(fmt.Sprintf("%s:%d:%s:%v",key(repo.Owner,repo.Name),len(repo.Commits),message,repo.Files)); c:=map[string]any{"sha":id,"commit":map[string]any{"message":message,"author":map[string]any{"name":"User","email":"user@example.com"},"committer":map[string]any{"name":"User","email":"user@example.com"}},"parents":[]any{},"html_url":fmt.Sprintf("http://forge/%s/%s/commit/%s",repo.Owner,repo.Name,id)}; repo.Commits=append([]map[string]any{c},repo.Commits...); repo.Branches[branch]=id; repo.BranchCommits[branch]=append([]string{id},repo.BranchCommits[branch]...); s.changed(repo); return id }
func pullResponse(repo *Repo, p map[string]any) map[string]any { out:=map[string]any{}; for k,v:=range p { out[k]=v }; head,_:=p["head"].(map[string]any); base,_:=p["base"].(map[string]any); out["url"]=fmt.Sprintf("/api/v3/repos/%s/%s/pulls/%v",repo.Owner,repo.Name,p["number"]); out["html_url"]=fmt.Sprintf("http://forge/%s/%s/pull/%v",repo.Owner,repo.Name,p["number"]); out["issue_url"]=fmt.Sprintf("/api/v3/repos/%s/%s/issues/%v",repo.Owner,repo.Name,p["number"]); out["user"]=map[string]any{"login":repo.Owner}; out["head"]=map[string]any{"label":repo.Owner+":"+str(head["ref"]),"ref":head["ref"],"sha":head["sha"],"repo":repoJSON(repo)}; out["base"]=map[string]any{"label":repo.Owner+":"+str(base["ref"]),"ref":base["ref"],"sha":base["sha"],"repo":repoJSON(repo)}; out["merged_at"]=p["merged_at"]; out["mergeable"]=true; files,_:=p["files"].([]any); out["changed_files"]=len(files); out["additions"]=len(files); out["deletions"]=0; out["commits"]=1; out["comments"]=0; out["review_comments"]=0; return out }
func queryInt(req *http.Request, key string, fallbackValue int) int { n,e:=strconv.Atoi(req.URL.Query().Get(key)); if e!=nil || n<1 { return fallbackValue }; return n }
func pageSlice(length, page, per int) []int { start:=(page-1)*per; if start>=length { return []int{} }; end:=start+per; if end>length { end=length }; out:=make([]int,0,end-start); for i:=start;i<end;i++ { out=append(out,i) }; return out }
func fallback(value any, defaultValue any) any { if value==nil || value=="" { return defaultValue }; return value }
func lastRef(value string) string { parts:=strings.Split(value, ":"); return parts[len(parts)-1] }
