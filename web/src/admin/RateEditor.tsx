import { useState } from 'react';
import type { FormEvent } from 'react';
import { idempotencyKey, request } from '../api';
import { Modal, Notice } from '../ui';

const tokenLabels:Record<string,string>={input:'输入',output:'输出',cache_read:'缓存读取',cache_write:'缓存写入',reasoning:'推理'};
// Keep these choices aligned with internal/metering.ValidatePrice.
const operationUnits:Record<string,string[]>={chat:['token','request'],responses:['token'],embedding:['token'],rerank:['token'],speech:['character'],transcription:['audio_second'],image_generation:['image'],image_edit:['image'],ocr:['request'],video:['video_second','provider_unit']};
const dimensions:Record<string,[string,string]>={character:['characters','字符'],audio_second:['audio','音频秒'],image:['images','图片'],request:['requests','请求'],video_second:['video','视频秒'],provider_unit:['provider','计量']};
function quantity(raw:string,integer:boolean) {
 if(!/^(0|[1-9]\d*)(\.\d{1,9})?$/.test(raw)||(integer&&raw.includes('.')))throw new Error('计费量须为非负数；离散单位只接受整数，秒或供应商单位最多 9 位小数。');
 const [whole,fraction='']=raw.split('.');const value=(whole+fraction).replace(/^0+(?=\d)/,'');
 if(value.length>38)throw new Error('计费量超过 38 位整数范围。');
 return {value,scale:fraction.length};
}
export function RateEditor({csrf,onSaved}:{csrf?:string;onSaved:()=>void}) {
 const [open,setOpen]=useState(false),[busy,setBusy]=useState(false),[error,setError]=useState(''),[key,setKey]=useState(idempotencyKey);
 const [values,setValues]=useState<Record<string,string>>({asset:'CREDIT/6',semantics:'exclusive_v1',operation:'chat',unit:'token',policy:'provider_reported_v1'});
 const [selected,setSelected]=useState(['input','output']);
 const metered=values.semantics==='metered_v2',unit=values.unit;
 const terms=metered?(unit==='token'?selected:[dimensions[unit][0]]):Object.keys(tokenLabels);
 function set(name:string,value:string) {setValues(v=>({...v,[name]:value}));}
 function operation(value:string) {setValues(v=>({...v,operation:value,unit:operationUnits[value][0]}));setSelected(value==='rerank'||value==='embedding'?['input']:['input','output']);}
 const label=(dimension:string)=>tokenLabels[dimension]||dimensions[unit]?.[1]||dimension;
 async function submit(event:FormEvent) {
  event.preventDefault();setBusy(true);setError('');
  try {
   if(!terms.length)throw new Error('至少选择一个计量维度。');
   const prices=Object.fromEntries(terms.map(dimension=>{
    const numerator=values[`${dimension}_n`]||'0',denominator=values[`${dimension}_d`]||'1';
    if(!/^(0|[1-9]\d{0,37})$/.test(numerator)||! /^[1-9]\d{0,37}$/.test(denominator))throw new Error('价格分子须为非负整数，分母须为正整数，最多 38 位。');
    return [dimension,{numerator,denominator}];
   }));
   if(metered&&values.step&&quantity(values.step,['token','character','image','request'].includes(unit)).value==='0')throw new Error('计费步长必须大于零。');
   const rate={id:values.id,model:values.model,asset:values.asset,status:'published',semantics:values.semantics,...(metered?{metered:{version:'metered_v2',operation:values.operation,policy:values.policy,terms:terms.map(dimension=>({key:dimension,unit,price:prices[dimension],...(unit==='provider_unit'?{provider_unit:values.provider_unit}:{}),...(values.step?{step:quantity(values.step,['token','character','image','request'].includes(unit))}:{}),...(values.minimum?{minimum:quantity(values.minimum,['token','character','image','request'].includes(unit))}:{})}))}}:prices)};
   await request('/admin/v1/rates',{method:'POST',csrf,key,body:{version:0,rate,reason:values.reason}});
   setOpen(false);onSaved();
  }catch(e){setError((e as Error).message);}finally{setBusy(false);}
 }
 return <><button onClick={()=>{setKey(idempotencyKey());setError('');setOpen(true);}}>发布费率版本</button>{open?<Modal title="发布费率版本" onClose={()=>{if(!busy)setOpen(false);}}><form className="admin-form" onSubmit={submit}>
 <Notice>UniHub 托管模型费率；原生产品价格仍由原产品管理。价格按所选资产的最小单位填写。</Notice>
 <label>费率标识<input required maxLength={128} value={values.id||''} onChange={e=>set('id',e.target.value)}/></label>
 <label>模型<input required maxLength={128} value={values.model||''} onChange={e=>set('model',e.target.value)}/></label>
 <label>资产<select value={values.asset} onChange={e=>set('asset',e.target.value)}><option>CREDIT/6</option><option>CNY/6</option></select></label>
 <label>计价协议<select value={values.semantics} onChange={e=>set('semantics',e.target.value)}><option value="exclusive_v1">exclusive_v1 · 旧版 chat token</option><option value="metered_v2">metered_v2 · 按操作原生计量</option></select></label>
 {metered?<><label>操作<select value={values.operation} onChange={e=>operation(e.target.value)}>{Object.keys(operationUnits).map(op=><option key={op}>{op}</option>)}</select></label>
 <label>计量单位<select value={unit} onChange={e=>set('unit',e.target.value)}>{operationUnits[values.operation].map(u=><option key={u}>{u}</option>)}</select></label>
 <label>计费策略标识<input required maxLength={128} value={values.policy} onChange={e=>set('policy',e.target.value)}/></label>
 <p className="admin-form-hint">策略标识必须与服务端适配器的计量规则一致；原生计量可配置不代表该供应商操作已经验收。</p>
 {unit==='token'?<fieldset><legend>计量维度</legend>{(values.operation==='rerank'?['input']:Object.keys(tokenLabels)).map(d=><label key={d}><input type="checkbox" checked={selected.includes(d)} disabled={values.operation==='rerank'} onChange={e=>setSelected(current=>e.target.checked?[...current,d]:current.filter(k=>k!==d))}/>{tokenLabels[d]}</label>)}</fieldset>:null}
 {unit==='provider_unit'?<label>供应商计量命名空间<input required maxLength={128} value={values.provider_unit||''} onChange={e=>set('provider_unit',e.target.value)}/></label>:null}</>:null}
 {terms.map(d=><div className="admin-rate-price" key={d}><label>{label(d)}价格分子<input required inputMode="numeric" pattern="0|[1-9][0-9]{0,37}" maxLength={38} value={values[`${d}_n`]||'0'} onChange={e=>set(`${d}_n`,e.target.value)}/></label><label>{label(d)}价格分母<input required inputMode="numeric" pattern="[1-9][0-9]{0,37}" maxLength={38} value={values[`${d}_d`]||'1'} onChange={e=>set(`${d}_d`,e.target.value)}/></label></div>)}
 {metered?<><label>计费步长（可选）<input inputMode="decimal" value={values.step||''} onChange={e=>set('step',e.target.value)}/></label><label>最低计费量（可选）<input inputMode="decimal" value={values.minimum||''} onChange={e=>set('minimum',e.target.value)}/></label></>:null}
 <label>操作原因<textarea required minLength={2} maxLength={500} value={values.reason||''} onChange={e=>set('reason',e.target.value)}/></label>
 {error?<Notice kind="error">{error}</Notice>:null}<button type="submit" disabled={busy}>{busy?'提交中…':'确认提交'}</button>
 </form></Modal>:null}</>;
}
