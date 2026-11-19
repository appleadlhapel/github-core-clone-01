package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type gqlField struct { name, alias string; args map[string]any; children []gqlField }
type gqlParser struct { tokens []string; at int; variables map[string]any }

var gqlToken = regexp.MustCompile(`\.\.\.|[_A-Za-z][_0-9A-Za-z]*|-?[0-9]+|"(?:\\.|[^"\\])*"|[!$():=@\[\]{|},]`)

func (s *Store) graphqlComplete(w http.ResponseWriter, req *http.Request) {
	var input any
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil { s.write(w, 400, map[string]any{"errors": []any{map[string]string{"message":"Invalid JSON"}}}, nil); return }
	if batch, ok := input.([]any); ok { out:=make([]any,0,len(batch)); for _, item:=range batch { out=append(out,s.graphqlResult(item)) }; s.write(w,200,out,nil); return }
	s.write(w,200,s.graphqlResult(input),nil)
}

func (s *Store) graphqlResult(raw any) map[string]any {
	input, _ := raw.(map[string]any); query, _ := input["query"].(string); vars, _ := input["variables"].(map[string]any)
	parser:=gqlParser{tokens:gqlToken.FindAllString(query,-1),variables:vars}; fields:=parser.parseFields(); isMutation:=strings.Contains(query,"mutation")
	root:="Query"; if isMutation { root="Mutation" }; data:=s.gqlResolveFields(root,nil,fields,vars); errors:=[]any{}; s.gqlValidateFields(root,fields,&errors); if len(errors)>0 { return map[string]any{"data":data,"errors":errors} }
	return map[string]any{"data":data}
}

func (s *Store) gqlValidateFields(typ string, fields []gqlField, errors *[]any) { known:=map[string]map[string]bool{"Query":{"viewer":true,"repository":true,"node":true,"__type":true,"__typename":true},"Mutation":{"createIssue":true,"closeIssue":true,"createPullRequest":true,"mergePullRequest":true,"addComment":true},"Repository":{"id":true,"databaseId":true,"name":true,"nameWithOwner":true,"owner":true,"defaultBranchRef":true,"viewerPermission":true,"hasIssuesEnabled":true,"issue":true,"issues":true,"pullRequests":true,"pullRequest":true},"Issue":{"id":true,"number":true,"title":true,"state":true,"body":true},"PullRequest":{"id":true,"number":true,"headRefName":true,"baseRefName":true,"mergeable":true,"state":true,"merged":true,"files":true},"IssueConnection":{"nodes":true,"pageInfo":true},"PullRequestConnection":{"nodes":true},"PageInfo":{"hasNextPage":true,"endCursor":true},"User":{"login":true},"__Type":{"name":true,"fields":true,"enumValues":true},"DefaultBranchRef":{"name":true},"PullRequestFiles":{"nodes":true},"PullRequestChangedFile":{"path":true},"CommentEdge":{"node":true},"Comment":{"id":true,"body":true}}; for _,f:=range fields { if f.name=="__fragment" { s.gqlValidateFields(f.alias, f.children, errors); continue }; if !known[typ][f.name] { *errors=append(*errors,map[string]string{"message":fmt.Sprintf("Cannot query field %q on type %q.",f.name,typ)}); continue }; child:=gqlChildType(typ,f.name,nil); if len(f.children)>0 && typ!="Mutation" { if f.name=="node" { child="Repository" }; s.gqlValidateFields(child,f.children,errors) } } }

