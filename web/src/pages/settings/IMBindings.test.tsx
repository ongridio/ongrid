import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, expect, it, vi } from 'vitest';
import { request } from '@/api/client';
import { IMBindings } from './IMBindings';
import type { IMApp } from '@/api/imbridge';
vi.mock('@/api/client', async importOriginal => ({...await importOriginal<typeof import('@/api/client')>(), request: vi.fn()}));
vi.mock('@/components/ui/Select', () => ({Select: ({onValueChange, ...props}: {onValueChange:(v:string)=>void}) => <select {...props} onChange={e=>onValueChange(e.target.value)} />}));
const apps = [{id:1,name:'测试机器人',provider:'feishu',enabled:true,mode:'stream'}] as IMApp[];
beforeEach(()=>{vi.clearAllMocks();localStorage.setItem('ongrid-locale','zh-CN');});
it('detects a claimed identity but requires explicit confirmation before binding', async()=>{
 let confirmed=false;
 vi.mocked(request).mockImplementation(async(method,path)=>{
  if(path==='/im/bindings') return {data:{items:confirmed?[{id:'b',app_id:1,subject:'test-user'}]:[]}};
  if(path.endsWith('/confirm')) {confirmed=true;return {};}
  return {data:{id:'c',code:method==='POST'?'one-time-code':undefined,claimed:method==='GET',subject:'test-user',expires_at:new Date(Date.now()+600000).toISOString()}};
 });
 render(<IMBindings apps={apps}/>);
 fireEvent.change(screen.getByLabelText('绑定机器人'),{target:{value:'1'}});
 fireEvent.click(screen.getByRole('button',{name:'开始绑定'}));
 expect(await screen.findByText('/bind one-time-code')).toBeVisible();
 await waitFor(()=>expect(screen.getByRole('button',{name:'确认绑定此账号'})).toBeVisible(),{timeout:3000});
 expect(confirmed).toBe(false);
 fireEvent.click(screen.getByRole('button',{name:'确认绑定此账号'}));
 await waitFor(()=>expect(screen.getByText(/绑定成功，可以返回/)).toBeVisible());
 expect(screen.queryByText('/bind one-time-code')).not.toBeInTheDocument();
 expect(screen.getByRole('button',{name:'解绑'})).toBeVisible();
});
it('expires the command without offering confirmation', async()=>{
 vi.mocked(request).mockImplementation(async(_method,path)=>path==='/im/bindings'?{data:{items:[]}}:{data:{id:'c',code:'expired-code',claimed:true,subject:'test-user',expires_at:new Date(Date.now()-1000).toISOString()}});
 render(<IMBindings apps={apps}/>);
 fireEvent.change(screen.getByLabelText('绑定机器人'),{target:{value:'1'}});
 fireEvent.click(screen.getByRole('button',{name:'开始绑定'}));
 expect(await screen.findByText(/绑定码已失效/)).toBeVisible();
 expect(screen.queryByRole('button',{name:'确认绑定此账号'})).not.toBeInTheDocument();
});
