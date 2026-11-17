import { createHash } from 'node:crypto';
import { deflateSync, inflateSync } from 'node:zlib';

export type Obj = { type: 'blob'|'tree'|'commit'|'tag'; data: Buffer };
export const oid = (type: Obj['type'], data: Buffer) => createHash('sha1').update(`${type} ${data.length}\0`).update(data).digest('hex');
export const pkt = (s: string|Buffer) => { const b=Buffer.isBuffer(s)?s:Buffer.from(s); return Buffer.concat([Buffer.from((b.length+4).toString(16).padStart(4,'0')),b]); };
export function parsePack(p: Buffer): Obj[] {
 if(p.length<32||p.toString('ascii',0,4)!=='PACK'||!createHash('sha1').update(p.subarray(0,-20)).digest().equals(p.subarray(-20))) throw Error('bad pack checksum/header');
 const count=p.readUInt32BE(8); let off=12; const raw:{type:number;data:Buffer;offset:number;baseOff?:number;baseOid?:string}[]=[];
 for(let i=0;i<count;i++){const start=off;let c=p[off++],type=(c>>4)&7,size=c&15,shift=4;while(c&128){c=p[off++];size+=(c&127)*2**shift;shift+=7} let baseOff:number|undefined,baseOid:string|undefined;
  if(type===6){c=p[off++];let n=c&127;while(c&128){c=p[off++];n=((n+1)<<7)|(c&127)}baseOff=off-n}
  if(type===7){baseOid=p.subarray(off,off+20).toString('hex');off+=20}
  const inf=inflateSync(p.subarray(off),{info:true});const data=inf.buffer;off+=inf.engine.bytesWritten;if(data.length!==size&&type<6)throw Error('bad object size');raw.push({type,data,offset:start,baseOff,baseOid});
 }
 const result:Obj[]=[];const byOff=new Map<number,Obj>();const byId=new Map<string,Obj>();
 const varint=(b:Buffer,s:{n:number})=>{let v=0,sh=0,x;do{x=b[s.n++];v+=(x&127)*2**sh;sh+=7}while(x&128);return v};
 const delta=(base:Buffer,d:Buffer)=>{const s={n:0};varint(d,s);const size=varint(d,s);const out:Buffer[]=[];while(s.n<d.length){const op=d[s.n++];if(op&128){let at=0,len=0;for(let j=0;j<4;j++)if(op&(1<<j))at+=d[s.n++]<<(j*8);for(let j=0;j<3;j++)if(op&(1<<(j+4)))len+=d[s.n++]<<(j*8);if(!len)len=65536;out.push(base.subarray(at,at+len))}else if(op)out.push(d.subarray(s.n,s.n+=op));else throw Error('invalid delta')}const b=Buffer.concat(out);if(b.length!==size)throw Error('delta size');return b};
 const resolving=new Set<number>();const get=(r:typeof raw[number]):Obj=>{const known=byOff.get(r.offset);if(known)return known;if(resolving.has(r.offset))throw Error('delta cycle');resolving.add(r.offset);let o:Obj;
  if(r.type<6)o={type:(['','commit','tree','blob','tag'] as const)[r.type],data:r.data};else {const base=r.type===6?raw.find(x=>x.offset===r.baseOff):undefined;const b=base?get(base):byId.get(r.baseOid!);if(!b)throw Error('delta base missing');o={type:b.type,data:delta(b.data,r.data)}}
  resolving.delete(r.offset);byOff.set(r.offset,o);byId.set(oid(o.type,o.data),o);return o};
 for(const r of raw)result.push(get(r));return result;
}
export function buildPack(objects: Obj[]) {const a:Buffer[]=[];const h=Buffer.alloc(12);h.write('PACK');h.writeUInt32BE(2,4);h.writeUInt32BE(objects.length,8);a.push(h);for(const o of objects){let n=o.data.length,c=({commit:1,tree:2,blob:3,tag:4} as const)[o.type],first=(c<<4)|(n&15);n=Math.floor(n/16);const b=[first];while(n){b[b.length-1]|=128;b.push(n&127);n=Math.floor(n/128)}a.push(Buffer.from(b),deflateSync(o.data))}const body=Buffer.concat(a);return Buffer.concat([body,createHash('sha1').update(body).digest()])}
