// Public certificate presentation has no DNS credentials or issuance controls.
const providerInfo={manual:{name:'其他平台 / 手动管理'},alidns:{name:'阿里云 DNS'},dnspod:{name:'腾讯云 DNSPod'},cloudflare:{name:'Cloudflare'}};
function certDate(value){const time=Date.parse(value);return Number.isFinite(time)&&time>0?new Date(time).toLocaleString():'—'}
