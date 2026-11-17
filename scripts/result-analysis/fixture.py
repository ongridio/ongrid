"""Deterministic 25,000-row MCP fixture and independent SQLite answer oracle."""
import csv
import io
import json
import sqlite3
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

N = 25000
BASE = 9007199254740993

def row(i):
    return dict(id=BASE+i, name=f'payments-{i:05d}' if i % 13 == 0 or i == N-1 else f'service-{i:05d}',
                status='failed' if i % 17 == 0 or i == N-1 else 'paused' if i % 29 == 0 else 'success',
                region=None if i % 11 == 0 else ['east','west','north','south'][i % 4],
                team=f'team-{i % 7}', cost_cents=(i*137)%100000+25,
                duration_ms=99999 if i >= N-3 else (i*97)%50000,
                owner=None if i % 41 == 0 else '' if i % 43 == 0 else f'owner-{i % 97}',
                external_key=f'ext-{i % 24950}', note='模拟数据，包含逗号, 引号"和换行\n仅测试')

CURRENT = [row(i) for i in range(N)]
PREVIOUS = [dict(row(i), status='success' if i % 31 == 0 else row(i)['status']) for i in range(-50,N-50)]
stream = io.StringIO(newline='')
writer = csv.DictWriter(stream, fieldnames=list(CURRENT[0]))
writer.writeheader(); writer.writerows(CURRENT)
RESULTS = {
    'current_projects': {'content':[{'type':'text','text':'Synthetic current snapshot; 25000 projects in structuredContent.projects. Monetary amounts are integer cents; IDs must retain all digits.'}], 'structuredContent':{'synthetic':True,'projects':CURRENT}},
    'legacy_projects': {'content':[{'type':'text','text':json.dumps({'synthetic':True,'projects':CURRENT},ensure_ascii=False,separators=(',',':'))}]},
    'previous_projects': {'content':[{'type':'text','text':'Synthetic previous snapshot; 25000 projects in structuredContent.projects.'}], 'structuredContent':{'synthetic':True,'projects':PREVIOUS}},
    'projects_csv': {'content':[{'type':'text','text':stream.getvalue()}]},
}
DESCRIPTIONS = {
    'current_projects':'Read-only synthetic current snapshot, 25000 projects. Fields: id, name, status, region, team, cost_cents, duration_ms, owner, external_key, note.',
    'legacy_projects':'Read-only synthetic current snapshot in legacy JSON text format, 25000 projects with the same fields as current_projects.',
    'previous_projects':'Read-only synthetic previous snapshot, 25000 projects. Compare by exact integer id; same fields as current_projects.',
    'projects_csv':'Read-only synthetic current snapshot as CSV with 25000 data records and a header. Embedded newlines and quoted commas occur in note. All IDs and costs are exact integers.',
}

