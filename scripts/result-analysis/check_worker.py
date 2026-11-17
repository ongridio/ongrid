"""Exercise the actual Linux worker; command follows -- (e.g. docker exec -i ongrid /ongrid)."""
import json,subprocess,sys,time,uuid
from fixture import RESULTS,oracle

CMD=sys.argv[sys.argv.index('--')+1:]+['--internal-result-analysis']
IDS={name:str(uuid.uuid5(uuid.NAMESPACE_URL,'analysis-test:'+name)) for name in RESULTS}

def execute(inputs,expression,timeout=15):
    header={'expression':expression,'inputs':[]};bodies=[]
    for name,format,pointer,collection in inputs:
        body=json.dumps(RESULTS[name],ensure_ascii=False,separators=(',',':')).encode()
        header['inputs'].append({'result_id':IDS[name],'format':format,'pointer':pointer,'collection_pointer':collection,'bytes':len(body)})
        bodies.append(body)
    wire=json.dumps(header).encode()+b'\n'+b''.join(bodies)
    start=time.monotonic();p=subprocess.run(CMD,input=wire,capture_output=True,timeout=timeout)
    return p,time.monotonic()-start

current=[('current_projects','json','/structuredContent/projects','')]
queries={
 'filter':(current, 'reduce inputs as $r ({total:0,matches:[]}; .total+=1 | if ($r.value.name|contains("payments")) and $r.value.status=="failed" and $r.value.cost_cents>=80000 then .matches+=[$r.value|{id,name,cost_cents}] else . end) | .matches|=sort_by(.id)'),
 'groups':(current, 'reduce inputs as $r ({total:0,g:{}}; ($r.value.region//"unknown") as $k | .total+=1 | .g[$k].region=$k | .g[$k].count=(.g[$k].count//0)+1 | .g[$k].cost_cents=(.g[$k].cost_cents//0)+$r.value.cost_cents) | {total,groups:(.g|[.[]]|sort_by(.region))}'),
 'top':(current, 'reduce inputs as $r ({total:0,top:[]}; .total+=1 | .top=((.top+[$r.value|{id,duration_ms}])|sort_by([-.duration_ms,.id])|.[0:5]))'),
 'quality':([('legacy_projects','json_text','/content/0/text','/projects')], '[inputs.value|{owner,status,external_key}] as $r | ($r|group_by(.external_key)|map(select(length>1))) as $d | {total:($r|length),missing_owner:([$r[]|select(.owner==null or .owner=="")]|length),duplicate_keys:($d|length),duplicate_rows:($d|map(length)|add),critical:([$r[]|select(.status=="critical")]|length)}'),
 'csv':([('projects_csv','csv','/content/0/text','')], 'reduce inputs as $r ({total:0,cost_cents:0,min_id:null,max_id:null,failed:0}; ($r.value.id|tonumber) as $id | .total+=1 | .cost_cents+=($r.value.cost_cents|tonumber) | .min_id=([.min_id,$id]|map(select(.!=null))|min) | .max_id=([.max_id,$id]|map(select(.!=null))|max) | .failed+=(if $r.value.status=="failed" then 1 else 0 end))'),
 'compare':(current+[('previous_projects','json','/structuredContent/projects','')], '[inputs | if .source=="'+IDS['current_projects']+'" then {id:.value.id,new_status:.value.status} else {id:.value.id,old_status:.value.status} end] | group_by(.id) | map(add) as $g | {current_total:([$g[]|select(.new_status!=null)]|length),previous_total:([$g[]|select(.old_status!=null)]|length),added:([$g[]|select(.old_status==null)]|length),removed:([$g[]|select(.new_status==null)]|length),status_changed:([$g[]|select(.old_status!=null and .new_status!=null and .old_status!=.new_status)]|length),new_failures:([$g[]|select(.old_status!=null and .old_status!="failed" and .new_status=="failed")]|length)}'),
}
expected=oracle();report=[]
for name,(inputs,expr) in queries.items():
    p,elapsed=execute(inputs,expr)
    assert p.returncode==0,(name,p.stderr.decode()[:1000])
    result=json.loads(p.stdout)
    assert result['values']==[expected[name]],(name,result['values'],expected[name])
    assert all(x['complete'] and x['records_read']==25000 for x in result['coverage']),result['coverage']
    report.append({'case':name,'seconds':round(elapsed,3),'coverage':result['coverage']})
# A successful prefix query must honestly report incomplete coverage.
p,_=execute(current,'first(inputs).value.id');assert p.returncode==0,p.stderr
assert json.loads(p.stdout)['coverage'][0]=={'result_id':IDS['current_projects'],'records_read':1,'complete':False}
for expr in ['env', '$ENV']:
    p,_=execute(current,expr);assert p.returncode==0 and json.loads(p.stdout)['values']==[{}],p.stderr
for expr in ['import "/etc/passwd" as p; p','"x"*65536','[range(1000000000)]','def f:f;f']:
    p,elapsed=execute(current,expr)
    assert p.returncode!=0 and elapsed<12,(expr,p.returncode,elapsed)
    report.append({'negative':expr,'exit_code':p.returncode,'seconds':round(elapsed,3),'stderr_prefix':p.stderr.decode(errors='replace')[:120]})
print(json.dumps({'passed':6,'cases':report},ensure_ascii=False,indent=2))
