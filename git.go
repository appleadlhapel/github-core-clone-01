package main

// Git smart HTTP and pack support. Objects are stored on Repo itself so the Git
// routes, REST routes and the existing owner-learning path share one repository.
import (
	"bytes"
	"compress/flate"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

type GitObject struct {
	Type string
	Data []byte
}

func adler32sum(b []byte) uint32 {
	a, c := uint32(1), uint32(0)
	for _, x := range b {
		a = (a + uint32(x)) % 65521
		c = (c + a) % 65521
	}
	return c<<16 | a
}

func gitOID(t string, data []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "%s %d\x00", t, len(data))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}
func pkt(b []byte) []byte {
	out := make([]byte, 4+len(b))
	copy(out, fmt.Sprintf("%04x", len(out)))
	copy(out[4:], b)
	return out
}
func pktText(s string) []byte               { return pkt([]byte(s)) }
func flushPkt() []byte                      { return []byte("0000") }
func appendPkt(dst []byte, s string) []byte { return append(dst, pktText(s)...) }
func decodePackets(b []byte) ([][]byte, int) {
	var out [][]byte
	i := 0
	for i+4 <= len(b) {
		n, e := strconv.ParseInt(string(b[i:i+4]), 16, 32)
		if e != nil {
			return out, i
		}
		if n == 0 {
			i += 4
			break
		}
		if n == 1 || n == 2 {
			i += 4
			continue
		}
		if n < 4 || i+int(n) > len(b) {
			return out, i
		}
		out = append(out, b[i+4:i+int(n)])
		i += int(n)
	}
	return out, i
}

func packetHasPrefix(packets [][]byte, prefix string) bool {
	for _, p := range packets {
		if strings.HasPrefix(string(p), prefix) {
			return true
		}
	}
	return false
}

func packetsContain(packets [][]byte, value string) bool {
	for _, p := range packets {
		if strings.TrimSuffix(string(p), "\n") == value {
			return true
		}
	}
	return false
}

func buildPack(objs []GitObject) []byte {
	b := make([]byte, 12)
	copy(b, "PACK")
	binary.BigEndian.PutUint32(b[4:], 2)
	binary.BigEndian.PutUint32(b[8:], uint32(len(objs)))
	for _, o := range objs {
		typ := map[string]byte{"commit": 1, "tree": 2, "blob": 3, "tag": 4}[o.Type]
		n := len(o.Data)
		first := byte(typ<<4) | byte(n&15)
		n >>= 4
		if n > 0 {
			first |= 128
		}
		b = append(b, first)
		for n > 0 {
			c := byte(n & 127)
			n >>= 7
			if n > 0 {
				c |= 128
			}
			b = append(b, c)
		}
		var z bytes.Buffer
		z.Write([]byte{0x78, 0x9c})
		zw, _ := flate.NewWriter(&z, flate.DefaultCompression)
		_, _ = zw.Write(o.Data)
		_ = zw.Close()
		var ad [4]byte
		binary.BigEndian.PutUint32(ad[:], adler32sum(o.Data))
		z.Write(ad[:])
		b = append(b, z.Bytes()...)
	}
	h := sha1.Sum(b)
	return append(b, h[:]...)
}

// oneByteReader prevents zlib from buffering bytes belonging to the next pack entry.
type oneByteReader struct {
	b []byte
	i int
}

func (r *oneByteReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	p[0] = r.b[r.i]
	r.i++
	return 1, nil
}

type packedRaw struct {
	typ     byte
	data    []byte
	off     int
	baseOff int
	baseOID string
}

