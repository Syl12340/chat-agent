// Read-only data displays. Only host-owned DOM/SVG and bounded numeric layout.
import {palette, svgNode} from './charts.js';
const element = (tag, cls, text) => {const n=document.createElement(tag);if(cls)n.className=cls;if(text!==undefined)n.textContent=text;return n;};
const done = node => ({node, update() {}, dispose() {node.querySelectorAll('button').forEach(b=>{b.disabled=true;});}});
const fmt = n => Math.abs(n)>=10000 ? n.toExponential(1) : Number(n.toFixed(2)).toString();
const short = (text, count) => [...text].length>count ? [...text].slice(0,count-1).join('')+'…' : text;

export function renderTable(definition) {
  const p=definition.props, node=element('div','chat-ui-data-table');
  const scroll=element('div','chat-ui-table-scroll');scroll.tabIndex=0;scroll.setAttribute('role','region');scroll.setAttribute('aria-label',p.title || '数据表');
  const table=element('table');const head=element('thead'), row=element('tr');
  for(const column of p.columns){const th=element('th','',column.label);th.scope='col';row.append(th);}head.append(row);
  const body=element('tbody');for(const values of p.rows){const tr=element('tr');for(const value of values)tr.append(element('td',typeof value==='number'?'chat-ui-numeric':'',String(value)));body.append(tr);}
  table.append(head,body);scroll.append(table);
  const copy=element('button','chat-ui-reset','复制表格');copy.type='button';
  copy.addEventListener('click',async()=>{
    const quote=value=>String(value).replaceAll('\t',' ').replaceAll('\r',' ').replaceAll('\n',' ');
    const source=[p.columns.map(c=>c.label),...p.rows].map(r=>r.map(quote).join('\t')).join('\n');
    try{await navigator.clipboard.writeText(source);copy.textContent='已复制';}catch{copy.textContent='请选中表格复制';}
  });
  node.append(element('p','chat-ui-data-label','模型提供的数据'),scroll,copy);return done(node);
}

export function renderBars(definition) {
  const p=definition.props, node=element('div','chat-ui-bars');
  node.append(element('p','chat-ui-data-label','模型提供的数据'));
  const wrap=element('div','chat-ui-structure-scroll');wrap.tabIndex=0;wrap.setAttribute('aria-label',p.title || '柱状图');
  const width=Math.max(600,p.categories.length*66+100), height=380, left=76,right=24,top=36,bottom=96;
  const values=p.series.flatMap(s=>s.values);let low=Math.min(0,...values),high=Math.max(0,...values);
  if(high-low<.1){low-=.05;high+=.05;}const span=high-low;low-=span*.05;high+=span*.05;
  const y=value=>height-bottom-(value-low)/(high-low)*(height-bottom-top);
  const svg=svgNode('svg',{viewBox:`0 0 ${width} ${height}`,width,height,role:'img','aria-label':p.title || '柱状图',class:'chat-ui-structure-plot'});
  for(let i=0;i<=4;i++){const value=low+(high-low)*i/4;svg.append(svgNode('line',{x1:left,x2:width-right,y1:y(value),y2:y(value),class:'chat-ui-grid'}));svg.append(svgNode('text',{x:left-9,y:y(value)+5,'text-anchor':'end',class:'chat-ui-tick'},fmt(value)));}
  svg.append(svgNode('line',{x1:left,x2:width-right,y1:y(0),y2:y(0),class:'chat-ui-axis'}));
  const group=(width-left-right)/p.categories.length, bar=Math.min(40,group*.76/p.series.length);
  p.categories.forEach((label,i)=>{
    const center=left+group*(i+.5);
    p.series.forEach((series,j)=>{const value=series.values[i],r=svgNode('rect',{x:center+(j-(p.series.length-1)/2)*bar-bar*.44,y:Math.min(y(value),y(0)),width:bar*.88,height:Math.max(1,Math.abs(y(value)-y(0))),fill:palette[j]});r.append(svgNode('title',{},`${label} · ${series.name || `系列 ${j+1}`}：${value}`));svg.append(r);});
    const text=svgNode('text',{x:center,y:height-bottom+26,'text-anchor':'middle',class:'chat-ui-tick'},short(label,8));text.append(svgNode('title',{},label));svg.append(text);
  });
  if(p.x_label)svg.append(svgNode('text',{x:(width+left-right)/2,y:height-16,'text-anchor':'middle',class:'chat-ui-axis-label'},p.x_label));
  if(p.y_label)svg.append(svgNode('text',{x:left,y:22,class:'chat-ui-axis-label'},p.y_label));
  wrap.append(svg);node.append(wrap);
  const legend=element('ul','chat-ui-legend');p.series.forEach((s,i)=>{const li=element('li','',s.name || `系列 ${i+1}`);li.dataset.color=i;legend.append(li);});node.append(legend);
  const exact=element('details','chat-ui-structure-values');exact.append(element('summary','','查看完整数据'));
  const list=element('ul');p.categories.forEach((label,i)=>list.append(element('li','',`${label}：${p.series.map((s,j)=>`${s.name || `系列 ${j+1}`} ${s.values[i]}`).join('；')}`)));exact.append(list);node.append(exact);return done(node);
}

