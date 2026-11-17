"""Run inside the test Manager; sample each worker’s post-exec /proc VmHWM (Linux only)."""
import json,os,pathlib,re,subprocess,tempfile,time,uuid

def run(n,format='json',retain=False):
    value={'n':1,'id':9007199254740993,'note':'x'*150}
    record=json.dumps(value,separators=(',',':'))
    native='['+','.join([record]*n)+']'
    data=(json.dumps({'content':[{'text':native}]}) if format=='json_text' else native).encode()
    source={'result_id':str(uuid.uuid4()),'format':format,'pointer':'/content/0/text' if format=='json_text' else '', 'bytes':len(data)}
    expr='[inputs.value] | {count:length,sum:(map(.n)|add)}' if retain else 'reduce inputs as $r ({count:0,sum:0}; .count+=1 | .sum+=$r.value.n)'
    header=json.dumps({'expression':expr,'inputs':[source]}).encode()+b'\n'
    with tempfile.TemporaryDirectory(prefix='analysis-memory-') as tmp:
        pth=pathlib.Path(tmp)
        with (pth/'stdin').open('wb') as f:f.write(header);f.write(data)
        with (pth/'stdin').open('rb') as stdin,(pth/'stdout').open('wb') as out,(pth/'stderr').open('wb') as err:
            start=time.monotonic()
            p=subprocess.Popen(['/ongrid','--internal-result-analysis'],stdin=stdin,stdout=out,stderr=err,cwd=tmp,env={'HOME':'.','PATH':'/usr/bin:/bin','GOMEMLIMIT':'256MiB','GOMAXPROCS':'1','GOTRACEBACK':'none'})
            peak=0
            while True:
                try:
                    if os.readlink('/proc/'+str(p.pid)+'/exe')=='/ongrid':
                        status_text=pathlib.Path('/proc/'+str(p.pid)+'/status').read_text()
                        match=re.search(r'^VmHWM:\s+(\d+) kB',status_text,re.M)
                        if match:peak=max(peak,int(match.group(1)))
                except (FileNotFoundError,ProcessLookupError): pass
                pid,status,usage=os.wait4(p.pid,os.WNOHANG)
                if pid:break
                time.sleep(0.001)
            p.returncode=os.waitstatus_to_exitcode(status)
            elapsed=time.monotonic()-start
        assert p.returncode==0,(n,format,(pth/'stderr').read_text()[:500])
        result=json.loads((pth/'stdout').read_text())
        assert result['values']==[{'count':n,'sum':n}] and result['coverage'][0]['complete'],result
        return {'rows':n,'format':format,'retain_array':retain,'input_bytes':len(data),'sampled_worker_hwm_kib':peak,'seconds':round(elapsed,3)}
rows=[run(n,f) for f in ['json','json_text'] for n in [25000,250000]]
rows.append(run(25000,retain=True))
print(json.dumps(rows,indent=2))
