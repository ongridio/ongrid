#!/usr/bin/env python3
"""Replay identical model output through the baseline and current extractors."""
import argparse
import pathlib
import re
import subprocess
import os

BASELINE = "6b7ee6258fde6f7157c96bb15ce731b082cde122"
ROOT = pathlib.Path(__file__).resolve().parents[1]
PACKAGE = "internal/manager/biz/alert/investigator"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("output", type=pathlib.Path)
    args = parser.parse_args()
    output = args.output.resolve()
    source = subprocess.check_output(["git", "show", f"{BASELINE}:{PACKAGE}/report_extractor.go"], cwd=ROOT, text=True)
    usecase = subprocess.check_output(["git", "show", f"{BASELINE}:{PACKAGE}/usecase.go"], cwd=ROOT, text=True)
    source += "\n" + usecase[usecase.index("// firstParagraphOneLine"):]
    # Compile the actual old implementation beside the new one; only symbols
    # change to avoid collisions. Both consume the same deterministic reply.
    symbols = re.findall(r"^func (\w+)\(", source, re.M) + re.findall(r"^type (\w+) ", source, re.M)
    symbols += ["extractStructured", "extractorSystemPrompt", "relatedAlertHalfWindow", "relatedAlertLimit", "buildRelatedAlertsJSON"]
    for name in set(symbols):
        source = re.sub(r"\b" + name + r"\b", "baseline" + name[0].upper() + name[1:], source)
    source = source.replace('import (', 'import (\n"testing"', 1)
    source += '''
func TestEvidenceBaselineReplay(t *testing.T) {
    uc, _, sum, incident := replayInputs()
    fields := uc.baselineExtractStructured(context.Background(), incident, replayNarrative, 2, "zh")
    if fields.EvidenceCheckJSON != nil || fields.Confidence == nil || *fields.Confidence != 0.95 || sum.calls != 1 {
        t.Fatal("unexpected baseline behavior")
    }
    writeReplayArtifact(t, "old", fields)
    t.Logf("baseline: one model call, no source reads, no citation check")
}
'''
    generated = ROOT / PACKAGE / "zz_baseline_replay_generated_test.go"
    with generated.open("x") as file:
        file.write(source)
    try:
        subprocess.run(["go", "test", "./" + PACKAGE, "-run", "^TestEvidence(Baseline)?Replay$", "-count=1", "-v"], cwd=ROOT,
                       env={**os.environ, "ONGRID_EVIDENCE_REPLAY_DIR": str(output)}, check=True)
    finally:
        generated.unlink()
    print(f"Baseline: {BASELINE}\nReports and source details: {output}")


if __name__ == "__main__":
    main()