func (p *gqlParser) next() string { if p.at>=len(p.tokens) { return "" }; t:=p.tokens[p.at]; p.at++; return t }
func (p *gqlParser) peek() string { if p.at>=len(p.tokens) { return "" }; return p.tokens[p.at] }
func (p *gqlParser) parseFields() []gqlField {
	for p.peek()!="{" && p.peek()!="" { p.next() }; if p.peek()!="{" { return nil }; p.next(); fields:=[]gqlField{}
	for p.peek()!="}" && p.peek()!="" { if p.peek()=="," { p.next(); continue }; if p.peek()=="..." { p.next(); if p.peek()=="on" { p.next(); fragmentType:=p.next(); fields=append(fields,gqlField{name:"__fragment",alias:fragmentType,args:map[string]any{},children:p.parseFields()}) } else { p.next() }; continue }
		f:=gqlField{name:p.next(),args:map[string]any{}}; if p.peek()==":" { p.next(); f.alias=f.name; f.name=p.next() }
		if p.peek()=="(" { p.next(); for p.peek()!=")" && p.peek()!="" { if p.peek()=="," { p.next(); continue }; key:=p.next(); if p.peek()==":" { p.next() }; f.args[key]=p.value() }; if p.peek()==")" { p.next() } }
		for p.peek()=="@" { p.next(); p.next() }; if p.peek()=="{" { f.children=p.parseFields() }; fields=append(fields,f)
	}; if p.peek()=="}" { p.next() }; return fields
}
func (p *gqlParser) value() any { t:=p.next(); switch t { case "$": return p.variables[p.next()]; case "[": a:=[]any{}; for p.peek()!="]"&&p.peek()!="" { if p.peek()=="," { p.next(); continue }; a=append(a,p.value()) }; if p.peek()=="]" { p.next() }; return a; case "{": m:=map[string]any{}; for p.peek()!="}"&&p.peek()!="" { if p.peek()=="," { p.next(); continue }; k:=p.next(); if p.peek()==":" { p.next() }; m[k]=p.value() }; if p.peek()=="}" { p.next() }; return m; case "true": return true; case "false": return false; case "null": return nil }
	if strings.HasPrefix(t,"\"") { var out string; _=json.Unmarshal([]byte(t),&out); return out }; if n,e:=strconv.Atoi(t); e==nil { return n }; return t
}

func (s *Store) gqlResolveFields(typ string, object any, fields []gqlField, vars map[string]any) map[string]any {
	out:=map[string]any{}; for _, f:=range fields { if f.name=="__fragment" { nested:=s.gqlResolveFields(f.alias,object,f.children,vars); for k,v:=range nested { out[k]=v }; continue }; args:=f.args; v:=s.gqlResolve(typ,object,f.name,args); if v==nil { out[fieldKey(f)]=nil; continue }; childType:=gqlChildType(typ,f.name,v); if len(f.children)>0 { if list,ok:=v.([]any); ok { a:=make([]any,0,len(list)); for _, item:=range list { a=append(a,s.gqlResolveFields(childType,item,f.children,vars)) }; v=a } else if m,ok:=v.(map[string]any); ok { v=s.gqlResolveFields(childType,m,f.children,vars) } else { v=s.gqlResolveFields(childType,v,f.children,vars) } }; out[fieldKey(f)]=v }; return out
}
func fieldKey(f gqlField) string { if f.alias!="" { return f.alias }; return f.name }
func gqlChildType(parent, field string, value any) string { switch field { case "viewer","owner": return "User"; case "repository": return "Repository"; case "node": if parent=="CommentEdge" { return "Comment" }; return "Repository"; case "commentEdge": return "CommentEdge"; case "issue","issues": if field=="issues" { return "IssueConnection" }; return "Issue"; case "pullRequest","pullRequests": if field=="pullRequests" { return "PullRequestConnection" }; return "PullRequest"; case "files": return "PullRequestFiles"; case "nodes": if parent=="IssueConnection" { return "Issue" }; if parent=="PullRequestFiles" { return "PullRequestChangedFile" }; return "PullRequest"; case "pageInfo": return "PageInfo"; case "defaultBranchRef": return "DefaultBranchRef"; case "fields","enumValues": return "__Type"; case "__type": return "__Type" }; if m,ok:=value.(map[string]any); ok { if _,yes:=m["path"]; yes { return "PullRequestChangedFile" } }; return parent }

