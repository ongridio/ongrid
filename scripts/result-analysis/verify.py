"""Check browser/API-exported answers against SQLite; never execute model code.

Usage: python3 scripts/result-analysis/verify.py answers.json
answers.json maps case IDs to either the final assistant text or parsed JSON.
"""
import json
import pathlib
import sys
from fixture import oracle

def parse(value):
    if isinstance(value,dict): return value
    # Permit an explanatory sentence or a JSON code fence around the answer.
    decoder=json.JSONDecoder()
    for i,c in enumerate(value):
        if c!='{': continue
        try: return decoder.raw_decode(value[i:])[0]
        except ValueError: pass
    raise ValueError('no JSON answer found')

def verify(answers):
    expected=oracle()
    failures=[]
    for name,want in expected.items():
        try:
            actual=parse(answers[name])
            actual={k:actual[k] for k in want}
            assert actual==want, {'actual':actual,'expected':want}
        except (KeyError,ValueError,AssertionError) as e: failures.append({'case':name,'error':str(e)})
    return {'passed':len(expected)-len(failures),'total':len(expected),'failures':failures}

if __name__=='__main__':
    if sys.argv[1]=='--self-test':
        answers=oracle(); assert verify(answers)['passed']==6
        answers['top']['top'][0]['id']+=1
        assert verify(answers)['passed']==5
        print('Exact-answer verifier self-test passed'); sys.exit(0)
    report=verify(json.loads(pathlib.Path(sys.argv[1]).read_text()))
    print(json.dumps(report,ensure_ascii=False,indent=2))
    sys.exit(bool(report['failures']))