let flowSequence=0;
export function renderFlow(definition) {
  const p=definition.props,node=element('div','chat-ui-flow'),wrap=element('div','chat-ui-structure-scroll');wrap.tabIndex=0;wrap.setAttribute('aria-label',p.title || '流程与关系');
  const columns=Math.min(3,p.nodes.length), rows=Math.ceil(p.nodes.length/columns),width=columns*240+48,height=rows*150+90;
  const svg=svgNode('svg',{viewBox:`0 0 ${width} ${height}`,width,height,role:'img','aria-label':p.title || '流程与关系',class:'chat-ui-structure-plot chat-ui-flow-plot'});
  const markerID=`chat-ui-flow-arrow-${++flowSequence}`,defs=svgNode('defs'),marker=svgNode('marker',{id:markerID,viewBox:'0 0 10 10',refX:9,refY:5,markerWidth:7,markerHeight:7,orient:'auto'});marker.append(svgNode('path',{d:'M 0 0 L 10 5 L 0 10 z',class:'chat-ui-flow-arrow'}));defs.append(marker);svg.append(defs);
  // A stable finite grid also supports cycles, self-edges and disconnected nodes.
  const positions=new Map(p.nodes.map((n,i)=>[n.id,{x:24+(i%columns)*240,y:35+Math.floor(i/columns)*150}]));
  p.edges.forEach((edge,i)=>{
    const a=positions.get(edge.from),b=positions.get(edge.to);let d,x,y;
    if(edge.from===edge.to){d=`M ${a.x+150} ${a.y} C ${a.x+210} ${a.y-36},${a.x-12} ${a.y-36},${a.x+54} ${a.y}`;x=a.x+100;y=a.y-18;}
    else{const ax=a.x+100,ay=a.y+66,bx=b.x+100,by=b.y;const bend=Math.max(36,Math.abs(by-ay)*.35)+(i%3)*9;d=`M ${ax} ${ay} C ${ax} ${ay+bend},${bx} ${by-bend},${bx} ${by}`;x=(ax+bx)/2;y=(ay+by)/2-6;}
    const path=svgNode('path',{d,fill:'none',class:'chat-ui-flow-edge','marker-end':`url(#${markerID})`});path.append(svgNode('title',{},`${edge.from} → ${edge.to}${edge.label?`：${edge.label}`:''}`));svg.append(path);
    if(edge.label)svg.append(svgNode('text',{x,y,'text-anchor':'middle',class:'chat-ui-flow-edge-label'},short(edge.label,12)));
  });
  p.nodes.forEach(n=>{
    const {x,y}=positions.get(n.id);svg.append(svgNode('rect',{x,y,width:200,height:66,rx:12,class:'chat-ui-flow-node'}));
    const text=svgNode('text',{x:x+100,y:y+25,'text-anchor':'middle',class:'chat-ui-flow-label'});text.append(svgNode('title',{},n.label));
    const chars=[...n.label];for(let j=0;j<2;j++)text.append(svgNode('tspan',{x:x+100,dy:j?23:0},short(chars.slice(j*12).join(''),12)));svg.append(text);
  });
  wrap.append(svg);node.append(wrap);
  const list=element('details','chat-ui-structure-values');list.append(element('summary','','查看节点与连线'));
  const nodes=element('ul');p.nodes.forEach(n=>nodes.append(element('li','',`${n.id}：${n.label}`)));list.append(nodes);
  if(p.edges.length){const edges=element('ul');p.edges.forEach(e=>edges.append(element('li','',`${e.from} → ${e.to}${e.label?`：${e.label}`:''}`)));list.append(edges);}node.append(list);return done(node);
}
