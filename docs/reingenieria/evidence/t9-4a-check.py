"""Assert every figure the T9.4a verdict cites is derivable from the committed
metrics artifact, and that the artifact is not an empty store."""
import json, re, sys

art = json.load(open('docs/reingenieria/evidence/t9-4a-metrics.json'))
rec = open('docs/reingenieria/f9-observabilidad.md').read()

e, f = art['executions'], art['findings']
assert e['logical_runs'] > 0, 'artifact describes an empty store'

expected = {
    'logical runs': e['logical_runs'],
    'measured runs': e['measured_runs'],
    'findings observed': f['observed'],
    'findings confirmed': f['confirmed'],
    'duration coverage observed': e['duration_nanos']['coverage']['observed'],
    'identity coverage observed': e['identity_coverage']['observed'],
    'cost coverage observed': e['cost_coverage']['observed'],
    'scope unknown': e['scope']['unknown'],
    'successful runs': e['successful_runs'],
    'stages': len(art['stages']),
    'remediation attempts': art['remediation']['attempts'],
}
section = re.search(r'### T9\.4a — observation sufficiency verdict(.*?)(?=\n### |\Z)', rec, re.S)
if not section:
    print('FAIL: no T9.4a verdict section in the phase record')
    sys.exit(1)
body = section.group(1)
missing = [k for k, v in expected.items() if str(v) not in body]
if missing:
    print('FAIL: verdict does not cite artifact figures for:', ', '.join(missing))
    sys.exit(1)
print('PASS: every cited figure is derivable from the artifact')
print('artifact:', {k: v for k, v in expected.items()})
