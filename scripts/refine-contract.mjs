import fs from 'node:fs';
const path = new URL('../contracts/openapi.yaml', import.meta.url);
const doc = JSON.parse(fs.readFileSync(path, 'utf8'));
const str = (minLength=0,maxLength=500)=>({type:'string',minLength,maxLength});
const num = (minimum=0,maximum=1e12)=>({type:'integer',format:'int64',minimum,maximum});
const bool = {type:'boolean'};
const uuid = {type:'string',format:'uuid'};
const channel = {type:'string',enum:['shopee','lazada','tiktok','tiki']};
const choice = (...values)=>({type:'string',enum:values});
const arr = items=>({type:'array',items});
const obj = (properties,required=Object.keys(properties))=>({type:'object',additionalProperties:false,properties,required});
const schemas = doc.components.schemas;
doc.paths['/admin/browser/cookies']={put:{security:[{session:[]}],responses:{'200':{description:'Cookie applied to the shared Chromium session. Returns status only, never cookie values.'},'422':{description:'Invalid cookie input'},'503':{description:'Browser unavailable'}}}};
doc.paths['/admin/browser/session-checks']={post:{security:[{session:[]}],responses:{'201':{description:'Probes the existing browser session without reloading pasted cookies.'},'503':{description:'Browser unavailable'}}}};
const define = (name,value)=>(schemas[name]=value,{$ref:'#/components/schemas/'+name});
const tierCode=choice('bronze','platinum','diamond');
const sharePercent={type:'number',minimum:0,maximum:100,multipleOf:0.01};
const cashbackTier=define('CashbackTier',obj({tierCode,minApprovedOrders:num(0,1000000000),minSharePercent:sharePercent,maxSharePercent:sharePercent}));
const cashbackPolicy=define('CashbackPolicy',obj({id:uuid,createdAt:{type:'string',format:'date-time'},tiers:{...arr(cashbackTier),minItems:3,maxItems:3}}));
const cashbackInput=define('CashbackPolicyInput',obj({currentVersionId:uuid,tiers:{...arr(cashbackTier),minItems:3,maxItems:3}}));
const policyResponse={description:'Cashback policy version',content:{'application/json':{schema:obj({data:cashbackPolicy,meta:{$ref:'#/components/schemas/Meta'}})}}};
doc.paths['/admin/cashback-policies/current']={get:{security:[{session:[]}],responses:{'200':policyResponse,'401':{description:'Session required'},'403':{description:'Settings permission required'}}}};
doc.paths['/admin/cashback-policies']={post:{security:[{session:[]}],description:'Creates an immutable version. Requires settings permission, recent password authentication and Idempotency-Key. currentVersionId protects against stale edits.',responses:{'201':policyResponse,'401':{description:'Session required'},'403':{description:'Permission, CSRF or recent authentication required'},'409':{description:'Stale policy version or idempotency conflict'},'422':{description:'Invalid tiers or percentage precision'}}}};
const reason = str(3,500), password = str(12,128);
const url = {type:'string',format:'uri',maxLength:2048,pattern:'^https://'};
const permissions=arr(choice('orders','withdrawals','users','gifts','community','notifications','settings','audit'));
const bankDetails=define('BankDetails',obj({bank:str(2,80),account:{...str(6,20),pattern:'^[0-9]{6,20}$'},holder:str(2,80)}));
schemas.User.properties.bankDetails={allOf:[bankDetails],nullable:true,description:'Private encrypted payout profile; visible only to the signed-in owner. Null until configured.'};
schemas.User.required=[...new Set([...schemas.User.required,'bankDetails'])];
const requests = {
 'post /admin/cashback-policies':cashbackInput,
 'put /admin/browser/cookies':define('ShopeeCookieInput',obj({cookie:str(1,65536)})),
 'post /auth/internal/login':define('InternalLogin',obj({username:str(3,40),password:str(1,128)})),
 'post /auth/internal/reauth':define('Reauthentication',obj({password:str(1,128)})),
 'patch /me':define('ProfileUpdate',{...obj({name:str(1,80),bankDetails:{oneOf:[bankDetails,obj({bank:{type:'string',enum:['']},account:{type:'string',enum:['']},holder:{type:'string',enum:['']}})],description:'Provide all three valid fields, or three empty strings to clear the saved payout profile.'}},[]),minProperties:1}),
 'put /me/password':define('PasswordChange',obj({oldPassword:str(1,128),password})),
 'post /product-checks':define('ProductCheckInput',obj({url})),
 'post /shopee/check':{$ref:'#/components/schemas/ProductCheckInput'},
 'post /affiliate-links':{$ref:'#/components/schemas/ProductCheckInput'},
 'patch /affiliate-links/{id}':define('LinkUpdate',obj({saved:bool})),
 'post /withdrawals':define('WithdrawalInput',obj({amount:{...num(50000),multipleOf:1000},bank:str(2,80),account:{...str(6,20),pattern:'^[0-9]{6,20}$'},holder:str(2,80)})),
 'post /coin-exchanges':define('CoinExchangeInput',obj({coins:{...num(10,1000000),multipleOf:10}})),
 'post /gift-redemptions':define('GiftRedemptionInput',obj({giftId:str(1,80)})),
 'post /deals':define('DealInput',obj({channel,body:str(5,1000)})),
 'post /admin/internal-accounts':define('InternalAccountInput',obj({username:str(3,40),name:str(1,80),password,role:choice('staff','admin'),permissions},['username','name','password','role'])),
 'post /admin/internal-accounts/{id}/reset':define('InternalResetInput',obj({password,permissions,blocked:bool})),
 'patch /admin/users/{id}':define('CustomerLockInput',obj({blocked:bool,reason})),
 'post /admin/orders':define('ManualOrderInput',obj({trackingCode:str(1,100),channel,publisher:str(1,100),externalId:str(1,100),lineId:str(1,100),productName:str(1,200),value:num(),commission:num(),evidence:reason})),
 'post /admin/orders/{id}/events':define('OrderEventInput',obj({action:choice('approved','rejected','adjustment'),reason:str(0,500),commission:num()},['action'])),
 'post /admin/withdrawals/{id}/events':define('WithdrawalEventInput',obj({action:choice('processing','paid','rejected'),reason:str(0,500),bankReference:str(0,100),evidenceId:uuid},['action'])),
 'patch /admin/gifts/{id}':define('GiftUpdate',obj({name:str(1,80),cost:num(1,1000000),stock:num(0,100000),active:bool})),
 'post /admin/gift-redemptions/{id}/events':define('GiftEventInput',obj({action:choice('completed','rejected'),code:str(0,500),reason:str(0,500)},['action'])),
 'post /admin/deals/{id}/events':define('ModerationInput',obj({action:choice('hide','show','delete'),reason})),
 'post /admin/notifications':define('NotificationInput',obj({recipientId:{...str(0,36),description:'Empty string broadcasts to customers.'},title:str(1,80),body:str(1,1000)},['title','body'])),
 'put /admin/settings':define('SettingsInput',obj({brand:str(1,30),supportEmail:str(0,254),coinExchangeEnabled:bool,maxDisplayPercent:{type:'number',nullable:true,minimum:0,maximum:100},faq:{type:'array',maxItems:20,items:obj({question:str(1,150),answer:str(1,1000)})}},['brand','coinExchangeEnabled'])),
 'patch /admin/affiliate-channels/{id}':define('ChannelUpdate',obj({status:choice('not_configured','available','temporarily_unavailable'),template:str(0,2048)},['status'])),
 'post /admin/order-imports/{id}/rows/{number}/resolve':define('AttributionInput',obj({trackingCode:str(1,100),reason})),
};
const time={type:'string',format:'date-time'};
const browserStatus=define('BrowserStatus',obj({authenticated:bool,browser:bool,savedCookies:bool,state:choice('not_started','checking','authenticated','login_required','verification_required','unavailable','cookie_storage_error'),pending:num(0,50),workers:num(2,2),starts:num(),lastVerifiedAt:{...time,nullable:true}}));
const browserEnvelope=obj({data:browserStatus,meta:{$ref:'#/components/schemas/Meta'}});
doc.paths['/admin/browser'].get.responses['200']={description:'Managed Chromium status; remoteAvailable is true only for administrators when remote display is enabled.',content:{'application/json':{schema:obj({data:obj({enabled:bool,trackingVerified:bool,remoteAvailable:bool,browser:browserStatus},['enabled','trackingVerified','remoteAvailable']),meta:{$ref:'#/components/schemas/Meta'}})}}};
const remoteAccess=define('RemoteBrowserAccess',obj({url:{type:'string',format:'uri',description:'Open directly at the backend origin. One-use access ticket is in the fragment; expires after 60 seconds.'},expiresAt:time}));
doc.paths['/admin/browser/access']={post:{security:[{session:[]}],description:'Administrator only; requires CSRF and recent password authentication. Starts headed Chromium and issues a one-use ticket for a browser display session of at most 10 minutes. Underlying account/session revocation also revokes display access.',responses:{'201':{description:'Remote browser access ticket',content:{'application/json':{schema:obj({data:remoteAccess,meta:{$ref:'#/components/schemas/Meta'}})}}},'401':{description:'Session required'},'403':{description:'Administrator, CSRF and recent password authentication required'},'429':{description:'At most six access requests per minute'},'503':{description:'Remote display or headed Chromium unavailable'}}}};
doc.paths['/admin/browser/cookies'].put.responses['200'].content={'application/json':{schema:browserEnvelope}};
doc.paths['/admin/browser/session-checks'].post.responses['201'].content={'application/json':{schema:browserEnvelope}};
for(const p of ['/admin/browser/cookies','/admin/browser/session-checks']){
 const op=doc.paths[p][p.endsWith('/cookies')?'put':'post'];
 op.description='Requires admin or staff settings permission. Returns metadata only. The managed Chromium process and current cookies are reused while it remains alive.';
 for(const code of ['401','403','429'])op.responses[code]={description:code==='401'?'Session required':code==='403'?'Settings permission and CSRF validation required':'Per-user request limit reached'};
}
const status=choice('pending','approved','rejected');
define('Order',obj({id:uuid,userId:uuid,name:str(),channel,productName:str(),value:num(),commission:num(),cashback:num(),status,sourceStatus:status,orderedAt:time,externalId:str(),lineId:str(),publisher:str()}));
define('Wallet',obj({available:num(),held:num(),debt:num()}));
define('AffiliateChannel',obj({id:channel,name:str(),status:choice('demo','not_configured','available','temporarily_unavailable')}));
define('AffiliateLink',obj({id:uuid,channel,originalUrl:url,affiliateUrl:url,trackingCode:str(),saved:bool,createdAt:time}));
define('Deal',obj({id:uuid,body:str(),channel,name:str(),createdAt:time,likes:num()}));
define('Gift',obj({id:str(),name:str(),channel,cost:num(),stock:num(),active:bool}));
define('Notification',obj({id:uuid,title:str(),body:str(),createdAt:time,read:bool}));
define('Transaction',obj({id:uuid,description:str(),amount:{type:'integer',format:'int64',nullable:true},createdAt:time}));
define('Withdrawal',obj({id:uuid,userId:uuid,name:str(),bank:str(),account:str(),holder:str(),amount:num(),status:choice('pending','processing','paid','rejected'),createdAt:time,processorId:{...uuid,nullable:true},bankReference:{...str(),nullable:true},evidenceId:{...uuid,nullable:true},reason:{...str(),nullable:true}}));
define('GiftRedemption',obj({id:uuid,userId:uuid,name:str(),giftName:str(),giftId:str(),cost:num(),status:choice('pending','completed','rejected'),code:{...str(),nullable:true},reason:{...str(),nullable:true},createdAt:time}));
Object.assign(schemas.Order.properties,{policyId:uuid,tierCode:{...tierCode,nullable:true},sharePercent});
schemas.Order.required=[...new Set([...schemas.Order.required,'policyId','tierCode','sharePercent'])];
Object.assign(schemas.AffiliateLink.properties,{policyId:uuid,tierCode:{...tierCode,nullable:true},minSharePercent:sharePercent,maxSharePercent:sharePercent});
schemas.AffiliateLink.required=[...new Set([...schemas.AffiliateLink.required,'policyId','tierCode','minSharePercent','maxSharePercent'])];
const createdLink=define('AffiliateLinkCreated',obj({id:uuid,channel,affiliateUrl:url,trackingCode:str(),policyId:uuid,tierCode,minSharePercent:sharePercent,maxSharePercent:sharePercent}));
doc.paths['/affiliate-links'].post.responses['201']={description:'Created link with immutable membership and share range snapshot',content:{'application/json':{schema:obj({data:createdLink,meta:{$ref:'#/components/schemas/Meta'}})}}};
delete doc.paths['/affiliate-links'].post.responses['200'];
const manualOrder=define('ManualOrderCreated',obj({id:uuid,tierCode:{...tierCode,nullable:true},sharePercent,cashback:num()}));
doc.paths['/admin/orders'].post.responses['201']={description:'Recorded order with its persisted sampled rate; no wallet credit until approval',content:{'application/json':{schema:obj({data:manualOrder,meta:{$ref:'#/components/schemas/Meta'}})}}};
delete doc.paths['/admin/orders'].post.responses['200'];
const membership=define('Membership',obj({policyId:uuid,tierCode,minApprovedOrders:num(),minSharePercent:sharePercent,maxSharePercent:sharePercent,approvedOrders:num(),nextTier:{allOf:[cashbackTier],nullable:true},ordersToNext:num()}));
const dashboard=define('Dashboard',obj({pending:num(),approved:num(),approvedOrders:num(),available:num(),held:num(),debt:num(),coins:num(),membership}));
define('Meta',obj({requestId:str(),hasNext:bool,nextCursor:{...str(0,512),nullable:true}},['requestId']));
schemas.Error.properties.error.properties.details={type:'array',items:{type:'object',additionalProperties:true}};
const reads={
 '/me/dashboard':dashboard,
 '/me':{$ref:'#/components/schemas/User'},'/wallet':{$ref:'#/components/schemas/Wallet'},'/orders':arr({$ref:'#/components/schemas/Order'}),'/orders/{id}':{$ref:'#/components/schemas/Order'},'/admin/orders':arr({$ref:'#/components/schemas/Order'}),
 '/affiliate-channels':arr({$ref:'#/components/schemas/AffiliateChannel'}),'/affiliate-links':arr({$ref:'#/components/schemas/AffiliateLink'}),'/deals':arr({$ref:'#/components/schemas/Deal'}),'/gifts':arr({$ref:'#/components/schemas/Gift'}),'/admin/gifts':arr({$ref:'#/components/schemas/Gift'}),'/notifications':arr({$ref:'#/components/schemas/Notification'}),
 '/wallet/transactions':arr({$ref:'#/components/schemas/Transaction'}),'/coins/transactions':arr({$ref:'#/components/schemas/Transaction'}),'/withdrawals':arr({$ref:'#/components/schemas/Withdrawal'}),'/admin/withdrawals':arr({$ref:'#/components/schemas/Withdrawal'}),'/gift-redemptions':arr({$ref:'#/components/schemas/GiftRedemption'}),
};
const cursorPaths=new Set(['/orders','/affiliate-links','/coins/transactions','/wallet/transactions','/notifications','/deals','/withdrawals','/gift-redemptions']);
const lists=new Set([...cursorPaths,...Object.keys(doc.paths).filter(p=>p.startsWith('/admin/')&&['/orders','/users','/internal-accounts','/withdrawals','/gift-redemptions','/deals','/notifications','/audit-logs','/order-imports'].some(s=>p==='/admin'+s)),'/admin/order-imports/{id}/rows']);
const idempotent=new Set(['post /withdrawals','post /coin-exchanges','post /gift-redemptions','post /admin/orders','post /admin/orders/{id}/events','post /admin/withdrawals/{id}/events','post /admin/gift-redemptions/{id}/events','post /admin/order-imports/{id}/commit']);
idempotent.add('post /admin/cashback-policies');
for(const [p,methods] of Object.entries(doc.paths)) for(const [m,op] of Object.entries(methods)){
 const key=m+' '+p;
 op.summary=m.toUpperCase()+' '+p;
 if(!op.parameters?.some(v=>v.name==='Accept-Language'))(op.parameters||=[]).push({name:'Accept-Language',in:'header',required:false,schema:{type:'string',enum:['vi','en']},description:'Error message language. Defaults to Vietnamese; error codes stay unchanged.'});
 op.parameters=(op.parameters||[]).filter(param=>param.name!=='X-CSRF-Token'&&(param.in!=='query'||(lists.has(p)&&['page','perPage','cursor','status','saved','q'].includes(param.name))||(['/leaderboards','/me/leaderboard'].includes(p)&&param.name==='period')));
 if(cursorPaths.has(p)&&m==='get'&&!op.parameters.some(v=>v.name==='cursor'))op.parameters.push({name:'cursor',in:'query',schema:str(0,512),description:'Opaque nextCursor from the previous response; takes precedence over page.'});
 for(const param of op.parameters){if(param.in==='path'&&param.name==='id'&&!p.includes('/admin/gifts/')&&!p.includes('/affiliate-channels/'))param.schema=uuid;}
 if(m!=='get'){
  if(op.security?.length)op.parameters.push({name:'X-CSRF-Token',in:'header',required:true,schema:str(),description:'Token from GET /me; Origin must match the configured frontend.'});
  if(idempotent.has(key)&&!op.parameters.some(v=>v.name==='Idempotency-Key'))op.parameters.push({name:'Idempotency-Key',in:'header',required:true,schema:str(8,128),description:'Reuse on retry; a different payload under the same key returns 409.'});
  if(requests[key])op.requestBody={required:true,content:{'application/json':{schema:requests[key]}}};
  else if(op.requestBody?.content?.['application/json'])delete op.requestBody;
 }
 if(m==='get'&&reads[p])op.responses['200']={description:'Success',content:{'application/json':{schema:obj({data:reads[p],meta:{$ref:'#/components/schemas/Meta'}})}}};
 if(m==='post'&&!['/auth/internal/login','/auth/internal/reauth','/auth/logout','/product-checks','/shopee/check','/notification-read-batches','/admin/internal-accounts/{id}/reset'].includes(p)){
  const code=p.endsWith('/commit')||p.endsWith('/retry')?'202':'201';
  op.responses[code]=op.responses['200']||op.responses[code]||{description:'Success'};delete op.responses['200'];
 }
 for(const [code,response] of Object.entries(op.responses)){if(Number(code)>=400)response.content={'application/json':{schema:{$ref:'#/components/schemas/Error'}}};}
 if(p.startsWith('/auth/google'))op.responses={'302':{description:'Redirects to Google or frontend. A failed callback redirects to /login?error=google.'},'503':{description:'Google OAuth is not configured.',content:{'application/json':{schema:{$ref:'#/components/schemas/Error'}}}}};
 if(p==='/auth/google/callback')op.parameters=['state','code','error'].map(name=>({name,in:'query',schema:str(),required:false}));
 if(p==='/private-files/{id}')op.responses['200']={description:'Private file, delivered as attachment after checking ownership or permission.',content:{'application/octet-stream':{schema:{type:'string',format:'binary'}}}};
 if(p==='/admin/private-files'&&m==='post')delete op.requestBody.content['multipart/form-data'].schema.properties.mapping;
}
for(const [p,names] of Object.entries({'/orders':['status'],'/admin/orders':['status'],'/affiliate-links':['saved'],'/admin/users':['q']}))for(const name of names){const op=doc.paths[p].get;if(!op.parameters.some(v=>v.name===name))op.parameters.push({name,in:'query',schema:name==='saved'?bool:str()});}
doc.info.description='Windows local PostgreSQL API. Google-only customer identities; provisioned internal accounts. No public registration. Financial commands are transactional and idempotent. Money amounts are integer VND, dates UTC except check-ins in Asia/Ho_Chi_Minh. Recent password authentication is required for money, account resets and policy changes. Unsupported affiliate integrations remain demo/unavailable.';
fs.writeFileSync(path,JSON.stringify(doc,null,2)+'\n');
