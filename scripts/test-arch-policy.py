#!/usr/bin/env python3
"""在隔离源码副本中验证架构检查器不会放过禁止的 import。"""
import argparse
import pathlib
import shutil
import subprocess
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', default='go-arch-lint')
    args = parser.parse_args()
    binary = shutil.which(args.binary)
    if not binary:
        parser.error('go-arch-lint not found; run make tools-arch')
    source = pathlib.Path(__file__).resolve().parents[1]
    with tempfile.TemporaryDirectory(prefix='ongrid-arch-policy-') as directory:
        root = pathlib.Path(directory)
        for filename in ('go.mod', '.go-arch-lint.yml'):
            shutil.copy2(source / filename, root / filename)
        for area in ('internal', 'cmd', 'api', 'scripts', 'tests'):
            (root / area).mkdir(parents=True, exist_ok=True)
            for directory in (source / area).rglob('*'):
                if directory.is_dir():
                    (root / directory.relative_to(source)).mkdir(parents=True, exist_ok=True)
            for filename in (source / area).rglob('*.go'):
                if filename.name.endswith('_test.go'):
                    continue
                target = root / filename.relative_to(source)
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(filename, target)

        def check(expected, dependency=''):
            result = subprocess.run(
                [binary, 'check', '--output-color=false'], cwd=root,
                text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                timeout=60,
            )
            if (result.returncode == 0) != expected:
                raise AssertionError(result.stdout)
            if not expected and dependency not in result.stdout:
                raise AssertionError('Failed for an unrelated reason:\n' + result.stdout)

        check(True)
        cases = [
            ('internal/manager/biz', 'internal/iam/model', False),
            ('internal/iam/biz', 'internal/manager/model/audit', False),
            ('internal/edgeagent/biz', 'internal/manager/model/audit', False),
            ('internal/pkg', 'internal/manager/model/audit', False),
            ('internal/skill', 'internal/manager/model/audit', False),
            ('internal/manager/service', 'internal/manager/data/audit/store', False),
            ('internal/manager/biz', 'internal/manager/data/audit/store', False),
            ('cmd/ongrid-edge', 'internal/iam/model', False),
            ('cmd/ongrid', 'internal/edgeagent/edgedirs', False),
            ('internal/manager/biz', 'internal/manager/model/audit', True),
            ('internal/manager/biz', 'internal/skill', True),
        ]
        for area, dependency, permitted in cases:
            fixture = root / area / 'arch_policy_fixture' / 'policy.go'
            fixture.parent.mkdir(parents=True, exist_ok=True)
            fixture.write_text(
                'package arch_policy_fixture\nimport _ "github.com/ongridio/ongrid/'
                + dependency + '"\n', encoding='utf-8',
            )
            try:
                check(permitted, dependency)
            finally:
                shutil.rmtree(fixture.parent)
        print(f'Architecture policy: baseline and {len(cases)} boundary cases passed')


if __name__ == '__main__':
    main()
