package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// REST response helpers operate on the same Repo objects used by Git and GraphQL.
func (s *Store) restComplete(w http.ResponseWriter, req *http.Request, r *Repo, owner, name, tail string) {
	m := req.Method
	if tail == "" {
		switch m {
		case http.MethodGet:
			s.write(w, 200, repoJSON(r), nil)
		case http.MethodPatch:
			b := readJSON(req)
			if v, ok := b["description"]; ok {
				r.Description = str(v)
			}
			if v, ok := b["private"].(bool); ok {
				r.Private = v
			}
			s.changed(r)
			s.write(w, 200, repoJSON(r), nil)
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
	if tail == "/issues" {
		if m == http.MethodPost {
			b := readJSON(req)
			labels := []any{}
			if a, ok := b["labels"].([]any); ok {
				for _, v := range a {
					found := false
					for _, l := range r.Labels {
						if str(l["name"]) == str(v) {
							found = true
							labels = append(labels, l)
							break
						}
					}
					if !found {
						s.fail(w, 422, "Validation Failed")
						return
					}
				}
			}
			i := map[string]any{"id": r.NextID, "number": r.NextIssue, "title": str(b["title"]), "body": str(b["body"]), "state": "open", "labels": labels, "comments": []any{}, "assignee": nil, "milestone": nil, "created_at": "2000-01-01T00:00:00Z", "updated_at": "2000-01-01T00:00:00Z"}
			r.NextID++
			r.NextIssue++
			if a, ok := b["assignees"].([]any); ok && len(a) > 0 {
				i["assignee"] = map[string]any{"login": a[0]}
			}
			r.Issues = append(r.Issues, i)
			s.changed(r)
			s.write(w, 201, s.issue(r, i), nil)
			return
		}
		if m == http.MethodGet {
			state := req.URL.Query().Get("state")
			if state == "" {
				state = "open"
			}
			dir := req.URL.Query().Get("direction")
			if dir == "" {
				dir = "desc"
			}
			page, per := pageArgs(req)
			ck := fmt.Sprintf("%s:%s:%d:%d", state, dir, page, per)
			if b := r.IssueCache[ck]; b != nil {
				s.writeBytes(w, 200, b, nil)
				return
			}
			a := []map[string]any{}
			for _, i := range r.Issues {
				if state == "all" || str(i["state"]) == state {
					a = append(a, i)
				}
			}
			sort.SliceStable(a, func(i, j int) bool {
				ni, _ := a[i]["number"].(int)
				nj, _ := a[j]["number"].(int)
				if dir == "asc" {
					return ni < nj
				}
				return ni > nj
			})
			a = paginate(a, page, per)
			out := make([]any, 0, len(a))
			for _, i := range a {
				out = append(out, s.issue(r, i))
			}
			b, _ := jsonMarshal(out)
			r.IssueCache[ck] = b
			s.writeBytes(w, 200, b, nil)
			return
		}
	}
	if strings.HasPrefix(tail, "/issues/") {
		issueTail := strings.TrimPrefix(tail, "/issues/")
		if strings.HasSuffix(issueTail, "/labels") {
			s.issueLabels(w, req, r, strings.TrimSuffix(issueTail, "/labels"))
			return
		}
		s.restIssue(w, req, r, issueTail)
		return
	}
	if tail == "/labels" {
		if m == http.MethodGet {
			s.write(w, 200, r.Labels, nil)
			return
		}
		if m == http.MethodPost {
			b := readJSON(req)
			color, ok := b["color"].(string)
			if !ok {
				color = "ededed"
			}
			description, ok := b["description"].(string)
			if !ok {
				description = ""
			}
			l := map[string]any{"id": r.NextID, "name": str(b["name"]), "color": color, "default": false, "description": description}
			r.NextID++
			r.Labels = append(r.Labels, l)
			s.changed(r)
			s.write(w, 201, l, nil)
			return
		}
	}
	if tail == "/pulls" {
		if m == http.MethodPost {
			s.createPull(w, req, r)
			return
		}
		if m == http.MethodGet {
			state := req.URL.Query().Get("state")
			if state == "" {
				state = "open"
			}
			out := []any{}
			for _, p := range r.Pulls {
				if state == "all" || str(p["state"]) == state {
					out = append(out, s.pull(r, p))
				}
			}
			s.write(w, 200, out, nil)
			return
		}
	}
	if strings.HasPrefix(tail, "/pulls/") {
		s.restPull(w, req, r, strings.TrimPrefix(tail, "/pulls/"))
		return
	}
	if tail == "/branches" && m == http.MethodGet {
		out := []any{}
		for b, sha := range r.Branches {
			out = append(out, map[string]any{"name": b, "commit": map[string]any{"sha": sha}})
		}
		sort.Slice(out, func(i, j int) bool {
			return out[i].(map[string]any)["name"].(string) < out[j].(map[string]any)["name"].(string)
		})
		s.write(w, 200, out, nil)
		return
	}
	if tail == "/git/refs" && m == http.MethodPost {
		b := readJSON(req)
		ref := str(b["ref"])
		branch := strings.TrimPrefix(ref, "refs/heads/")
		sha := str(b["sha"])
		if sha == "" {
			sha = r.Branches["main"]
		}
		r.Branches[branch] = sha
		r.BranchCommits[branch] = append([]string{}, r.BranchCommits["main"]...)
		s.changed(r)
		s.write(w, 201, map[string]any{"ref": ref, "object": map[string]any{"sha": sha, "type": "commit"}}, nil)
		return
	}
	if tail == "/commits" && m == http.MethodGet {
		branch := req.URL.Query().Get("sha")
		if branch == "" {
			branch = "main"
		}
		ids := append([]string{}, r.BranchCommits[branch]...)
		tip := r.Branches[branch]
		if tip != "" {
			ids = append([]string{tip}, ids...)
		}
		out := []any{}
		seen := map[string]bool{}
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			for _, c := range r.Commits {
				if str(c["sha"]) == id {
					out = append(out, c)
				}
			}
		}
		s.write(w, 200, out, nil)
		return
	}
	if strings.HasPrefix(tail, "/contents/") {
		path := strings.TrimPrefix(tail, "/contents/")
		s.restContents(w, req, r, path)
		return
	}
	if tail == "/readme" && m == http.MethodGet {
		v, ok := r.Files["README.md"]
		if !ok {
			s.fail(w, 404, "Not Found")
			return
		}
		s.contentReply(w, "README.md", v)
		return
	}
	if strings.HasPrefix(tail, "/collaborators") {
		parts := strings.Split(strings.TrimPrefix(tail, "/collaborators"), "/")
		if len(parts) == 1 && m == http.MethodGet {
			out := []any{}
			for _, u := range r.Collaborators {
				out = append(out, map[string]any{"login": u})
			}
			s.write(w, 200, out, nil)
			return
		}
		if len(parts) == 2 {
			login := parts[1]
			if m == http.MethodPut {
				for _, u := range r.Collaborators {
					if u == login {
						s.empty(w, 204)
						return
					}
				}
				r.Collaborators = append(r.Collaborators, login)
				s.changed(r)
				s.empty(w, 201)
				return
			}
			if m == http.MethodDelete {
				out := r.Collaborators[:0]
				for _, u := range r.Collaborators {
					if u != login {
						out = append(out, u)
					}
				}
				r.Collaborators = out
				s.changed(r)
				s.empty(w, 204)
				return
			}
		}
	}
	if tail == "/forks" && m == http.MethodPost {
		fork := s.newRepo(owner, name+"-fork", r.Description)
		fork.Private = r.Private
		fork.Files = r.Files
		fork.Branches = r.Branches
		fork.Commits = r.Commits
		s.write(w, 202, repoJSON(fork), nil)
		return
	}
	if tail == "/forks" && m == http.MethodGet {
		out := []any{}
		for _, x := range s.repos {
			if strings.HasPrefix(x.Name, name+"-fork") {
				out = append(out, repoJSON(x))
			}
		}
		s.write(w, 200, out, nil)
		return
	}
	if tail == "/milestones" {
		if m == http.MethodGet {
			s.write(w, 200, r.Milestones, nil)
			return
		}
		if m == http.MethodPost {
			b := readJSON(req)
			ms := map[string]any{"id": r.NextID, "number": len(r.Milestones) + 1, "title": str(b["title"]), "state": "open", "description": str(b["description"]), "open_issues": 0, "closed_issues": 0, "created_at": "2000-01-01T00:00:00Z", "updated_at": "2000-01-01T00:00:00Z"}
			r.NextID++
			r.Milestones = append(r.Milestones, ms)
			s.changed(r)
			s.write(w, 201, ms, nil)
			return
		}
	}
	if strings.HasPrefix(tail, "/milestones/") {
		n, _ := strconv.Atoi(strings.TrimPrefix(tail, "/milestones/"))
		for _, ms := range r.Milestones {
			if ms["number"] == n {
				if m == http.MethodGet {
					s.write(w, 200, ms, nil)
					return
				}
				if m == http.MethodPatch {
					for k, v := range readJSON(req) {
						ms[k] = v
					}
					s.changed(r)
					s.write(w, 200, ms, nil)
					return
				}
				if m == http.MethodDelete {
					out := r.Milestones[:0]
					for _, x := range r.Milestones {
						if x["number"] != ms["number"] {
							out = append(out, x)
						}
					}
					r.Milestones = out
					s.empty(w, 204)
					return
				}
			}
		}
	}
	s.fail(w, 404, "Not Found")
}

func pageArgs(r *http.Request) (int, int) {
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	n, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if p < 1 {
		p = 1
	}
	if n < 1 {
		n = 30
	}
	return p, n
}
func paginate[T any](a []T, p, n int) []T {
	lo := (p - 1) * n
	if lo > len(a) {
		lo = len(a)
	}
	hi := lo + n
	if hi > len(a) {
		hi = len(a)
	}
	return a[lo:hi]
}
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
func (s *Store) writeBytes(w http.ResponseWriter, code int, b []byte, h map[string]string) {
	s.request++
	w.Header().Set("content-type", "application/json; charset=utf-8")
	w.Header().Set("content-length", strconv.Itoa(len(b)))
	w.Header().Set("x-github-request-id", fmt.Sprintf("FORGE%08x", s.request))
	for k, v := range h {
		w.Header().Set(k, v)
	}
	w.WriteHeader(code)
	_, _ = w.Write(b)
}
func (s *Store) restIssue(w http.ResponseWriter, req *http.Request, r *Repo, tail string) {
	parts := strings.Split(tail, "/")
	n, _ := strconv.Atoi(parts[0])
	var issue map[string]any
	for _, i := range r.Issues {
		if i["number"] == n {
			issue = i
			break
		}
	}
	if issue == nil {
		s.fail(w, 404, "Not Found")
		return
	}
	if len(parts) == 1 {
		if req.Method == http.MethodGet {
			s.write(w, 200, s.issue(r, issue), nil)
			return
		}
		if req.Method == http.MethodPatch {
			b := readJSON(req)
			for k, v := range b {
				switch k {
				case "state", "title", "body", "milestone", "assignees":
					issue[k] = v
				}
			}
			s.changed(r)
			s.write(w, 200, s.issue(r, issue), nil)
			return
		}
	}
	if len(parts) == 2 && parts[1] == "comments" {
		if req.Method == http.MethodGet {
			s.write(w, 200, issue["comments"], nil)
			return
		}
		if req.Method == http.MethodPost {
			b := readJSON(req)
			id := r.NextID
			r.NextID++
			c := map[string]any{"id": id, "body": str(b["body"]), "created_at": "2000-01-01T00:00:00Z"}
			issue["comments"] = append(issue["comments"].([]any), c)
			s.changed(r)
			s.write(w, 201, c, nil)
			return
		}
	}
	if len(parts) == 3 && parts[1] == "comments" {
		cid, _ := strconv.Atoi(parts[2])
		comments, _ := issue["comments"].([]any)
		for idx, raw := range comments {
			c, _ := raw.(map[string]any)
			if c != nil && (c["id"] == cid || fmt.Sprint(c["id"]) == fmt.Sprint(cid)) {
				if req.Method == http.MethodPatch {
					b := readJSON(req)
					if v, ok := b["body"]; ok {
						c["body"] = v
					}
					s.changed(r)
					s.write(w, 200, c, nil)
					return
				}
				if req.Method == http.MethodDelete {
					issue["comments"] = append(comments[:idx], comments[idx+1:]...)
					s.changed(r)
					s.empty(w, 204)
					return
				}
			}
		}
		s.fail(w, 404, "Not Found")
		return
	}
	s.fail(w, 404, "Not Found")
}
func (s *Store) createPull(w http.ResponseWriter, req *http.Request, r *Repo) {
	b := readJSON(req)
	head, base := str(b["head"]), str(b["base"])
	if base == "" {
		base = "main"
	}
	href := head
	if i := strings.LastIndex(href, ":"); i >= 0 {
		href = href[i+1:]
	}
	bref := base
	if i := strings.LastIndex(bref, ":"); i >= 0 {
		bref = bref[i+1:]
	}
	hs := r.Branches[href]
	if hs == "" {
		for _, c := range r.Commits {
			if str(c["sha"]) == head {
				hs = head
				break
			}
		}
		if hs == "" {
			hs = sha1String(head)
		}
	}
	bs := r.Branches[bref]
	if bs == "" {
		for _, c := range r.Commits {
			if str(c["sha"]) == base {
				bs = base
				break
			}
		}
		if bs == "" {
			bs = sha1String(base)
		}
	}
	files := pullFiles(r, hs, bs)
	p := map[string]any{"id": r.NextID, "number": r.NextPull, "title": str(b["title"]), "body": str(b["body"]), "state": "open", "merged": false, "head": map[string]any{"ref": href, "sha": hs}, "base": map[string]any{"ref": bref, "sha": bs}, "files": files, "created_at": "2000-01-01T00:00:00Z", "merged_at": nil}
	r.NextID++
	r.NextPull++
	r.Pulls = append(r.Pulls, p)
	s.changed(r)
	s.write(w, 201, s.pull(r, p), nil)
}
func (s *Store) pull(r *Repo, p map[string]any) map[string]any {
	v := map[string]any{}
	for k, x := range p {
		v[k] = x
	}
	n := p["number"]
	v["url"] = fmt.Sprintf("/api/v3/repos/%s/%s/pulls/%v", r.Owner, r.Name, n)
	v["html_url"] = fmt.Sprintf("http://forge/%s/%s/pull/%v", r.Owner, r.Name, n)
	v["issue_url"] = fmt.Sprintf("/api/v3/repos/%s/%s/issues/%v", r.Owner, r.Name, n)
	v["user"] = map[string]any{"login": r.Owner}
	v["mergeable"] = true
	v["changed_files"] = len(p["files"].([]any))
	v["additions"] = len(p["files"].([]any))
	v["deletions"] = 0
	v["commits"] = 1
	v["comments"] = 0
	v["review_comments"] = 0
	for _, k := range []string{"head", "base"} {
		obj := p[k].(map[string]any)
		obj["label"] = r.Owner + ":" + str(obj["ref"])
		obj["repo"] = repoJSON(r)
	}
	return v
}
func (s *Store) restPull(w http.ResponseWriter, req *http.Request, r *Repo, tail string) {
	parts := strings.Split(tail, "/")
	n, _ := strconv.Atoi(parts[0])
	var p map[string]any
	for _, x := range r.Pulls {
		if x["number"] == n {
			p = x
			break
		}
	}
	if p == nil {
		if len(parts) > 1 && parts[1] == "merge" {
			s.fail(w, 404, "Not Found")
			return
		}
		s.fail(w, 404, "Not Found")
		return
	}
	extra := ""
	if len(parts) > 1 {
		extra = parts[1]
	}
	switch extra {
	case "":
		if req.Method == http.MethodGet {
			s.write(w, 200, s.pull(r, p), nil)
			return
		}
	case "files":
		if req.Method == http.MethodGet {
			s.write(w, 200, p["files"], nil)
			return
		}
	case "commits":
		if req.Method == http.MethodGet {
			out := []any{}
			if len(r.Commits) > 0 {
				out = append(out, r.Commits[0])
			}
			s.write(w, 200, out, nil)
			return
		}
	case "reviews":
		if req.Method == http.MethodGet {
			s.write(w, 200, []any{}, nil)
			return
		}
	case "merge":
		if req.Method == http.MethodPut {
			for _, other := range r.Pulls {
				if other["merged"] == true && str(other["base"].(map[string]any)["ref"]) == str(p["base"].(map[string]any)["ref"]) && str(other["head"].(map[string]any)["ref"]) != str(p["head"].(map[string]any)["ref"]) && str(other["base"].(map[string]any)["sha"]) != str(p["base"].(map[string]any)["sha"]) {
					s.write(w, 405, map[string]string{"message": "Pull Request is not mergeable"}, nil)
					return
				}
			}
			if p["merged"] == true {
				s.write(w, 405, map[string]string{"message": "Pull Request is not mergeable"}, nil)
				return
			}
			p["state"] = "closed"
			p["merged"] = true
			p["merged_at"] = "2000-01-01T00:00:00Z"
			id := digest(fmt.Sprintf("merge:%s:%d", key(r.Owner, r.Name), n))
			p["merge_commit_sha"] = id
			p["base"].(map[string]any)["sha"] = id
			r.Branches[str(p["base"].(map[string]any)["ref"])] = id
			c := map[string]any{"sha": id, "commit": map[string]any{"message": fmt.Sprintf("Merge pull request #%d: %s", n, p["title"])}}
			r.Commits = append([]map[string]any{c}, r.Commits...)
			s.changed(r)
			s.write(w, 200, map[string]any{"sha": id, "merged": true, "message": "Pull Request successfully merged"}, nil)
			return
		}
	}
	s.fail(w, 404, "Not Found")
}
func (s *Store) restContents(w http.ResponseWriter, req *http.Request, r *Repo, path string) {
	if req.Method == http.MethodGet {
		if v, ok := r.Files[path]; ok {
			s.contentReply(w, path, v)
			return
		}
		prefix := path + "/"
		out := []any{}
		seen := map[string]bool{}
		for p, v := range r.Files {
			if strings.HasPrefix(p, prefix) {
				rest := strings.TrimPrefix(p, prefix)
				name := strings.Split(rest, "/")[0]
				if !seen[name] {
					seen[name] = true
					out = append(out, map[string]any{"name": name, "path": prefix + name, "type": "file", "sha": digest(v)})
				}
			}
		}
		if len(out) > 0 {
			s.write(w, 200, out, nil)
		} else {
			s.fail(w, 404, "Not Found")
		}
		return
	}
	if req.Method == http.MethodPut {
		b := readJSON(req)
		old, exists := r.Files[path]
		if exists && str(b["sha"]) != digest(old) {
			s.fail(w, 409, "sha does not match")
			return
		}
		data, _ := base64.StdEncoding.DecodeString(str(b["content"]))
		v := string(data)
		r.Files[path] = v
		branch := str(b["branch"])
		if branch == "" {
			branch = "main"
		}
		id := digest(fmt.Sprintf("%s:%s:%s:%s", key(r.Owner, r.Name), path, v, str(b["message"])))
		r.Branches[branch] = id
		c := map[string]any{"sha": id, "commit": map[string]any{"message": str(b["message"])}}
		r.Commits = append([]map[string]any{map[string]any{"sha": id, "commit": map[string]any{"message": str(b["message"])}}}, r.Commits...)
		r.BranchCommits[branch] = append(r.BranchCommits[branch], id)
		s.changed(r)
		st := 201
		if exists {
			st = 200
		}
		s.write(w, st, map[string]any{"content": map[string]any{"name": path, "path": path, "sha": digest(v), "size": len(v), "encoding": "base64"}, "commit": c}, nil)
		return
	}
	if req.Method == http.MethodDelete {
		if _, ok := r.Files[path]; !ok {
			s.fail(w, 404, "Not Found")
			return
		}
		delete(r.Files, path)
		id := digest("delete:" + path)
		s.changed(r)
		s.write(w, 200, map[string]any{"commit": map[string]any{"sha": id}}, nil)
		return
	}
	s.fail(w, 404, "Not Found")
}
func (s *Store) contentReply(w http.ResponseWriter, path, v string) {
	s.write(w, 200, map[string]any{"name": path, "path": path, "sha": digest(v), "size": len(v), "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(v)), "type": "file"}, nil)
}
func sha1String(v string) string { return digest(v) }

func (s *Store) empty(w http.ResponseWriter, status int) {
	s.request++
	w.Header().Set("x-github-request-id", fmt.Sprintf("FORGE%08x", s.request))
	w.WriteHeader(status)
}
func (s *Store) issueLabels(w http.ResponseWriter, req *http.Request, r *Repo, number string) {
	n, _ := strconv.Atoi(number)
	var issue map[string]any
	for _, i := range r.Issues {
		if i["number"] == n {
			issue = i
			break
		}
	}
	if issue == nil {
		s.fail(w, 404, "Not Found")
		return
	}
	labels, _ := issue["labels"].([]any)
	if labels == nil {
		labels = []any{}
	}
	switch req.Method {
	case http.MethodGet:
		s.write(w, 200, labels, nil)
	case http.MethodPost, http.MethodPut:
		b := readJSON(req)
		incoming, _ := b["labels"].([]any)
		for _, v := range incoming {
			found := false
			for _, l := range r.Labels {
				if str(l["name"]) == str(v) {
					found = true
					labels = append(labels, l)
					break
				}
			}
			if !found {
				s.fail(w, 422, "Validation Failed")
				return
			}
		}
		issue["labels"] = labels
		s.changed(r)
		s.write(w, 200, labels, nil)
	case http.MethodDelete:
		issue["labels"] = []any{}
		s.changed(r)
		s.empty(w, 200)
	default:
		s.fail(w, 404, "Not Found")
	}
}
