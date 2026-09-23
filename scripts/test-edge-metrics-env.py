#!/usr/bin/env python3
"""Run the installers' actual metrics-setting preservation blocks without root."""
import os
from pathlib import Path
import subprocess
import tempfile

root = Path(__file__).resolve().parents[1]
for name in ("install.sh", "install-edge.sh"):
    source = (root / "deploy/install/edge" / name).read_text()
    capture = source.split('# Preserve only this setting;', 1)[1].split('\nfi\n', 1)[0]
    capture = capture.split('\n', 1)[1] + '\nfi\n'
    append = 'if [[ -n "$METRICS_ENV_LINE" ]]; then' + source.split(
        'if [[ -n "$METRICS_ENV_LINE" ]]; then', 1
    )[1].split('\nfi\n', 1)[0] + '\nfi\n'
    script = 'log_error() { printf "%s\\n" "$*" >&2; }\n' + capture
    script += 'printf "NEW_CREDENTIALS=placeholder\\n" > "$ENV_FILE"\n' + append
    for old, new, expected in (
        (None, None, None),
        (None, ':19101', ':19101'),
        (':19101', None, ':19101'),
        ('"[::1]:19101"', None, '"[::1]:19101"'),
        (':19101', ':29101', ':29101'),
        (':19101', '', ''),
        (':19101', ':29101\nINJECTED=value', 'reject'),
    ):
        with tempfile.TemporaryDirectory() as tmp:
            env_file = Path(tmp) / 'edge.env'
            if old is not None:
                env_file.write_text('OLD_CREDENTIALS=placeholder\nONGRID_EDGE_METRICS_ADDR=' + old + '\n')
            env = dict(os.environ, ENV_FILE=str(env_file))
            env.pop('ONGRID_EDGE_METRICS_ADDR', None)
            if new is not None:
                env['ONGRID_EDGE_METRICS_ADDR'] = new
            result = subprocess.run(['bash', '-eu', '-c', script], env=env, capture_output=True, text=True)
            if expected == 'reject':
                assert result.returncode != 0, (name, 'accepted newline')
                assert env_file.read_text().startswith('OLD_CREDENTIALS='), name
                continue
            assert result.returncode == 0, (name, result.stderr)
            lines = env_file.read_text().splitlines()
            actual = [line for line in lines if line.startswith('ONGRID_EDGE_METRICS_ADDR=')]
            want = [] if expected is None else ['ONGRID_EDGE_METRICS_ADDR=' + expected]
            assert actual == want, (name, actual, want)
            assert 'OLD_CREDENTIALS=placeholder' not in lines, name
    print(name + ': metrics address install/reinstall checks passed')