func parsePack(b []byte) ([]GitObject, error) {
	if len(b) < 32 || string(b[:4]) != "PACK" {
		return nil, fmt.Errorf("bad pack checksum/header")
	}
	sum := sha1.Sum(b[:len(b)-20])
	if !bytes.Equal(sum[:], b[len(b)-20:]) {
		return nil, fmt.Errorf("bad pack checksum/header")
	}
	count := binary.BigEndian.Uint32(b[8:12])
	off := 12
	raw := make([]packedRaw, 0, count)
	for i := uint32(0); i < count; i++ {
		start := off
		if off >= len(b)-20 {
			return nil, fmt.Errorf("truncated pack")
		}
		c := b[off]
		off++
		typ := (c >> 4) & 7
		size := uint64(c & 15)
		shift := uint(4)
		for c&128 != 0 {
			if off >= len(b)-20 {
				return nil, fmt.Errorf("truncated object header")
			}
			c = b[off]
			off++
			size += uint64(c&127) << shift
			shift += 7
		}
		r := packedRaw{typ: typ, off: start}
		if typ == 6 {
			if off >= len(b)-20 {
				return nil, fmt.Errorf("truncated ofs delta")
			}
			c = b[off]
			off++
			n := int(c & 127)
			for c&128 != 0 {
				if off >= len(b)-20 {
					return nil, fmt.Errorf("truncated ofs delta")
				}
				c = b[off]
				off++
				n = ((n + 1) << 7) | int(c&127)
			}
			r.baseOff = off - n
		}
		if typ == 7 {
			if off+20 > len(b)-20 {
				return nil, fmt.Errorf("truncated ref delta")
			}
			r.baseOID = hex.EncodeToString(b[off : off+20])
			off += 20
		}
		zr := &oneByteReader{b: b[off : len(b)-20]}
		if zr.i+2 > len(zr.b) {
			return nil, fmt.Errorf("truncated zlib stream")
		}
		zr.i = 2
		z := flate.NewReader(zr)
		data, e := io.ReadAll(z)
		ce := z.Close()
		if e != nil {
			return nil, e
		}
		if ce != nil {
			return nil, ce
		}
		if zr.i+4 > len(zr.b) || binary.BigEndian.Uint32(zr.b[zr.i:zr.i+4]) != adler32sum(data) {
			return nil, fmt.Errorf("bad zlib checksum")
		}
		zr.i += 4
		off += zr.i
		if typ < 6 && uint64(len(data)) != size {
			return nil, fmt.Errorf("bad object size")
		}
		r.data = data
		raw = append(raw, r)
	}
	byOff := map[int]GitObject{}
	byID := map[string]GitObject{}
	state := map[int]bool{}
	var resolve func(packedRaw) (GitObject, error)
	resolve = func(r packedRaw) (GitObject, error) {
		if o, ok := byOff[r.off]; ok {
			return o, nil
		}
		if state[r.off] {
			return GitObject{}, fmt.Errorf("delta cycle")
		}
		state[r.off] = true
		var o GitObject
		if r.typ >= 1 && r.typ <= 4 {
			o = GitObject{[]string{"", "commit", "tree", "blob", "tag"}[r.typ], r.data}
		} else {
			var base GitObject
			var ok bool
			if r.typ == 6 {
				for _, x := range raw {
					if x.off == r.baseOff {
						var e error
						base, e = resolve(x)
						if e != nil {
							return o, e
						}
						ok = true
						break
					}
				}
			} else {
				base, ok = byID[r.baseOID]
			}
			if !ok {
				return o, fmt.Errorf("delta base missing")
			}
			d, e := applyGitDelta(base.Data, r.data)
			if e != nil {
				return o, e
			}
			o = GitObject{base.Type, d}
		}
		delete(state, r.off)
		byOff[r.off] = o
		byID[gitOID(o.Type, o.Data)] = o
		return o, nil
	}
	out := make([]GitObject, 0, len(raw))
	for _, r := range raw {
		o, e := resolve(r)
		if e != nil {
			return nil, e
		}
		out = append(out, o)
	}
	return out, nil
}
func readVarint(b []byte, i *int) (int, error) {
	v, shift := 0, uint(0)
	for {
		if *i >= len(b) || shift > 56 {
			return 0, fmt.Errorf("bad delta varint")
		}
		c := b[*i]
		*i++
		v |= int(c&127) << shift
		shift += 7
		if c&128 == 0 {
			return v, nil
		}
	}
}
func applyGitDelta(base, d []byte) ([]byte, error) {
	i := 0
	bs, e := readVarint(d, &i)
	if e != nil || bs != len(base) {
		return nil, fmt.Errorf("bad delta base size")
	}
	sz, e := readVarint(d, &i)
	if e != nil {
		return nil, e
	}
	out := make([]byte, 0, sz)
	for i < len(d) {
		op := d[i]
		i++
		if op&128 != 0 {
			at, n := 0, 0
			for j := 0; j < 4; j++ {
				if op&(1<<j) != 0 {
					if i >= len(d) {
						return nil, fmt.Errorf("bad delta copy")
					}
					at |= int(d[i]) << (8 * j)
					i++
				}
			}
			for j := 0; j < 3; j++ {
				if op&(1<<(j+4)) != 0 {
					if i >= len(d) {
						return nil, fmt.Errorf("bad delta copy")
					}
					n |= int(d[i]) << (8 * j)
					i++
				}
			}
			if n == 0 {
				n = 65536
			}
			if at+n > len(base) {
				return nil, fmt.Errorf("delta copy out of range")
			}
			out = append(out, base[at:at+n]...)
		} else if op != 0 {
			if i+int(op) > len(d) {
				return nil, fmt.Errorf("bad delta insert")
			}
			out = append(out, d[i:i+int(op)]...)
			i += int(op)
		} else {
			return nil, fmt.Errorf("invalid delta")
		}
	}
	if len(out) != sz {
		return nil, fmt.Errorf("delta size")
	}
	return out, nil
}

