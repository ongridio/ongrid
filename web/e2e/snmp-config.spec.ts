import { expect, test } from '@playwright/test';

// Mocked transport: this suite never creates server-side credentials or inventory.
test.use({ storageState: { cookies: [], origins: [] } });
for (const theme of ['dark', 'light']) {
 for (const entry of ['list', 'detail', 'candidate', 'mobile']) {
  test(`network SNMP configuration ${entry} (${theme})`, async ({ page }, testInfo) => {
    if (entry === 'mobile') await page.setViewportSize({ width: 390, height: 844 });
    await page.addInitScript(() => {
      localStorage.setItem('ongrid.auth', JSON.stringify({ state: { token: 'ui-test', role: 'admin', email: 'test@example.invalid' }, version: 0 }));
      localStorage.setItem('ongrid-locale', 'en-US');
    });
    const host = { id: 3, name: 'Office scanner', scope: 'host', current_edge_id: 17, online: true, os: 'linux', ip_address: '192.0.2.3' };
    const device = { id: 9, name: 'Core switch', scope: 'network', os: 'network', arch: 'network', online: true, ip_address: '192.0.2.9', roles: ['network'] };
    const profile = { device_id: 9, management_address: '192.0.2.9', poll_scanner_host_device_id: 3, poll_enabled: true, poll_credential_name: 'switch-snmp', poll_port: 161, poll_interval_seconds: 300 };
    const saves: Record<string, unknown>[] = [];
    await page.route('**/api/v1/**', async route => {
      const request = route.request(); const url = new URL(request.url());
      let body: unknown = { items: [], total: 0 };
      if (url.pathname === '/api/v1/devices/9') body = device;
      if (url.pathname === '/api/v1/network-discovery/candidates') body = { items: [{ id: 9, ip_address: '192.0.2.9', observer_host_device_id: 3, source: 'lldp', status: 'identified' }] };
      if (url.pathname === '/api/v1/me') body = { id: 1, role: 'admin', email: 'test@example.invalid' };
      if (url.pathname === '/api/v1/devices') body = { items: url.searchParams.has('roles') ? [device] : [host, device], total: 2 };
      if (url.pathname === '/api/v1/secrets') body = { items: [{ id: 1, name: 'switch-snmp', type: 'snmp', field_keys: ['version', 'username'] }] };
      if (url.pathname === '/api/v1/devices/9/network') body = profile;
      if (url.pathname.endsWith('/snmp-test')) {
        expect(request.postDataJSON().scanner_host_device_id).toBe(3);
        body = { ok: true, sys_name: 'Core switch', interface_count: 24, duration_ms: 85 };
      }
      if (request.method() === 'PATCH' || url.pathname.endsWith('/snmp-config')) { saves.push(request.postDataJSON()); body = profile; }
      await route.fulfill({ json: body });
    });
    await page.goto((entry === 'list' || entry === 'mobile') ? '/devices?roles=network' : entry === 'detail' ? '/devices/9' : '/devices?view=network-discovery');
    await page.evaluate(theme => { document.documentElement.classList.toggle('light', theme === 'light'); document.documentElement.classList.toggle('dark', theme === 'dark'); }, theme);
    await page.getByRole('button', { name: entry === 'detail' ? 'Configure' : 'Configure SNMP', exact: true }).click();
    const modal = page.getByRole('dialog');
    await expect(modal.getByLabel('Management address')).toHaveValue('192.0.2.9');
    const save = modal.getByRole('button', { name: entry === 'candidate' ? 'Save and add device' : 'Save and enable', exact: true });
    if (entry === 'candidate') {
      await modal.getByRole('combobox', { name: 'SNMP credential', exact: true }).click();
      await page.getByRole('option', { name: 'switch-snmp', exact: true }).click();
    }
    await expect(save).toBeDisabled();
    await expect(modal.getByRole('combobox', { name: 'Scanner', exact: true })).toContainText('Office scanner');
    await modal.getByRole('button', { name: 'Test connection', exact: true }).click();
    await expect(modal.getByRole('status')).toContainText('24 interfaces');
    await page.screenshot({ path: testInfo.outputPath(`snmp-${theme}.png`), fullPage: true });
    expect(saves).toHaveLength(0);
    await modal.getByLabel('Management address').fill('192.0.2.10');
    await expect(save).toBeDisabled();
    await modal.getByRole('button', { name: 'Test connection', exact: true }).click();
    await expect(modal.getByRole('status')).toBeVisible();
    await save.click(); await expect(modal).toBeHidden();
    expect(saves).toEqual([expect.objectContaining({ scanner_host_device_id: 3, address: '192.0.2.10', credential_name: 'switch-snmp' })]);
  });
}

}

