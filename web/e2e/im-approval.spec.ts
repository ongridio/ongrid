import { test, expect } from '@playwright/test';
const approval = {id:'test-approval',kind:'agent_tool',runtime_version:'v3',title:'重启测试服务 / Restart test service',summary:'目标：test-host；操作：重启测试服务。执行前请核对维护窗口。',payload:JSON.stringify({tool_name:'restart_service',args_json:'{"host_id":123,"service":"test-service"}',execution_token:'never-show'}),source:'agent',status:'pending',proposed_by:1,created_at:'2026-11-01T00:00:00Z'};
for (const theme of ['dark','light']) for(const locale of ['zh-CN','en-US']) {
 test(`${theme} ${locale}: responsive approval review and explicit confirmation`, async({page},info) => {
  let decisions=0; let status='pending';
  await page.addInitScript(({theme,locale}) => {
   localStorage.setItem('ongrid.auth',JSON.stringify({state:{token:'test',role:'admin',email:'test@example.invalid'},version:0}));
   localStorage.setItem('ongrid-locale',locale);
   localStorage.setItem('ongrid.theme',JSON.stringify({state:{mode:theme},version:0}));
  },{theme,locale});
  await page.route('**/api/v1/**',async route=>{
   const path=new URL(route.request().url()).pathname;
   if(path.endsWith('/approvals/test-approval/approve')){decisions++;status='executed';await route.fulfill({json:{...approval,status}});return;}
   if(path.endsWith('/approvals/test-approval')){await route.fulfill({json:{...approval,status}});return;}
   await route.fulfill({json:{items:[],pending:1,total:0}});
  });
  await page.goto('/approvals/test-approval');
  await expect(page.getByRole('heading',{name:approval.title})).toBeVisible();
  await page.evaluate(theme=>{document.documentElement.classList.remove('light','dark');document.documentElement.classList.add(theme);},theme);
  expect(decisions).toBe(0);
  await expect(page.getByText('never-show',{exact:false})).toHaveCount(0);
  for(const width of [390,768,1280,1920]){
   await page.setViewportSize({width,height:900});
   await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
   await expect(page.getByRole('heading',{name:approval.title})).toHaveCount(1);
   await expect(page.getByRole('button',{name:/批准并执行|Approve and execute/})).toBeVisible();
   expect(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBe(true);
   await page.screenshot({path:info.outputPath(`${theme}-${locale}-${width}.png`),fullPage:true});
  }
  await page.getByRole('button',{name:/批准并执行|Approve and execute/}).click();
  await expect(page.getByRole('dialog')).toBeVisible();expect(decisions).toBe(0);
  await page.getByRole('dialog').getByRole('button',{name:/^取消$|^Cancel$/}).click();expect(decisions).toBe(0);
  await page.getByRole('button',{name:/批准并执行|Approve and execute/}).click();
  await page.getByRole('dialog').getByRole('button',{name:/^确认$|^Confirm$/}).click();
  await expect(page.getByRole('button',{name:/批准并执行|Approve and execute/})).toHaveCount(0);expect(decisions).toBe(1);
 });
}
test('login returns to the approval without executing it',async({page})=>{
 let decisions=0;
 await page.route('**/api/v1/**',async route=>{
  const path=new URL(route.request().url()).pathname;
  if(path.endsWith('/auth/login')){await route.fulfill({json:{access_token:'test',user:{role:'admin',email:'test@example.invalid'}}});return;}
  if(route.request().method()==='POST')decisions++;
  await route.fulfill({json:path.endsWith('/approvals/test-approval')?approval:{items:[],pending:0,total:0}});
 });
 await page.goto('/approvals/test-approval');await expect(page).toHaveURL(/\/login$/);
 await page.locator('#email').fill('test@example.invalid');await page.locator('#password').fill('test-only-password');
 await page.locator('button[type="submit"]').click();await expect(page).toHaveURL(/\/approvals\/test-approval$/);
 await expect(page.getByRole('heading',{name:approval.title})).toBeVisible();expect(decisions).toBe(0);
});

for (const theme of ['dark','light']) test(`IM identity binding ${theme}: explicit two-sided confirmation`,async({page},info)=>{
 let confirmed=0;let removed=0;
 const bot={id:9,name:'Approval test bot',provider:'telegram',mode:'stream',enabled:true,app_id:'test_bot',has_secret:true};
 await page.addInitScript(theme=>{
  localStorage.setItem('ongrid.auth',JSON.stringify({state:{token:'test',role:'admin',email:'test@example.invalid'},version:0}));
  localStorage.setItem('ongrid-locale','zh-CN');
  localStorage.setItem('ongrid.theme',JSON.stringify({state:{mode:theme},version:0}));
 },theme);
 await page.route('**/api/v1/**',async route=>{
  const path=new URL(route.request().url()).pathname;const method=route.request().method();
  if(path.endsWith('/im/apps'))return route.fulfill({json:{items:[bot],total:1}});
  if(path.endsWith('/im/bindings/challenges') && method==='POST')return route.fulfill({json:{code:0,data:{id:'challenge',code:'test-one-time-code',claimed:false,expires_at:'2099-01-01T00:00:00Z'}}});
  if(path.endsWith('/im/bindings/challenges/challenge/confirm')){confirmed++;return route.fulfill({json:{code:0,data:null}});}
  if(path.endsWith('/im/bindings/challenges/challenge'))return route.fulfill({json:{code:0,data:{id:'challenge',claimed:true,subject:'platform-user-42',expires_at:'2099-01-01T00:00:00Z'}}});
  if(path.endsWith('/im/bindings/binding')&&method==='DELETE'){removed++;return route.fulfill({json:{code:0,data:null}});}
  if(path.endsWith('/im/bindings'))return route.fulfill({json:{code:0,data:{items:confirmed && !removed?[{id:'binding',app_id:9,subject:'platform-user-42'}]:[]}}});
  return route.fulfill({json:{items:[],total:0,pending:0}});
 });
 await page.goto('/settings/channels');
 await page.evaluate(theme=>{document.documentElement.classList.remove('light','dark');document.documentElement.classList.add(theme);},theme);
 await expect(page.locator('html')).toHaveClass(new RegExp(theme));
 await page.getByRole('combobox',{name:'绑定机器人'}).click();
 await page.getByRole('option',{name:'Approval test bot'}).click();
 await page.getByRole('button',{name:'开始绑定'}).click();
 await expect(page.getByText('/bind test-one-time-code')).toBeVisible();expect(confirmed).toBe(0);
 await expect(page.getByRole('button',{name:'确认绑定此账号'})).toHaveCount(0);
 await page.getByRole('button',{name:'检查平台确认'}).click();
 await expect(page.getByRole('button',{name:'确认绑定此账号'})).toBeVisible();expect(confirmed).toBe(0);
 for(const width of [390,1280]){await page.setViewportSize({width,height:1000});await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));await page.screenshot({path:info.outputPath(`im-binding-${theme}-${width}.png`),fullPage:true});expect(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBe(true);}
 await page.getByRole('button',{name:'确认绑定此账号'}).click();await expect(page.getByRole('button',{name:'解绑'})).toBeVisible();expect(confirmed).toBe(1);
 await page.getByRole('button',{name:'解绑'}).click();await expect(page.getByRole('button',{name:'解绑'})).toHaveCount(0);expect(removed).toBe(1);
});