func (s *Store) gitRefs(r *Repo) []struct{ name, oid string } {
	var a []struct{ name, oid string }
	for b, id := range r.Branches {
		if id != "" {
			a = append(a, struct{ name, oid string }{"refs/heads/" + b, id})
		}
	}
	for t, id := range r.Tags {
		a = append(a, struct{ name, oid string }{"refs/tags/" + t, id})
	}
	sort.Slice(a, func(i, j int) bool { return a[i].name < a[j].name })
	return a
}
func peelGit(r *Repo, id string) string {
	o, ok := r.Objects[id]
	if !ok || o.Type != "tag" {
		return ""
	}
	for _, l := range strings.Split(string(o.Data), "\n") {
		if strings.HasPrefix(l, "object ") {
			return strings.TrimPrefix(l, "object ")
		}
	}
	return ""
}
func (s *Store) changed(r *Repo) { clear(r.GitCache); clear(r.IssueCache) }
func reachableOrder(r *Repo, roots []string, depth int) []string {
	out := map[string]bool{}
	order := []string{}
	var visit func(string, int)
	visit = func(id string, d int) {
		if out[id] {
			return
		}
		o, ok := r.Objects[id]
		if !ok {
			return
		}
		out[id] = true
		order = append(order, id)
		switch o.Type {
		case "commit":
			tree := ""
			parents := []string{}
			for _, l := range strings.Split(string(o.Data), "\n") {
				if strings.HasPrefix(l, "tree ") {
					tree = l[5:]
				} else if strings.HasPrefix(l, "parent ") {
					parents = append(parents, l[7:])
				}
			}
			visit(tree, d)
			if depth == 0 || d+1 < depth {
				for _, p := range parents {
					visit(p, d+1)
				}
			}
		case "tag":
			visit(peelGit(r, id), d)
		case "tree":
			for at := 0; at < len(o.Data); {
				sp := bytes.IndexByte(o.Data[at:], ' ')
				if sp < 0 {
					break
				}
				sp += at
				nul := bytes.IndexByte(o.Data[sp+1:], 0)
				if nul < 0 || sp+1+nul+21 > len(o.Data) {
					break
				}
				at = sp + 1 + nul + 21
				visit(hex.EncodeToString(o.Data[at-20:at]), d)
			}
		}
	}
	for _, id := range roots {
		visit(id, 0)
	}
	return order
}
func reachable(r *Repo, roots []string, depth int) map[string]bool {
	out := map[string]bool{}
	for _, id := range reachableOrder(r, roots, depth) {
		out[id] = true
	}
	return out
}

