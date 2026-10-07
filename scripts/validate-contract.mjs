import fs from 'node:fs';
const contract=JSON.parse(fs.readFileSync(new URL('../contracts/openapi.yaml',import.meta.url),'utf8'));
const schemas=contract.components.schemas;
const fail=message=>{throw new Error(message);};
if(contract.openapi!=='3.0.3')fail('Expected the maintained OpenAPI 3.0.3 contract.');
if(!schemas.OrderEventInput.properties.action.enum.includes('reopened'))fail('Missing explicit order reopening.');
if(!schemas.CashbackPolicyInput.properties.taxPercent)fail('Missing internal tax configuration.');
if(!contract.paths['/affiliate-links'].post.parameters.some(p=>p.name==='Idempotency-Key'&&p.required))fail('Link creation must require an operation key.');
if(!schemas.Meta.properties.hasNext || !schemas.Meta.properties.nextCursor)fail('Pagination metadata is missing.');
function inspect(schema,seen=new Set()) {
 if(!schema || typeof schema!=='object')return;
 if(schema.$ref) {if(seen.has(schema.$ref))return;seen.add(schema.$ref);inspect(schema.$ref.split('/').slice(1).reduce((value,key)=>value?.[key],contract),seen);}
 for(const [name,value] of Object.entries(schema.properties||{})) {if(/tax|deduct/i.test(name))fail('Internal tax appeared in customer response: '+name);inspect(value,seen);}
 for(const name of ['allOf','oneOf','anyOf'])for(const child of schema[name]||[])inspect(child,seen);
 inspect(schema.items,seen);
}
for(const path of ['/config','/me/dashboard','/product-checks','/affiliate-links']) {
 for(const operation of Object.values(contract.paths[path]))for(const [status,response] of Object.entries(operation.responses||{}))if(status.startsWith('2'))inspect(response.content?.['application/json']?.schema);
}
console.log('Contract validated: operation keys, reopening, pagination and private tax.');
