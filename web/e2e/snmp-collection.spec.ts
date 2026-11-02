import { expect, test } from '@playwright/test';
test.use({storageState:{cookies:[],origins:[]}});
for(const theme of ['light','dark']) {
 test(`SNMP collection is distinct from direct connectivity (${theme})`,async({page},info)=>{
  await page.addInitScript(theme=>{
   localStorage.setItem('ongrid.auth',JSON.stringify({state:{token:'test',role:'admin',email:'test@example.invalid'},version:0}));
   localStorage.setItem('ongrid-locale','en-US');localStorage.setItem('ongrid-theme-preference',theme);
  },theme);
  await page.route('**/api/v1/**',async route=>{
   const p=new URL(route.request().url()).pathname;let body:unknown={items:[],total:0};
   if(p==='/api/v1/me')body={id:1,role:'admin'};
   if(p==='/api/v1/devices/9')body={id:9,name:'SNMP device',os:'network',node_id:90,roles:['network']};
   if(p==='/api/v1/devices/9/network')body={device_id:9,poll_enabled:false,poll_scanner_host_device_id:3,interfaces:[{if_index:2,name:"eth0",interface_kind:"ethernet",mac:"52:54:00:89:3f:33",addresses:["10.0.4.17"],admin_status:"up",oper_status:"up"}]};
   if(p==='/api/v1/topology/nodes/90')body={id:90,type:'device',name:'SNMP device'};
   if(p==='/api/v1/topology/nodes/30')body={id:30,type:'device',name:'Office scanner'};
   if(p==='/api/v1/topology/relations')body={items:[{id:1,src_id:30,dst_id:90,type:'collects_from',props:{source:'network_collection',enabled:false}}],total:1};
   if(p==='/api/v1/topology/relation-types')body={items:[{name:'collects_from',semantics_tag:'observation',propagates_failure:false}]};
   await route.fulfill({json:body});
  });
  await page.goto('/devices/9?tab=topology');
  await expect(page.getByText('Collection',{exact:true})).toBeVisible();
  await expect(page.getByText('Office scanner',{exact:false})).toBeVisible();
  await expect(page.getByText('Polling disabled',{exact:true})).toBeVisible();
  await expect(page.getByText(/Collection does not imply a direct interface connection/)).toBeVisible();
  await expect(page.getByText('connected_to',{exact:true})).toHaveCount(0);
  await page.screenshot({path:info.outputPath(`collection-${theme}.png`),fullPage:true});
  await expect(page.locator('header.app-header')).toHaveCSS('border-bottom-width','0px');
  await page.getByRole('tab',{name:'Interfaces',exact:true}).click();
  const address=await page.getByRole('columnheader',{name:'Addresses',exact:true}).boundingBox();
  const admin=await page.getByRole('columnheader',{name:'Admin',exact:true}).boundingBox();
  expect(address!.width).toBeGreaterThanOrEqual(190);
  expect(admin!.x).toBeGreaterThanOrEqual(address!.x+address!.width-1);
  await page.screenshot({path:info.outputPath(`interfaces-${theme}.png`),fullPage:true});
 });
}