func (s *Store) gitHandler(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || !strings.HasSuffix(parts[1], ".git") {
		s.fail(w, 404, "Not Found")
		return
	}
	owner := parts[0]
	name := strings.TrimSuffix(parts[1], ".git")
	ep := strings.Join(parts[2:], "/")
	if s.owner != "" && owner != s.owner {
		s.fail(w, 404, "Not Found")
		return
	}
	repo := s.repo(owner, name)
	if repo == nil {
		s.fail(w, 404, "Not Found")
		return
	}
	if ep == "info/refs" && r.Method == "GET" {
		s.gitAdvertise(w, r, repo)
		return
	}
	if ep == "git-upload-pack" && r.Method == "POST" {
		s.gitUpload(w, r, repo)
		return
	}
	if ep == "git-receive-pack" && r.Method == "POST" {
		s.gitReceive(w, r, repo)
		return
	}
	s.fail(w, 404, "Not Found")
}
func (s *Store) gitAdvertise(w http.ResponseWriter, r *http.Request, repo *Repo) {
	service := r.URL.Query().Get("service")
	if service != "git-receive-pack" {
		service = "git-upload-pack"
	}
	b := appendPkt(nil, "# service="+service+"\n")
	b = append(b, flushPkt()...)
	v2 := service == "git-upload-pack" && strings.Contains(r.Header.Get("Git-Protocol"), "version=2")
	if v2 {
		for _, line := range []string{"version 2\n", "agent=forge/1\n", "ls-refs\n", "fetch=shallow wait-for-done filter\n", "server-option\n", "object-format=sha1\n"} {
			b = appendPkt(b, line)
		}
		b = append(b, flushPkt()...)
	} else {
		caps := "side-band-64k ofs-delta agent=forge/1"
		if service == "git-upload-pack" {
			caps += " shallow"
		}
		if service == "git-receive-pack" {
			caps = "report-status delete-refs ofs-delta side-band-64k quiet atomic agent=forge/1"
		}
		refs := s.gitRefs(repo)
		main := repo.Branches["main"]
		if main == "" && len(refs) > 0 {
			main = refs[0].oid
		}
		if len(refs) > 0 {
			b = append(b, pktText(main+" HEAD\x00"+caps+"\n")...)
			for _, x := range refs {
				b = append(b, pktText(x.oid+" "+x.name+"\n")...)
				if strings.HasPrefix(x.name, "refs/tags/") {
					if peeled := peelGit(repo, x.oid); peeled != "" {
						b = append(b, pktText(peeled+" "+x.name+"^{}\n")...)
					}
				}
			}
		} else {
			b = append(b, pktText(strings.Repeat("0", 40)+" capabilities^{}\x00"+caps+"\n")...)
		}
		b = append(b, flushPkt()...)
	}
	w.Header().Set("content-type", "application/x-"+service+"-advertisement")
	w.WriteHeader(200)
	_, _ = w.Write(b)
}

