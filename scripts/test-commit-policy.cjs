const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const workflow = fs.readFileSync(path.join(__dirname, '../.github/workflows/commit-policy.yml'), 'utf8');
const script = workflow.split('          script: |\n')[1].replace(/^            /gm, '');
const run = new (Object.getPrototypeOf(async function () {}).constructor)('github', 'context', 'core', script);

async function check(messages, { previous, title = 'fix: handle empty input', eventTitle = title, latest, count = messages.length, stale = false, error = false } = {}) {
  const states = [], comments = [], failed = [];
  const pr = { number: 1, title, head: { sha: 'head' }, base: { ref: 'main', sha: 'base' }, commits: count };
  let reads = 0;
  const github = { rest: {
    repos: { createCommitStatus: async value => states.push(value) },
    pulls: { get: async () => ({ data: { ...pr, head: { sha: stale ? 'new' : 'head' }, ...(reads++ ? latest : {}) } }), listCommits: 'commits' },
    issues: { listComments: 'comments', createComment: async c => comments.push(c), updateComment: async c => comments.push(c) },
  }, paginate: async method => {
    if (error) throw new Error('API unavailable');
    return method === 'commits' ? messages.map((message, i) => ({ sha: String(i).padStart(40, '0'), commit: { message } })) : (previous ? [previous] : []);
  } };
  const context = { payload: { pull_request: { ...pr, title: eventTitle } }, repo: { owner: 'test', repo: 'test' }, serverUrl: 'https://github.com', runId: 1 };
  try { await run(github, context, { setFailed: m => failed.push(m) }); }
  catch (e) { if (!error) throw e; }
  assert(states.every(s => s.sha === 'head' && s.context === 'commit-policy'));
  return { state: states.at(-1).state, comments, failed };
}

(async () => {
  for (const message of ['fix: handle empty input', 'feat(api)!: change response\n\nBREAKING CHANGE: new shape', 'revert: remove cache', 'docs: improve reader’s guide']) {
    assert.equal((await check([message])).state, 'success');
  }
  for (const message of ['', 'fix: ', 'update docs', 'fix: 修复错误', 'fix: handle errors\n\n修复', 'fix: handle 𠀀', 'Merge branch main']) {
    const result = await check([message]);
    assert.equal(result.state, 'failure');
    assert.equal(result.comments.length, 1);
    assert.equal(result.failed.length, 1);
  }
  for (const title of ['修复登录错误', 'fix: 修复登录错误', 'update docs', '']) {
    const result = await check(['fix: resolve login error'], { title });
    assert.equal(result.state, 'failure', `Invalid PR title passed: ${title}`);
    assert.match(result.comments[0].body, /PR title:/);
  }
  assert.match(workflow, /types: \[[^\]\n]*\bedited\b[^\]\n]*\]/, 'PR title and base edits must trigger the check');
  for (const latest of [
    { title: 'fix: 修复登录错误' },
    { base: { ref: 'release', sha: 'base' } },
    { base: { ref: 'main', sha: 'new-base' } },
    { head: { sha: 'new-head' } },
  ]) {
    const result = await check(['fix: valid'], { latest });
    assert.equal(result.state, 'failure', 'Changed PR metadata must not receive a stale success');
    assert.equal(result.failed.length, 1);
    assert.equal(result.comments.length, 0);
  }
  assert.equal((await check(['fix: valid'], { title: 'feat(api)!: change response', eventTitle: '旧标题' })).state, 'success');
  assert.equal((await check(['fix: valid', 'bad'])).state, 'failure');
  assert.equal((await check(Array(250).fill('fix: valid'), { count: 251 })).state, 'failure');
  assert.equal((await check([])).state, 'failure');
  assert.equal((await check(['fix: valid'], { error: true })).state, 'failure');
  assert.equal((await check(['fix: valid'], { stale: true })).state, 'pending');
  const previous = { id: 7, user: { login: 'github-actions[bot]' }, body: '<!-- ongrid-commit-policy -->\nOld failure' };
  const invalidTitle = await check(['fix: valid'], { previous, title: 'fix: 修复错误' });
  const fixed = await check(['fix: valid'], { previous: { ...previous, body: invalidTitle.comments[0].body } });
  assert.equal(fixed.state, 'success');
  assert.equal(fixed.comments[0].comment_id, 7);
  assert.match(fixed.comments[0].body, /PR title and all commit messages pass/);
  assert.equal((await check(['fix: valid'], { previous: { ...previous, body: fixed.comments[0].body } })).comments.length, 0);
  assert.equal((await check(['bad'], { previous: { ...previous, user: { login: 'someone' } } })).comments[0].comment_id, undefined);
  assert.equal((await check(['bad @everyone <script>'])).comments[0].body.includes('@everyone'), false);
  console.log('Commit policy checks passed.');
})();
