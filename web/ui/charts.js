export const palette = ['#3b82f6', '#c45c20', '#2e916b'];
export function svgNode(tag, attrs = {}, text) {
  const node = document.createElementNS('http://www.w3.org/2000/svg', tag);
  for (const [key, value] of Object.entries(attrs)) node.setAttribute(key, String(value));
  if (text !== undefined) node.textContent = text;
  return node;
}
const fmt = n => Math.abs(n) >= 10000 || (n !== 0 && Math.abs(n) < .001) ? n.toExponential(1) : Number(n.toFixed(2)).toString();
export function plotFrame(points, props, {includeOrigin = false, equalScale = false} = {}) {
  const xs = points.map(p => p.x), ys = points.map(p => p.y);
  if (includeOrigin) { xs.push(0); ys.push(0); }
  let minX = Math.min(...xs), maxX = Math.max(...xs), minY = Math.min(...ys), maxY = Math.max(...ys);
  const dx = Math.max(maxX - minX, .1), dy = Math.max(maxY - minY, .1);
  if (maxX - minX < .1) { const mid = (minX + maxX) / 2; minX = mid - .05; maxX = mid + .05; }
  if (maxY - minY < .1) { const mid = (minY + maxY) / 2; minY = mid - .05; maxY = mid + .05; }
  minX -= dx * .06; maxX += dx * .06; minY -= dy * .1; maxY += dy * .1;
  if (equalScale) {
    const unit = Math.max((maxX - minX) / 504, (maxY - minY) / 242);
    const midX = (minX + maxX) / 2, midY = (minY + maxY) / 2;
    minX = midX - unit * 252; maxX = midX + unit * 252; minY = midY - unit * 121; maxY = midY + unit * 121;
  }
  const x = v => 64 + (v - minX) / (maxX - minX) * 504;
  const y = v => 270 - (v - minY) / (maxY - minY) * 242;
  const svg = svgNode('svg', {viewBox: '0 0 600 320', role: 'img', 'aria-label': props.title || '二维坐标图', class: 'chat-ui-plot'});
  for (let i = 0; i <= 4; i++) {
    const xv = minX + (maxX - minX) * i / 4, yv = minY + (maxY - minY) * i / 4;
    svg.append(svgNode('line', {x1: x(xv), y1: 28, x2: x(xv), y2: 270, class: 'chat-ui-grid'}));
    svg.append(svgNode('line', {x1: 64, y1: y(yv), x2: 568, y2: y(yv), class: 'chat-ui-grid'}));
    svg.append(svgNode('text', {x: x(xv), y: 289, 'text-anchor': 'middle', class: 'chat-ui-tick'}, fmt(xv)));
    svg.append(svgNode('text', {x: 57, y: y(yv) + 4, 'text-anchor': 'end', class: 'chat-ui-tick'}, fmt(yv)));
  }
  const axisX = Math.min(maxX, Math.max(minX, 0)), axisY = Math.min(maxY, Math.max(minY, 0));
  svg.append(svgNode('line', {x1: 64, y1: y(axisY), x2: 568, y2: y(axisY), class: 'chat-ui-axis'}));
  svg.append(svgNode('line', {x1: x(axisX), y1: 28, x2: x(axisX), y2: 270, class: 'chat-ui-axis'}));
  if (props.x_label) svg.append(svgNode('text', {x: 316, y: 313, 'text-anchor': 'middle', class: 'chat-ui-axis-label'}, props.x_label));
  if (props.y_label) svg.append(svgNode('text', {x: 64, y: 17, class: 'chat-ui-axis-label'}, props.y_label));
  return {svg, x, y};
}
export function sinePoints(source, values) {
  return Array.from({length: 256}, (_, i) => {
    const x = source.duration * i / 255;
    return {x, y: (source.offset || 0) + values.amplitude * Math.sin(2 * Math.PI * values.frequency * x + values.phase)};
  });
}
export function renderChart(definition, state, {changed, interactive}) {
  const p = definition.props, source = p.source;
  const node = document.createElement('div'); node.className = 'chat-ui-chart';
  const badge = document.createElement('p'); badge.className = 'chat-ui-data-label';
  badge.textContent = source.kind === 'sine' ? '函数演示 · 滑块仅在本机计算' : '模型提供的数据';
  const plot = document.createElement('div'); plot.className = 'chat-ui-plot-wrap';
  node.append(badge, plot);
  if (!state.parameters) state.parameters = {amplitude: source.amplitude, frequency: source.frequency, phase: source.phase || 0};
  const outputs = [];
  function draw() {
    const lines = source.kind === 'sine' ? [{name: '正弦波', points: sinePoints(source, state.parameters)}] : source.series;
    const frame = plotFrame(lines.flatMap(line => line.points), p);
    lines.forEach((line, i) => {
      const points = line.points.map(pt => `${frame.x(pt.x)},${frame.y(pt.y)}`).join(' ');
      frame.svg.append(svgNode('polyline', {points, fill: 'none', stroke: palette[i], 'stroke-width': 2.5, 'stroke-linejoin': 'round'}));
      if (line.points.length === 1) frame.svg.append(svgNode('circle', {cx: frame.x(line.points[0].x), cy: frame.y(line.points[0].y), r: 3, fill: palette[i]}));
    });
    plot.replaceChildren(frame.svg);
    for (const {key, input, output} of outputs) { input.value = state.parameters[key]; output.value = fmt(state.parameters[key]); }
  }
  if (source.kind === 'sine') {
    const controls = document.createElement('div'); controls.className = 'chat-ui-sliders';
    const ranges = {amplitude: [0, 10, .05, '振幅'], frequency: [.1, 5, .05, '频率 (Hz)'], phase: [-Math.PI, Math.PI, .01, '相位 (rad)']};
    for (const key of source.controls || []) {
      const [min, max, step, title] = ranges[key]; const label = document.createElement('label');
      const text = document.createElement('span'); text.textContent = title;
      const input = document.createElement('input'); input.type = 'range'; input.min = min; input.max = max; input.step = step;
      const output = document.createElement('output'); outputs.push({key, input, output});
      input.addEventListener('input', () => { state.parameters[key] = Number(input.value); state.dirty = true; draw(); changed(); });
      label.append(text, input, output); controls.append(label);
    }
    if ((source.controls || []).length) {
      const reset = document.createElement('button'); reset.type = 'button'; reset.className = 'chat-ui-reset'; reset.textContent = '重置参数';
      reset.addEventListener('click', () => { state.parameters = {amplitude: source.amplitude, frequency: source.frequency, phase: source.phase || 0}; state.dirty = false; draw(); changed(); });
      controls.append(reset); node.append(controls);
    }
  } else {
    const legend = document.createElement('ul'); legend.className = 'chat-ui-legend';
    source.series.forEach((line, i) => { const item = document.createElement('li'); item.textContent = line.name || `曲线 ${i + 1}`; item.dataset.color = i; legend.append(item); });
    node.append(legend);
  }
  function update() { node.querySelectorAll('input,button').forEach(el => { el.disabled = !interactive(); }); }
  draw(); update(); return {node, update, dispose() { node.querySelectorAll('input,button').forEach(el => { el.disabled = true; }); }};
}
