/** Taro 根据构建目标将本地图片导出为包内资源地址。 */
declare module '*.jpg' {
  const url: string
  export default url
}
