"""Index chapter start pages from pdftotext -layout output, then verify on final render."""
import argparse
import json
from pathlib import Path
from enterprise_manual_content import CHAPTERS

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('text', type=Path)
parser.add_argument('output', type=Path)
args = parser.parse_args()
numbers = ['一','二','三','四','五','六','七','八','九','十','十一','十二','十三','十四','十五','十六','十七','十八','十九','二十']
titles = ['使用说明'] + [f'{numbers[i]}、{c["title"]}' for i,c in enumerate(CHAPTERS)] + ['附录　操作核验清单','附录　阈值保存结果','附录　告警关闭结果','附录　流程完成结果','附录　知识编辑填写','附录　页面入口图示']
normalize = lambda text: ''.join(text.split())
lookup = {normalize(t): t for t in titles}
index = {}
for page, content in enumerate(args.text.read_text().split('\f'), 1):
    lines = [line.strip() for line in content.splitlines() if line.strip()]
    if lines and normalize(lines[0]) in lookup:
        title = lookup[normalize(lines[0])]
        if title in index:
            raise ValueError(f'Duplicate chapter heading: {title}')
        index[title] = page
missing = set(titles) - set(index)
if missing:
    raise ValueError(f'Missing chapter headings: {missing}')
args.output.write_text(json.dumps(index,ensure_ascii=False,indent=2)+'\n')
print(f'Indexed {len(index)} headings')