func (s *Store) gqlResolve(typ string, object any, field string, args map[string]any) any {
	obj,_:=object.(map[string]any)
	switch typ+"."+field {
	case "Query.viewer": if s.owner=="" { return map[string]any{"login":"test-user"} }; return map[string]any{"login":s.owner}
	case "Query.repository": r:=s.repo(str(args["owner"]),str(args["name"])); if r==nil { return nil }; return r
	case "Query.node": n:=s.gqlNode(str(args["id"])); if n==nil { return nil }; if r,ok:=n.(*Repo); ok && r==nil { return nil }; return n
	case "Query.__type": return map[string]any{"name":args["name"]}
	case "Query.__typename": return "Query"
	case "Mutation.createIssue": in,_:=args["input"].(map[string]any); r:=s.gqlRepo(str(in["repositoryId"])); if r==nil { return nil }; i:=map[string]any{"id":r.NextID,"number":r.NextIssue,"title":in["title"],"body":in["body"],"state":"open","labels":[]any{},"comments":[]any{}}; r.NextID++; r.NextIssue++; r.Issues=append(r.Issues,i); s.changed(r); return map[string]any{"issue":i}
	case "Mutation.closeIssue": in,_:=args["input"].(map[string]any); issue:=s.gqlIssue(str(in["issueId"])); if issue==nil{return nil}; issue["state"]="closed"; return map[string]any{"issue":issue}
	case "Mutation.createPullRequest": in,_:=args["input"].(map[string]any); r:=s.gqlRepo(str(in["repositoryId"])); if r==nil{return nil}; head:=str(in["headRefName"]); base:=str(in["baseRefName"]); if base=="" || r.Branches[base]=="" { base="main" }; p:=map[string]any{"id":r.NextID,"number":r.NextPull,"title":in["title"],"body":"","state":"open","merged":false,"head":map[string]any{"ref":head,"sha":r.Branches[head]},"base":map[string]any{"ref":base,"sha":r.Branches[base]},"files":pullFiles(r,r.Branches[head],r.Branches[base])}; r.NextID++; r.NextPull++; r.Pulls=append(r.Pulls,p); return map[string]any{"pullRequest":p}
	case "Mutation.mergePullRequest": in,_:=args["input"].(map[string]any); p:=s.gqlPull(str(in["pullRequestId"])); if p==nil{return nil}; p["merged"]=true;p["state"]="closed";return map[string]any{"pullRequest":p}
	case "Mutation.addComment": in,_:=args["input"].(map[string]any); i:=s.gqlIssue(str(in["subjectId"])); if i==nil{return nil}; comments,_:=i["comments"].([]any); c:=map[string]any{"id":len(comments)+1,"body":in["body"]};i["comments"]=append(comments,c);return map[string]any{"commentEdge":map[string]any{"node":map[string]any{"id":fmt.Sprintf("C_%v",c["id"]),"body":c["body"]}}}
	}
	if object == nil { return nil }; if r,ok:=object.(*Repo); ok && r==nil { return nil }
	switch typ+"."+field {
	case "Repository.id": r,_:=object.(*Repo);if r==nil{return nil};return "R_"+digest(key(r.Owner,r.Name))[:16]
	case "Repository.databaseId": r,_:=object.(*Repo);if r==nil{return nil}; n,_:=strconv.ParseInt(digest(key(r.Owner,r.Name))[:7],16,64);return n
	case "Repository.name": r,_:=object.(*Repo);if r==nil{return nil};return r.Name
	case "Repository.nameWithOwner": r,_:=object.(*Repo);if r==nil{return nil};return r.Owner+"/"+r.Name
	case "Repository.owner": r,_:=object.(*Repo);if r==nil{return nil};return map[string]any{"login":r.Owner}
	case "Repository.defaultBranchRef": return map[string]any{"name":"main"}
	case "Repository.viewerPermission": return "ADMIN"
	case "Repository.hasIssuesEnabled": return true
	case "Repository.issue": r,_:=object.(*Repo);if r==nil{return nil}; return s.numbered(r.Issues,args["number"])
	case "Repository.pullRequest": r,_:=object.(*Repo);if r==nil{return nil}; return s.numbered(r.Pulls,args["number"])
	case "Repository.issues": r,_:=object.(*Repo); if r==nil { return nil }; items:=[]any{}; for _,i:=range r.Issues { if states,ok:=args["states"].([]any); ok && len(states)>0 { match:=false; for _,state:=range states { if strings.EqualFold(str(state),str(i["state"])) {match=true} }; if !match {continue} }; items=append(items,i) }; desc:=false; if order,ok:=args["orderBy"].(map[string]any); ok { desc=strings.EqualFold(str(order["direction"]),"DESC") }; sort.SliceStable(items,func(i,j int)bool { a:=intArg(items[i].(map[string]any)["number"],0); b:=intArg(items[j].(map[string]any)["number"],0); if desc{return a>b}; return a<b }); if after:=str(args["after"]); strings.HasPrefix(after,"cursor:") { n,e:=strconv.Atoi(strings.TrimPrefix(after,"cursor:")); if e==nil { filtered:=[]any{}; for _,item:=range items { number:=intArg(item.(map[string]any)["number"],0); if (desc && number<n) || (!desc && number>n) { filtered=append(filtered,item) } }; items=filtered } }; first:=intArg(args["first"],len(items)); hasNext:=first<len(items); if hasNext { items=items[:first] }; var cursor any; if len(items)>0 { cursor=fmt.Sprintf("cursor:%v",items[len(items)-1].(map[string]any)["number"]) }; return map[string]any{"nodes":items,"pageInfo":map[string]any{"hasNextPage":hasNext,"endCursor":cursor}}
	case "Repository.pullRequests": r,_:=object.(*Repo);if r==nil{return nil}; items:=[]any{}; for _,p:=range r.Pulls { items=append(items,p) }; first:=intArg(args["first"],len(items));if first<len(items){items=items[:first]};return map[string]any{"nodes":items}
	case "PullRequest.files": p,_:=object.(map[string]any); files,_:=p["files"].([]any); first:=intArg(args["first"],len(files)); if first<len(files){files=files[:first]}; out:=[]any{}; for _,f:=range files { m,_:=f.(map[string]any); out=append(out,map[string]any{"path":m["filename"]}) }; return map[string]any{"nodes":out}
	case "PullRequest.headRefName","PullRequest.baseRefName": p,_:=object.(map[string]any); which:="head"; if field=="baseRefName" { which="base" }; m,_:=p[which].(map[string]any);return m["ref"]
	case "PullRequest.mergeable": return "MERGEABLE"
	case "Issue.id": i,_:=object.(map[string]any);return fmt.Sprintf("I_%v",i["id"])
	case "PullRequest.id": i,_:=object.(map[string]any);return fmt.Sprintf("P_%v",i["id"])
	case "Issue.state","PullRequest.state": m,_:=object.(map[string]any);if m["merged"]==true{return "MERGED"};return strings.ToUpper(str(m["state"]))
	case "PullRequest.merged": m,_:=object.(map[string]any);return m["merged"]==true
	case "Issue.number","PullRequest.number","Issue.title","PullRequest.title","Issue.body","PullRequest.body","User.login","PullRequestChangedFile.path","IssueConnection.nodes","PullRequestConnection.nodes","IssueConnection.pageInfo","PageInfo.hasNextPage","PageInfo.endCursor","__Type.name": return obj[field]
	case "__Type.fields": m,_:=object.(map[string]any); names:=map[string][]string{"Issue":{"id","number","title","state","body"},"PullRequest":{"id","number","headRefName","baseRefName","mergeable","state","merged","files"},"Repository":{"id","databaseId","name","nameWithOwner","owner","defaultBranchRef","viewerPermission","hasIssuesEnabled","issue","issues","pullRequests","pullRequest"}}; out:=[]any{}; for _,name:=range names[str(m["name"])] {out=append(out,map[string]any{"name":name})}; return out
	case "__Type.enumValues": m,_:=object.(map[string]any); if m["name"]=="SearchType" { return []any{map[string]any{"name":"ISSUE"},map[string]any{"name":"REPOSITORY"}} }; return []any{}
	}
	if obj!=nil { return obj[field] }; return nil
}

func (s *Store) gqlNode(id string) any { if r:=s.gqlRepo(id); r!=nil{return r};if i:=s.gqlIssue(id);i!=nil{return i};return s.gqlPull(id) }
func (s *Store) gqlRepo(id string) *Repo { for _,r:=range s.repos { if id=="R_"+digest(key(r.Owner,r.Name))[:16] {return r} };return nil }
func (s *Store) gqlIssue(id string) map[string]any { for _,r:=range s.repos { for _,i:=range r.Issues { if id==fmt.Sprintf("I_%v",i["id"]){return i} } };return nil }
func (s *Store) gqlPull(id string) map[string]any { for _,r:=range s.repos { for _,p:=range r.Pulls { if id==fmt.Sprintf("P_%v",p["id"]){return p} } };return nil }
func (s *Store) numbered(items []map[string]any, number any) map[string]any { n:=intArg(number,-1); for _,item:=range items { if intArg(item["number"],-2)==n{return item} };return nil }
func intArg(value any, fallbackValue int) int { switch v:=value.(type) { case int:return v;case float64:return int(v);case json.Number:n,_:=v.Int64();return int(n) };return fallbackValue }
