export type Work={id:string;code:string;title:string;year?:number;actors:string[];actor_thumbs?:Record<string,string>;tags:string[];rating:number|null;source_rating?:number|null;cover_url?:string;thumbnail_url?:string;source_url?:string;kind?:string;collection_status?:string;files?:FileItem[];progress?:Progress}
export type FileItem={id:string;device_id:string;library_id:string;relative_path:string;size:number;modified_at:string;version?:string;missing:boolean;progress:Progress}
export type Progress={compression:string;cutting:string;translation:string;verified:boolean}
export type Actor={name:string;count:number;policy:string;target:number;avatar_url?:string;favorite?:boolean}
export type Device={id:string;name:string;online:boolean;last_seen:string}
export type Library={id:string;device_id:string;name:string;path:string;openlist_base?:string;openlist_prefix?:string}
export type Job={id:string;device_id:string;type:string;payload:unknown;status:string;result?:string;created_at:string}
export type Candidate={id:string;code:string;title:string;url:string;source:string;owned:boolean;in_queue:boolean;status:string;metadata_only?:boolean;source_url?:string}
const TOKEN_KEY='mediashelf_token'
export function currentToken(){return sessionStorage.getItem(TOKEN_KEY)||localStorage.getItem(TOKEN_KEY)||''}
export function clearToken(){for(const storage of [localStorage,sessionStorage]){storage.removeItem(TOKEN_KEY);storage.removeItem('mediashelf_token_expires_at')}}
export function rememberToken(token:string,remember:boolean,expiresAt:string){clearToken();const storage=remember?localStorage:sessionStorage;storage.setItem(TOKEN_KEY,token);storage.setItem('mediashelf_token_expires_at',expiresAt);localStorage.setItem('mediashelf_remember_login',String(remember))}
const API='/api/v1'
export class ApiError extends Error{constructor(public status:number,message:string){super(message)}}
export async function request<T>(path:string,init:RequestInit={}):Promise<T>{const headers=new Headers(init.headers);const token=currentToken();if(token&&!headers.has('Authorization'))headers.set('Authorization',`Bearer ${token}`);if(init.body&&!(init.body instanceof FormData)&&!(init.body instanceof Blob))headers.set('Content-Type','application/json');const res=await fetch(API+path,{...init,headers});const text=await res.text();let data:any=null;try{data=text?JSON.parse(text):null}catch{data=text}if(!res.ok)throw new ApiError(res.status,data?.error||`请求失败 (${res.status})`);return data as T}
export const json=(method:string,body?:unknown):RequestInit=>({method,...(body===undefined?{}:{body:JSON.stringify(body)})})