func (s *Store) gitUpload(w http.ResponseWriter, r *http.Request, repo *Repo) {
	body, _ := io.ReadAll(r.Body)
	packets, _ := decodePackets(body)
	v2 := strings.Contains(r.Header.Get("Git-Protocol"), "version=2")
	out := []byte{}
	if v2 && packetHasPrefix(packets, "command=ls-refs") {
		prefixes := []string{}
		for _, p := range packets {
			line := strings.TrimSuffix(string(p), "\n")
			if strings.HasPrefix(line, "ref-prefix ") {
				prefixes = append(prefixes, line[11:])
			}
		}
		for _, x := range s.gitRefs(repo) {
			keep := len(prefixes) == 0
			for _, p := range prefixes {
				if strings.HasPrefix(x.name, p) {
					keep = true
				}
			}
			if keep {
				line := x.oid + " " + x.name
				if p := peelGit(repo, x.oid); p != "" && packetsContain(packets, "peel") {
					line += " peeled:" + p
				}
				out = append(out, pktText(line+"\n")...)
			}
		}
		headRequested := len(prefixes) == 0
		for _, p := range prefixes {
			if strings.HasPrefix("HEAD", p) {
				headRequested = true
			}
		}
		if headRequested && repo.Branches["main"] != "" {
			out = append(out, pktText(repo.Branches["main"]+" HEAD symref-target:refs/heads/main\n")...)
		}
		out = append(out, flushPkt()...)
	} else {
		wants, haves, shallow, depth := []string{}, []string{}, []string{}, 0
		txt := string(body)
		for _, p := range packets {
			line := strings.TrimSuffix(string(p), "\n")
			f := strings.Fields(line)
			if len(f) > 1 {
				switch f[0] {
				case "want":
					wants = append(wants, f[1])
				case "have":
					haves = append(haves, f[1])
				case "shallow":
					shallow = append(shallow, f[1])
				case "deepen":
					depth, _ = strconv.Atoi(f[1])
				}
			}
		}
		for _, line := range strings.Split(txt, "\n") {
			f := strings.Fields(line)
			if len(f) > 1 {
				switch f[0] {
				case "want":
					wants = append(wants, f[1])
				case "have":
					haves = append(haves, f[1])
				case "shallow":
					shallow = append(shallow, f[1])
				case "deepen":
					depth, _ = strconv.Atoi(f[1])
				}
			}
		}
		wants = unique(wants)
		haves = unique(haves)
		ordered := reachableOrder(repo, wants, depth)
		sel := map[string]bool{}
		for _, id := range ordered {
			sel[id] = true
		}
		if depth == 0 && len(haves) > 0 {
			held := reachable(repo, haves, 0)
			for id := range held {
				delete(sel, id)
			}
		}
		list := make([]string, 0, len(sel))
		for _, id := range ordered {
			if sel[id] {
				list = append(list, id)
			}
		}
		objs := make([]GitObject, 0, len(list))
		for _, id := range list {
			objs = append(objs, repo.Objects[id])
		}
		cacheKey := ""
		if depth == 1 && len(wants) == 1 && len(haves) == 0 {
			cacheKey = "clone:" + wants[0] + ":1"
		}
		pack, ok := repo.GitCache[cacheKey]
		if !ok || cacheKey == "" {
			pack = buildPack(objs)
			if cacheKey != "" {
				repo.GitCache[cacheKey] = pack
			}
		}
		shallowLines := []string{}
		if depth > 0 && len(wants) > 0 {
			chain := []string{}
			id := wants[0]
			for i := 0; i < depth && id != ""; i++ {
				chain = append(chain, id)
				o := repo.Objects[id]
				parent := ""
				for _, l := range strings.Split(string(o.Data), "\n") {
					if strings.HasPrefix(l, "parent ") {
						parent = l[7:]
						break
					}
				}
				id = parent
			}
			if len(chain) >= depth {
				shallowLines = append(shallowLines, "shallow "+chain[len(chain)-1]+"\n")
			}
			for _, old := range shallow {
				for _, x := range chain {
					if old == x && old != chain[0] {
						shallowLines = append(shallowLines, "unshallow "+old+"\n")
					}
				}
			}
		}
		if v2 {
			out = append(out, pktText("acknowledgments\n")...)
			out = append(out, pktText("NAK\n")...)
			out = append(out, flushPkt()...)
		} else {
			for _, line := range shallowLines {
				out = append(out, pktText(line)...)
			}
			out = append(out, pktText("NAK\n")...)
		}
		if v2 && len(shallowLines) > 0 {
			out = append(out, pktText("shallow-info\n")...)
			for _, line := range shallowLines {
				out = append(out, pktText(line)...)
			}
			out = append(out, flushPkt()...)
		}
		if v2 {
			out = append(out, pktText("packfile\n")...)
		}
		for i := 0; i < len(pack); i += 64000 {
			end := i + 64000
			if end > len(pack) {
				end = len(pack)
			}
			chunk := append([]byte{1}, pack[i:end]...)
			out = append(out, pkt(chunk)...)
		}
		if v2 {
			out = append(out, flushPkt()...)
		}
	}
	w.Header().Set("content-type", "application/x-git-upload-pack-result")
	w.WriteHeader(200)
	_, _ = w.Write(out)
}
func unique(a []string) []string {
	m := map[string]bool{}
	out := a[:0]
	for _, x := range a {
		if !m[x] {
			m[x] = true
			out = append(out, x)
		}
	}
	return out
}

