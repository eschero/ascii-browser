#!/usr/bin/env python3
# Renders every URL with --once in parallel and records what went wrong.
import subprocess, sys, os, re, json, time, concurrent.futures as cf
S=os.path.dirname(os.path.abspath(__file__))
BIN=sys.argv[1]; OUT=sys.argv[2]; MODE=sys.argv[3] if len(sys.argv)>3 else "auto"
EXTRA=sys.argv[4:]
os.makedirs(OUT, exist_ok=True)
urls=[u.strip() for u in open(os.path.join(S,'urls.txt')) if u.strip()]
COOKIE=re.compile(r'cookie|consent|consenso|accetta|accept all|rifiuta|reject all|privacy settings|gdpr|we use cookies|utilizziamo', re.I)
def run(i,u):
    dump=os.path.join(OUT,f'{i:03d}.txt')
    t=time.time()
    try:
        p=subprocess.run([BIN,'--once','--size','200x49','--profile','','--renderer',MODE,'--timing','--timeout','25s','--dump',dump,*EXTRA,u],
                         capture_output=True,text=True,timeout=90)
        rc,err=p.returncode,p.stderr
    except subprocess.TimeoutExpired:
        rc,err=-9,'HARD TIMEOUT'
    dt=time.time()-t
    txt=open(dump,errors='replace').read() if os.path.exists(dump) else ''
    lines=[l for l in txt.split('\n') if l]
    widths=sorted(set(len(l) for l in lines))
    letters=sum(c.isalpha() for c in txt)
    nonascii=sum(1 for c in txt if ord(c)>126 or (ord(c)<32 and c!='\n'))
    rows_text=sum(1 for l in lines if re.search(r'[A-Za-z]{3}',l))
    cookie=len(COOKIE.findall(txt))
    blank=sum(1 for l in lines if not l.strip())
    m=re.search(r'total ([0-9.]+m?s)',err); tot=m.group(1) if m else ''
    docm=re.search(r'dom quiet: \w+ after \d+ ms, (\d+) characters',err); doc=int(docm.group(1)) if docm else -1
    panic='panic' in err or 'goroutine ' in err
    return dict(i=i,url=u,rc=rc,sec=round(dt,1),total=tot,rows=len(lines),widths=widths,letters=letters,
                nonascii=nonascii,rows_text=rows_text,cookie=cookie,blank=blank,doc=doc,panic=panic,
                err=' | '.join(l for l in err.split('\n') if l and not l.startswith(('chrome start','grid ','dom quiet','wake:','ui detail')))[:300])
res=[]
with cf.ThreadPoolExecutor(int(os.environ.get('J','8'))) as ex:
    for r in ex.map(lambda a: run(*a), enumerate(urls)):
        res.append(r)
        flag=[]
        if r['rc']!=0: flag.append(f"rc={r['rc']}")
        if r['panic']: flag.append('PANIC')
        if r['rows']!=49 or r['widths']!=[200]: flag.append(f"shape={r['rows']}x{r['widths']}")
        if r['nonascii']: flag.append(f"nonascii={r['nonascii']}")
        if r['letters']<150: flag.append(f"letters={r['letters']}")
        if r['cookie']>=2: flag.append(f"cookie={r['cookie']}")
        print(f"{r['i']:03d} {r['sec']:5.1f}s L={r['letters']:5d} doc={r['doc']:6d} {' '.join(flag):40s} {r['url']}",flush=True)
json.dump(res,open(os.path.join(OUT,'results.json'),'w'),indent=1)
