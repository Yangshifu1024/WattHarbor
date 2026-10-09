// 微信支付界面成功只表示客户端已提交支付；订单和设备结果仍以服务端查询为准。
function paymentParams(value) {
 if(value?.provider==='simulation' && typeof value.prepay_id==='string' && /^SIM(P[1-9][0-9]{4,18}|PAY[A-Za-z0-9]+)$/.test(value.prepay_id))return {provider:'simulation',merchantOrderNo:value.prepay_id.slice(3)};
 if(!value || value.signType!=='RSA' || !/^\d+$/.test(value.timeStamp || '') || typeof value.nonceStr!=='string' || !value.nonceStr || typeof value.paySign!=='string' || !value.paySign || !/^prepay_id=\S+$/.test(value.package || ''))throw new Error('支付参数异常，请在订单列表核实状态');
 return {timeStamp:value.timeStamp,nonceStr:value.nonceStr,package:value.package,signType:'RSA',paySign:value.paySign};
}
function newRequestId(){
 return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g,c=>{const r=Math.floor(Math.random()*16);return(c==='x'?r:(r&3)|8).toString(16);});
}
export {paymentParams,newRequestId};
