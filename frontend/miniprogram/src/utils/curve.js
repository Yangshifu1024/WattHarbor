// 曲线仅接受有时间和数值的采样；null、缺失和非法时间不转换为零。
export function powerSegments(series) {
 const groups=[];let group=[];let last=null;
 for(const point of series){
  const at=Date.parse(point.ts),value=point.power_w;
  const numeric=value!==null && value!==undefined && value!=='' ? Number(value) : NaN;
  if(!Number.isFinite(at)||!Number.isFinite(numeric)||numeric<0){if(group.length)groups.push(group);group=[];last=null;continue;}
  if(last!==null && (at<=last || at-last>180000)){if(group.length)groups.push(group);group=[];}
  group.push({at,value:numeric});last=at;
 }
 if(group.length)groups.push(group);
 return groups;
}