func (s *Store) gitReceive(w http.ResponseWriter, r *http.Request, repo *Repo) {
	body, _ := io.ReadAll(r.Body)
	_, end := decodePackets(body)
	commands, _ := decodePackets(body)
	type cmd struct{ old, next, ref string }
	cs := []cmd{}
	for i, p := range commands {
		if len(p) == 0 {
			continue
		}
		line := strings.SplitN(string(p), "\x00", 2)[0]
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		c := cmd{f[0], f[1], f[2]}
		cs = append(cs, c)
		_ = i
	}
	unpack := "ok"
	if end < len(body) {
		objs, e := parsePack(body[end:])
		if e != nil {
			unpack = "error invalid pack"
		} else {
			for _, o := range objs {
				repo.Objects[gitOID(o.Type, o.Data)] = o
			}
		}
	} else if len(cs) > 0 {
		for _, c := range cs {
			if c.next != strings.Repeat("0", 40) {
				unpack = "error invalid pack"
			}
		}
	}
	lines := []string{"unpack " + unpack + "\n"}
	zero := strings.Repeat("0", 40)
	for _, c := range cs {
		refs := repo.Branches
		name := strings.TrimPrefix(strings.TrimPrefix(c.ref, "refs/heads/"), "refs/tags/")
		tag := strings.HasPrefix(c.ref, "refs/tags/")
		if tag {
			refs = repo.Tags
		}
		old := refs[name]
		if old == "" {
			old = zero
		}
		status := "ok " + c.ref
		if unpack != "ok" {
			status = "ng " + c.ref + " unpack failed"
		} else if old != c.old {
			status = "ng " + c.ref + " stale info"
		} else if c.next != zero && old != zero && !isAncestor(repo, c.next, old) {
			status = "ng " + c.ref + " non-fast-forward"
		} else if c.next == zero {
			delete(refs, name)
		} else {
			refs[name] = c.next
			if !tag {
				gitReceiveCommitFiles(repo, c.next)
			}
		}
		lines = append(lines, status+"\n")
	}
	s.changed(repo)
	inner := []byte{}
	for _, line := range lines {
		inner = append(inner, pktText(line)...)
	}
	inner = append(inner, flushPkt()...)
	report := []byte{}
	for len(inner) > 0 {
		n := len(inner)
		if n > 64000 {
			n = 64000
		}
		report = append(report, pkt(append([]byte{1}, inner[:n]...))...)
		inner = inner[n:]
	}
	report = append(report, flushPkt()...)
	w.Header().Set("content-type", "application/x-git-receive-pack-result")
	w.WriteHeader(200)
	_, _ = w.Write(report)
}

// gitReceiveCommitFiles mirrors the receive-pack state update used by the
// REST commits and contents endpoints: record the pushed commit and materialize
// the files reachable from its root tree.
func gitReceiveCommitFiles(repo *Repo, tip string) {
	commit, ok := repo.Objects[tip]
	if !ok || commit.Type != "commit" {
		return
	}
	text := string(commit.Data)
	tree := ""
	message := ""
	parents := []any{}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "tree ") {
			tree = line[5:]
		} else if strings.HasPrefix(line, "parent ") {
			parents = append(parents, map[string]any{"sha": line[7:]})
		}
	}
	if separator := strings.Index(text, "\n\n"); separator >= 0 {
		message = strings.TrimSpace(text[separator+2:])
	}
	files := gitTreeFiles(repo, tree)
	repo.Files = files
	if !containsCommit(repo.Commits, tip) {
		repo.Commits = append([]map[string]any{{
			"sha": tip,
			"commit": map[string]any{
				"message":   message,
				"author":    map[string]any{"name": "User", "email": "user@example.com"},
				"committer": map[string]any{"name": "User", "email": "user@example.com"},
			},
			"parents":  parents,
			"html_url": fmt.Sprintf("http://forge/%s/%s/commit/%s", repo.Owner, repo.Name, tip),
		}}, repo.Commits...)
	}
}