for (const theme of ['light', 'dark']) {
 test(`manual SNMP creation from empty network inventory (${theme})`, async ({page}, info) => {
  await page.addInitScript(theme => {
   localStorage.setItem('ongrid.auth', JSON.stringify({state:{token:'ui-test',role:'admin',email:'test@example.invalid'},version:0}));
   localStorage.setItem('ongrid-locale','en-US'); localStorage.setItem('ongrid-theme-preference',theme);
  }, theme);
  const host={id:3,name:'Office scanner',scope:'host',current_edge_id:17,online:true,os:'linux'};
  const network={id:9,name:'Manual switch',os:'network',arch:'network',roles:['network'],ip_address:'192.0.2.88'};
  const profile={device_id:9,management_address:'192.0.2.88',poll_scanner_host_device_id:3,poll_enabled:true,poll_credential_name:'switch-snmp',poll_port:161,poll_interval_seconds:300};
  const creates: unknown[]=[];
  await page.route('**/api/v1/**', async route=>{
   const req=route.request(); const url=new URL(req.url()); let body:unknown={items:[],total:0}; let status=200;
   if(url.pathname==='/api/v1/me') body={id:1,role:'admin',email:'test@example.invalid'};
   if(url.pathname==='/api/v1/devices') body={items:url.searchParams.has('roles')?(creates.length?[network]:[]):[host],total:creates.length};
   if(url.pathname==='/api/v1/secrets') body={items:[{id:1,name:'switch-snmp',type:'snmp',field_keys:['community']}]};
   if(url.pathname==='/api/v1/devices/9/network') body=profile;
   if(url.pathname==='/api/v1/network-devices/snmp-test') {expect(req.postDataJSON()).toMatchObject({address:'192.0.2.88',scanner_host_device_id:3});body={ok:true,sys_name:'Manual switch',interface_count:8,duration_ms:5};}
   if(url.pathname==='/api/v1/network-devices') {creates.push(req.postDataJSON());body=profile;status=201;}
   await route.fulfill({status,json:body});
  });
  await page.goto('/devices?roles=network');
  await expect(page.getByText('No network devices yet. Add one by entering its SNMP address.')).toBeVisible();
  await expect(page.locator('.device-list-table')).toHaveCount(0);
  for (const width of [1280, 640]) {
   await page.setViewportSize({width,height:800});
   expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
   await page.screenshot({path:info.outputPath(`empty-network-${theme}-${width}.png`),fullPage:true});
  }
  await page.setViewportSize({width:1280,height:800});
  await expect(page.getByRole('button',{name:'Add SNMP device',exact:true})).toHaveCount(2);
  await page.getByRole('button',{name:'Add SNMP device',exact:true}).first().click();
  const modal=page.getByRole('dialog');
  await expect(modal.getByLabel('Management address')).toHaveValue('');
  await modal.getByLabel('Management address').fill('192.0.2.88');
  await modal.getByRole('combobox',{name:'SNMP credential',exact:true}).click();
  await page.getByRole('option',{name:'switch-snmp',exact:true}).click();
  const save=modal.getByRole('button',{name:'Save and add device',exact:true});
  await expect(save).toBeDisabled();
  await modal.getByRole('button',{name:'Test connection',exact:true}).click();
  await expect(modal.getByRole('status')).toContainText('Test passed');
  expect(creates).toHaveLength(0);
  await page.screenshot({path:info.outputPath(`manual-snmp-${theme}.png`),fullPage:true});
  await save.click(); await expect(modal).toBeHidden();
  await expect(page.getByText('Manual switch',{exact:true})).toBeVisible();
  await expect(page.locator('.device-list-table table')).toBeVisible();
  expect(creates).toEqual([expect.objectContaining({address:'192.0.2.88',scanner_host_device_id:3,credential_name:'switch-snmp'})]);
 });
}
