package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

func (s *Store) mcp(w http.ResponseWriter, req *http.Request) {
	var q map[string]any
	if err:=json.NewDecoder(req.Body).Decode(&q);err!=nil { s.write(w,400,map[string]any{"jsonrpc":"2.0","id":nil,"error":map[string]any{"code":-32700,"message":"Parse error"}},nil);return }
	id:=q["id"]; method:=str(q["method"]); params,_:=q["params"].(map[string]any)
	reply:=func(result any){s.write(w,200,map[string]any{"jsonrpc":"2.0","id":id,"result":result},nil)}
	fail:=func(code int,message string){s.write(w,200,map[string]any{"jsonrpc":"2.0","id":id,"error":map[string]any{"code":code,"message":message}},nil)}
	switch method {
	case "notifications/initialized": s.request++;w.Header().Set("x-github-request-id",fmt.Sprintf("FORGE%08x",s.request));w.WriteHeader(202);return
	case "initialize": version:=str(params["protocolVersion"]);if version==""{version="2025-11-25"};reply(map[string]any{"protocolVersion":version,"capabilities":map[string]any{"tools":map[string]any{}},"serverInfo":map[string]string{"name":"github-core-clone","version":"1.0.0"}});return
	case "server/discover": reply(map[string]any{"supportedVersions":[]string{"2026-07-28","2025-11-25"},"serverInfo":map[string]string{"name":"github-core-clone","version":"1.0.0"},"capabilities":map[string]any{"tools":map[string]any{}}});return
	case "ping": reply(map[string]any{});return
	case "tools/list": reply(map[string]any{"tools":mcpToolList()});return
	case "tools/call": s.mcpCall(params,reply,fail);return
	default: fail(-32601,"Method not found");return
	}
}

func mcpToolList() []any {
	definitions:=[]struct{name,description string; props map[string]string}{
		{"create_repo","Create a repository",map[string]string{"name":"string"}},
		{"get_repo","Get a repository",map[string]string{"owner":"string","repo":"string"}},
		{"create_issue","Create an issue",map[string]string{"owner":"string","repo":"string","title":"string","body":"string"}},
		{"list_issues","List issues",map[string]string{"owner":"string","repo":"string","state":"string"}},
		{"create_pull_request","Create a pull request",map[string]string{"owner":"string","repo":"string","title":"string","head":"string","base":"string"}},
		{"merge_pull_request","Merge a pull request",map[string]string{"owner":"string","repo":"string","pull_number":"number"}},
		{"search_repositories","Search repositories",map[string]string{"query":"string"}},
	}; out:=[]any{};for _,d:=range definitions { properties:=map[string]any{};for key,kind:=range d.props {properties[key]=map[string]string{"type":kind}};out=append(out,map[string]any{"name":d.name,"description":d.description,"inputSchema":map[string]any{"type":"object","properties":properties}}) };return out
}

func (s *Store) mcpCall(params map[string]any, reply func(any), fail func(int,string)) {
	name:=strings.ToLower(str(params["name"])); if at:=strings.LastIndex(name,"__");at>=0{name=name[at+2:]}; args,_:=params["arguments"].(map[string]any)
	owner:=str(args["owner"]);if owner==""{owner=s.owner};if owner==""{owner="test-user"}; repo:=s.repo(owner,str(args["repo"]))
	textResult:=func(value any) any { data,_:=json.Marshal(value); return map[string]any{"content":[]any{map[string]any{"type":"text","text":string(data)}},"structuredContent":value} }
	switch name {
	case "create_repo": repoName:=str(args["name"]);if repoName==""{fail(-32602,"name is required");return};if s.repos[key(s.owner,repoName)]!=nil||s.repos[key("",repoName)]!=nil{fail(-32602,"repository already exists");return};created:=s.newRepo("",repoName,str(args["description"]));if s.owner!=""{s.learn(s.owner)};reply(textResult(repoJSON(created)));return
	case "get_repo": if repo==nil{fail(-32602,"repository not found");return};reply(textResult(repoJSON(repo)));return
	case "create_issue": if repo==nil{fail(-32602,"repository not found");return};issue:=map[string]any{"id":repo.NextID,"number":repo.NextIssue,"title":args["title"],"body":fallback(args["body"],""),"state":"open","labels":[]any{},"comments":[]any{}};repo.NextID++;repo.NextIssue++;repo.Issues=append(repo.Issues,issue);s.changed(repo);reply(textResult(s.issue(repo,issue)));return
	case "list_issues": if repo==nil{fail(-32602,"repository not found");return};items:=[]any{};state:=str(args["state"]);for _,issue:=range repo.Issues{if state==""||state=="all"||str(issue["state"])==state{items=append(items,s.issue(repo,issue))}};reply(textResult(items));return
	case "create_pull_request": if repo==nil{fail(-32602,"repository not found");return};head,base:=str(args["head"]),str(args["base"]);hsha,bsha:=repo.Branches[head],repo.Branches[base];files:=[]any{};for _,item:=range pullFiles(repo,hsha,bsha){files=append(files,item)};p:=map[string]any{"id":repo.NextID,"number":repo.NextPull,"title":args["title"],"body":fallback(args["body"],""),"state":"open","merged":false,"head":map[string]any{"ref":head,"sha":hsha},"base":map[string]any{"ref":base,"sha":bsha},"files":files};repo.NextID++;repo.NextPull++;repo.Pulls=append(repo.Pulls,p);reply(textResult(pullResponse(repo,p)));return
	case "merge_pull_request": if repo==nil{fail(-32602,"repository not found");return};number:=intArg(args["pull_number"],-1);var p map[string]any;for _,item:=range repo.Pulls{if intArg(item["number"],-2)==number{p=item;break}};if p==nil{fail(-32602,"pull request not found");return};sha:=restCommit(s,repo,fmt.Sprintf("Merge PR %d",number),"main");p["merged"]=true;p["state"]="closed";p["merge_commit_sha"]=sha;reply(textResult(map[string]any{"number":number,"merged":true,"sha":sha}));return
	case "search_repositories": query:=strings.ToLower(str(args["query"]));items:=[]any{};for _,r:=range s.repos{if strings.Contains(strings.ToLower(r.Name+" "+r.Description+" "+r.Owner),query){items=append(items,repoJSON(r))}};sort.Slice(items,func(i,j int)bool{return str(items[i].(map[string]any)["full_name"])<str(items[j].(map[string]any)["full_name"])});reply(textResult(items));return
	default: fail(-32602,"Unknown tool or missing arguments");return
	}
}