func gitTreeFiles(repo *Repo, tree string) map[string]string {
	files := map[string]string{}
	var walk func(string, string)
	walk = func(id, prefix string) {
		o, exists := repo.Objects[id]
		if !exists || o.Type != "tree" {
			return
		}
		for at := 0; at < len(o.Data); {
			sp := bytes.IndexByte(o.Data[at:], ' ')
			if sp < 0 {
				break
			}
			sp += at
			nul := bytes.IndexByte(o.Data[sp+1:], 0)
			if nul < 0 {
				break
			}
			nul += sp + 1
			if nul+21 > len(o.Data) {
				break
			}
			mode := string(o.Data[at:sp])
			name := string(o.Data[sp+1 : nul])
			oid := hex.EncodeToString(o.Data[nul+1 : nul+21])
			at = nul + 21
			if mode == "40000" {
				walk(oid, prefix+name+"/")
			} else if blob, exists := repo.Objects[oid]; exists && blob.Type == "blob" {
				files[prefix+name] = string(blob.Data)
			}
		}
	}
	walk(tree, "")
	return files
}

// pullFiles computes a sorted PR file list from the head and base commit trees.
// Repositories populated through REST contents may not have Git objects, so
// absent head commits fall back to the materialized repository file map.
func pullFiles(repo *Repo, headSha, baseSha string) []any {
	head, ok := repo.Objects[headSha]
	if !ok || head.Type != "commit" {
		return pullFileEntries(repo.Files)
	}
	treeID := commitTreeID(head)
	tree, ok := repo.Objects[treeID]
	if !ok || tree.Type != "tree" {
		return pullFileEntries(repo.Files)
	}
	headFiles := gitTreeFiles(repo, treeID)
	baseFiles := map[string]string{}
	if base, exists := repo.Objects[baseSha]; exists && base.Type == "commit" {
		baseTreeID := commitTreeID(base)
		if baseTree, exists := repo.Objects[baseTreeID]; exists && baseTree.Type == "tree" {
			baseFiles = gitTreeFiles(repo, baseTreeID)
		}
	}
	changed := map[string]string{}
	for path, content := range headFiles {
		if baseContent, exists := baseFiles[path]; !exists || baseContent != content {
			changed[path] = content
		}
	}
	return pullFileEntries(changed)
}

func commitTreeID(commit GitObject) string {
	for _, line := range strings.Split(string(commit.Data), "\n") {
		if strings.HasPrefix(line, "tree ") {
			return line[5:]
		}
	}
	return ""
}

func pullFileEntries(files map[string]string) []any {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	entries := make([]any, 0, len(paths))
	for _, path := range paths {
		entries = append(entries, map[string]any{"filename": path, "status": "modified", "additions": 1, "deletions": 0, "changes": 1, "blob_url": "", "raw_url": "", "contents_url": ""})
	}
	return entries
}

func containsCommit(commits []map[string]any, sha string) bool {
	for _, commit := range commits {
		if commit["sha"] == sha {
			return true
		}
	}
	return false
}

func isAncestor(r *Repo, tip, ancestor string) bool {
	seen := map[string]bool{}
	q := []string{tip}
	for len(q) > 0 {
		id := q[0]
		q = q[1:]
		if id == ancestor {
			return true
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		o := r.Objects[id]
		if o.Type == "commit" {
			for _, l := range strings.Split(string(o.Data), "\n") {
				if strings.HasPrefix(l, "parent ") {
					q = append(q, l[7:])
				}
			}
		}
	}
	return false
}
