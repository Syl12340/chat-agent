import {palette, plotFrame, svgNode} from './charts.js';
let markerSequence = 0;
export function renderVectors(definition) {
  const p = definition.props;
  const node = document.createElement('div'); node.className = 'chat-ui-vectors';
  const frame = plotFrame(p.vectors.flatMap(v => [v.from, v.to]), p, {includeOrigin: true, equalScale: true});
  const defs = svgNode('defs'); const seq = ++markerSequence;
  palette.forEach((color, i) => {
    const marker = svgNode('marker', {id: `chat-ui-arrow-${seq}-${i}`, viewBox: '0 0 10 10', refX: 9, refY: 5, markerWidth: 7, markerHeight: 7, orient: 'auto-start-reverse'});
    marker.append(svgNode('path', {d: 'M 0 0 L 10 5 L 0 10 z', fill: color})); defs.append(marker);
  });
  frame.svg.prepend(defs);
  const legend = document.createElement('ul'); legend.className = 'chat-ui-legend';
  p.vectors.forEach((v, i) => {
    const zero = v.from.x === v.to.x && v.from.y === v.to.y;
    if (zero) frame.svg.append(svgNode('circle', {cx: frame.x(v.from.x), cy: frame.y(v.from.y), r: 4, fill: palette[i % 3]}));
    else frame.svg.append(svgNode('line', {x1: frame.x(v.from.x), y1: frame.y(v.from.y), x2: frame.x(v.to.x), y2: frame.y(v.to.y), stroke: palette[i % 3], 'stroke-width': 2.5, 'marker-end': `url(#chat-ui-arrow-${seq}-${i % 3})`}));
    const item = document.createElement('li'); item.dataset.color = i % 3;
    item.textContent = `${v.label || v.id}：(${v.from.x}, ${v.from.y}) → (${v.to.x}, ${v.to.y})${zero ? ' · 零向量' : ''}`;
    legend.append(item);
  });
  node.append(frame.svg, legend); return {node, update() {}, dispose() {}};
}
