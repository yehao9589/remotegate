// Validate the delivered inline script, including all modules in the server's order.
const fs = require('node:fs');
const vm = require('node:vm');
const main = fs.readFileSync('cmd/server/main.go','utf8');
const root = main.slice(main.indexOf('func (a *app) root('));
const files = [...root.matchAll(/webFS\.ReadFile\("web\/([^"\n]+\.js)"\)/g)].map(match=>match[1]);
// Clipboard is prepended rather than appended in root().
const clipboard = files.splice(files.indexOf('clipboard.js'),1)[0];
if(!clipboard || files.length<6) throw Error('Unexpected console embedding order');
files.unshift(clipboard);
let html = fs.readFileSync('cmd/server/web/index.html','utf8');
if(!html.includes('/* INSTALL_GUIDE_JS */')) throw Error('Console script marker missing');
html = html.replace('/* INSTALL_GUIDE_JS */',()=>files.map(file=>fs.readFileSync('cmd/server/web/'+file,'utf8')).join('\n'));
let checked=0;
for(const script of html.matchAll(/<script([^>]*)>([\s\S]*?)<\/script>/g)){
 if(script[1].includes('application/json')) continue;
 new vm.Script(script[2],{filename:'embedded-console.js'});checked++;
}
if(!checked) throw Error('Console inline script missing');
console.log('Embedded console JavaScript passed ('+files.length+' modules).');