def oracle():
    db=sqlite3.connect(':memory:'); db.row_factory=sqlite3.Row
    for name,rows in [('current',CURRENT),('previous',PREVIOUS)]:
        db.execute(f'CREATE TABLE {name} (id INTEGER, name TEXT, status TEXT, region TEXT, team TEXT, cost_cents INTEGER, duration_ms INTEGER, owner TEXT, external_key TEXT, note TEXT)')
        db.executemany(f'INSERT INTO {name} VALUES ({",".join("?" for _ in rows[0])})',[tuple(r.values()) for r in rows])
    def query(sql): return [dict(r) for r in db.execute(sql)]
    return {
        'filter': {'total':N,'matches':query("SELECT id,name,cost_cents FROM current WHERE name LIKE '%payments%' AND status='failed' AND cost_cents>=80000 ORDER BY id")},
        'groups': {'total':N,'groups':query("SELECT COALESCE(region,'unknown') region,COUNT(*) count,SUM(cost_cents) cost_cents FROM current GROUP BY region ORDER BY region" )},
        'top': {'total':N,'top':query('SELECT id,duration_ms FROM current ORDER BY duration_ms DESC,id ASC LIMIT 5')},
        'quality': {'total':N,'missing_owner':query("SELECT COUNT(*) n FROM current WHERE owner IS NULL OR owner=''")[0]['n'],'duplicate_keys':query('SELECT COUNT(*) n FROM (SELECT external_key FROM current GROUP BY external_key HAVING COUNT(*)>1)')[0]['n'],'duplicate_rows':query('SELECT SUM(n) n FROM (SELECT COUNT(*) n FROM current GROUP BY external_key HAVING COUNT(*)>1)')[0]['n'],'critical':query("SELECT COUNT(*) n FROM current WHERE status='critical'")[0]['n']},
        'csv': {'total':N,'cost_cents':query('SELECT SUM(cost_cents) n FROM current')[0]['n'],'min_id':BASE,'max_id':BASE+N-1,'failed':query("SELECT COUNT(*) n FROM current WHERE status='failed'")[0]['n']},
        'compare': {'current_total':N,'previous_total':N,'added':query('SELECT COUNT(*) n FROM current c LEFT JOIN previous p USING(id) WHERE p.id IS NULL')[0]['n'],'removed':query('SELECT COUNT(*) n FROM previous p LEFT JOIN current c USING(id) WHERE c.id IS NULL')[0]['n'],'status_changed':query('SELECT COUNT(*) n FROM current c JOIN previous p USING(id) WHERE c.status!=p.status')[0]['n'],'new_failures':query("SELECT COUNT(*) n FROM current c JOIN previous p USING(id) WHERE c.status='failed' AND p.status!='failed'")[0]['n']},
    }

class Handler(BaseHTTPRequestHandler):
    def reply(self,status,value):
        body=json.dumps(value,ensure_ascii=False,separators=(',',':')).encode()
        self.send_response(status); self.send_header('Content-Type','application/json'); self.send_header('Content-Length',str(len(body))); self.end_headers(); self.wfile.write(body)
    def do_POST(self):
        length=int(self.headers.get('Content-Length','0'))
        if self.path!='/mcp' or not 0<length<=16384: self.reply(400,{'error':'invalid request'}); return
        try: request=json.loads(self.rfile.read(length))
        except ValueError: self.reply(400,{'error':'invalid JSON'}); return
        method,rid=request.get('method'),request.get('id')
        if rid is None: self.send_response(202); self.send_header('Content-Length','0'); self.end_headers(); return
        if method=='initialize': result={'protocolVersion':'2025-03-26','capabilities':{'tools':{}},'serverInfo':{'name':'result-analysis-fixture','version':'1'}}
        elif method=='tools/list': result={'tools':[{'name':n,'description':d,'inputSchema':{'type':'object','properties':{},'additionalProperties':False},'annotations':{'readOnlyHint':True}} for n,d in DESCRIPTIONS.items()]}
        elif method=='tools/call' and request.get('params',{}).get('name') in RESULTS: result=RESULTS[request['params']['name']]
        else: self.reply(200,{'jsonrpc':'2.0','id':rid,'error':{'code':-32601,'message':'unknown method/tool'}}); return
        self.reply(200,{'jsonrpc':'2.0','id':rid,'result':result})
    def log_message(self,*_): pass

if __name__=='__main__':
    sizes={k:len(json.dumps(v,ensure_ascii=False,separators=(',',':')).encode()) for k,v in RESULTS.items()}
    assert all(1<<20 < n < 16<<20 for n in sizes.values()),sizes
    answers=oracle()
    assert len(CURRENT)==len(PREVIOUS)==N and len({r['id'] for r in CURRENT})==N
    assert answers['compare']['added']==answers['compare']['removed']==50
    assert answers['quality']['duplicate_keys']==50 and answers['quality']['duplicate_rows']==100
    assert answers['csv']['max_id']>2**53
    assert len(list(csv.DictReader(io.StringIO(stream.getvalue()))))==N
    if '--self-test' in sys.argv: print(json.dumps({'fixture_bytes':sizes,'answers':answers},ensure_ascii=False)); sys.exit(0)
    ThreadingHTTPServer(('0.0.0.0',8000),Handler).serve_forever()
