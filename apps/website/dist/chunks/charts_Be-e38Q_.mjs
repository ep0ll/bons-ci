function seriesToAreaPath(data, bounds, range) {
  if (data.length === 0) return "";
  const { width, height, paddingTop = 4, paddingBottom = 4, paddingLeft = 0, paddingRight = 0 } = bounds;
  const w = width - paddingLeft - paddingRight;
  const h = height - paddingTop - paddingBottom;
  const values = data.map((d) => d.value);
  const min = Math.min(...values);
  const max = Math.max(...values);
  const valueRange = max - min || 1;
  const lastX = paddingLeft + w;
  const baseline = paddingTop + h;
  const linePoints = data.map((point, i) => {
    const x = paddingLeft + i / (data.length - 1) * w;
    const y = paddingTop + h - (point.value - min) / valueRange * h;
    return `${x.toFixed(2)},${y.toFixed(2)}`;
  });
  return `M${linePoints.join("L")}L${lastX},${baseline}L${paddingLeft},${baseline}Z`;
}
function seriesToSmoothPath(data, bounds, range) {
  if (data.length < 2) return "";
  const { width, height, paddingTop = 4, paddingBottom = 4, paddingLeft = 0, paddingRight = 0 } = bounds;
  const w = width - paddingLeft - paddingRight;
  const h = height - paddingTop - paddingBottom;
  const values = data.map((d) => d.value);
  const min = Math.min(...values);
  const max = Math.max(...values);
  const valueRange = max - min || 1;
  const points = data.map((point, i) => ({
    x: paddingLeft + i / (data.length - 1) * w,
    y: paddingTop + h - (point.value - min) / valueRange * h
  }));
  let path = `M${points[0].x.toFixed(2)},${points[0].y.toFixed(2)}`;
  for (let i = 1; i < points.length; i++) {
    const prev = points[i - 1];
    const curr = points[i];
    const cpx = (prev.x + curr.x) / 2;
    path += `C${cpx.toFixed(2)},${prev.y.toFixed(2)},${cpx.toFixed(2)},${curr.y.toFixed(2)},${curr.x.toFixed(2)},${curr.y.toFixed(2)}`;
  }
  return path;
}
function generateWaterfallBars(data, bounds) {
  const { width, height, paddingTop = 10, paddingBottom = 30, paddingLeft = 60, paddingRight = 10 } = bounds;
  const usableWidth = width - paddingLeft - paddingRight;
  const usableHeight = height - paddingTop - paddingBottom;
  const maxVal = Math.max(...data.map((d) => d.max));
  const barWidth = usableWidth / data.length * 0.6;
  const barGap = usableWidth / data.length;
  const scale = (v) => usableHeight - v / maxVal * usableHeight;
  return data.map((d, i) => {
    const x = paddingLeft + i * barGap + (barGap - barWidth) / 2;
    const fmt = (ms) => ms >= 6e4 ? `${(ms / 6e4).toFixed(1)}m` : `${(ms / 1e3).toFixed(0)}s`;
    return {
      project: d.project,
      x,
      y_p50: paddingTop + scale(d.p50),
      height_p50_to_p75: Math.abs(scale(d.p50) - scale(d.p75)),
      height_p75_to_p90: Math.abs(scale(d.p75) - scale(d.p90)),
      height_p90_to_p95: Math.abs(scale(d.p90) - scale(d.p95)),
      height_p95_to_p99: Math.abs(scale(d.p95) - scale(d.p99)),
      y_max: paddingTop + scale(d.max),
      width: barWidth,
      label_p50: fmt(d.p50),
      label_p95: fmt(d.p95),
      label_p99: fmt(d.p99)
    };
  });
}
function generateHeatmapData(days = 7, hours = 24, seed = 42) {
  const cells = [];
  let s = seed;
  const rand = () => {
    s = s * 1664525 + 1013904223 & 4294967295;
    return (s >>> 0) / 4294967295;
  };
  for (let day = 0; day < days; day++) {
    const isWeekend = day >= 5;
    for (let hour = 0; hour < hours; hour++) {
      const isPeak = hour >= 9 && hour <= 17 && !isWeekend;
      const base = isPeak ? 30 : isWeekend ? 3 : 8;
      const noise = rand() * base * 0.5;
      const raw = Math.round(base + noise);
      cells.push({ day, hour, raw, value: 0 });
    }
  }
  const maxRaw = Math.max(...cells.map((c) => c.raw));
  cells.forEach((c) => c.value = c.raw / maxRaw);
  return cells;
}
function generateResourceTimeline(duration_s, cpu_avg, mem_avg_mb, seed = 1) {
  const points = [];
  const steps = Math.min(duration_s, 60);
  const step_s = duration_s / steps;
  let s = seed;
  const rand = () => {
    s = s * 1664525 + 1013904223 & 4294967295;
    return (s >>> 0) / 4294967295;
  };
  let cpu = cpu_avg * 0.2;
  let mem = mem_avg_mb * 0.6;
  for (let i = 0; i <= steps; i++) {
    const phase = i / steps;
    const envelope = phase < 0.15 ? phase / 0.15 : phase > 0.85 ? (1 - phase) / 0.15 : 1;
    cpu = Math.min(100, Math.max(0, cpu_avg * envelope + (rand() - 0.5) * 15));
    mem = Math.max(0, mem_avg_mb * (0.7 + phase * 0.3) + (rand() - 0.5) * mem_avg_mb * 0.1);
    points.push({
      t: i * step_s,
      cpu: Math.round(cpu * 10) / 10,
      mem_mb: Math.round(mem),
      net_in_mb: Math.round(rand() * 10 * 10) / 10,
      net_out_mb: Math.round(rand() * 5 * 10) / 10
    });
  }
  return points;
}
function formatBytes(bytes, decimals = 1) {
  if (bytes === 0) return "0 B";
  const k = 1024;
  const sizes = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(k)), sizes.length - 1);
  return `${(bytes / Math.pow(k, i)).toFixed(decimals)} ${sizes[i]}`;
}

export { seriesToAreaPath as a, generateHeatmapData as b, generateWaterfallBars as c, formatBytes as f, generateResourceTimeline as g, seriesToSmoothPath as s };
