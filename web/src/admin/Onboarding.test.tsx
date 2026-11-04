// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { Onboarding } from './Onboarding';

afterEach(() => { cleanup(); vi.restoreAllMocks(); });
function fill(label:string,value:string){fireEvent.change(screen.getByLabelText(label),{target:{value}});}
it('generates a downloadable preparation bundle using supported public scopes and real scripts', () => {
 const createURL=vi.fn(()=> 'blob:plan');Object.defineProperty(URL,'createObjectURL',{configurable:true,value:createURL});Object.defineProperty(URL,'revokeObjectURL',{configurable:true,value:vi.fn()});
 const click=vi.spyOn(HTMLAnchorElement.prototype,'click').mockImplementation(()=>{});
 const fetcher=vi.spyOn(globalThis,'fetch');
 render(<Onboarding sandbox />);
 expect(screen.getByRole('heading',{name:'三方接入工作台'})).toBeDefined();
 expect(screen.getByText(/不会在浏览器保存客户端密钥/)).toBeDefined();
 expect(screen.queryByRole('checkbox',{name:'openid'})).toBeNull();
 expect((screen.getByRole('checkbox',{name:'accounts:read'}) as HTMLInputElement).checked).toBe(true);
 expect((screen.getByRole('button',{name:'生成接入计划'}) as HTMLButtonElement).disabled).toBe(true);
 fill('产品标识','notes');fill('回调地址','https://notes.example.com/auth/callback');
 fill('身份提供方地址','https://identity.example.com');
 fireEvent.click(screen.getByRole('button',{name:'生成接入计划'}));
 expect(click).toHaveBeenCalledTimes(1);expect(createURL).toHaveBeenCalled();expect(fetcher).not.toHaveBeenCalled();
 expect(screen.getByRole('button',{name:'下载准备命令'})).toBeDefined();
 expect(screen.getByRole('button',{name:'下载计费配置'})).toBeDefined();
 expect(screen.getByRole('button',{name:'下载公开参数'})).toBeDefined();
 expect(screen.getByText(/scripts\/product_client.py.*prepare/)).toBeDefined();
 expect(screen.getByText(/不代表正式支付已接入/)).toBeDefined();
});

it('separates mature product runtime authority and owned pricing from public client scopes', () => {
 render(<Onboarding />);
 expect(screen.getByText(/私有 runtime 服务权限/)).toBeDefined();
 expect(screen.getByText(/原产品 Key.*价格.*原产品/)).toBeDefined();
 expect(screen.queryByRole('checkbox',{name:'identity:import'})).toBeNull();
 expect(screen.queryByRole('checkbox',{name:'cash:payment'})).toBeNull();
 fill('产品标识','../unsafe');fill('回调地址','javascript:alert(1)');fill('身份提供方地址','https://identity.example.com');
 expect((screen.getByRole('button',{name:'生成接入计划'}) as HTMLButtonElement).disabled).toBe(true);
});
